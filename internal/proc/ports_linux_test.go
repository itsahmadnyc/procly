//go:build linux

package proc

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHelperListener is not a test: managed processes in these tests run the
// test binary with PROCLY_TEST_PORTS set, and it waits PROCLY_TEST_DELAY,
// then listens on its instance's port until it is killed.
func TestHelperListener(t *testing.T) {
	ports := os.Getenv("PROCLY_TEST_PORTS")
	if ports == "" {
		t.Skip("helper process only")
	}
	list := strings.Split(ports, ",")
	i, _ := strconv.Atoi(os.Getenv("PROCLY_INSTANCE"))
	d, _ := time.ParseDuration(os.Getenv("PROCLY_TEST_DELAY"))
	time.Sleep(d)
	l, err := net.Listen("tcp", "127.0.0.1:"+list[i%len(list)])
	if err != nil {
		os.Exit(3)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			os.Exit(0)
		}
		c.Close()
	}
}

func listenerSpec(t *testing.T, name string, delay time.Duration, ports ...uint16) Spec {
	var ps []string
	for _, p := range ports {
		ps = append(ps, strconv.Itoa(int(p)))
	}
	s := spec(t, name, "exec "+os.Args[0]+" -test.run=^TestHelperListener$")
	s.Env = map[string]string{"PROCLY_TEST_PORTS": strings.Join(ps, ","), "PROCLY_TEST_DELAY": delay.String()}
	return s
}

func TestListeningPorts(t *testing.T) {
	m, _ := newTestManager(t)
	port := freePort(t)
	in, err := m.Start(listenerSpec(t, "srv", 0, port))
	if err != nil {
		t.Fatal(err)
	}
	want := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port)
	eventually(t, "the helper to listen", func() bool { return dialable(want) })
	got := ListeningPorts(in.PID)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("ports of the group: %v, want [%v]", got, want)
	}

	quiet, err := m.Start(spec(t, "quiet", "sleep 30"))
	if err != nil {
		t.Fatal(err)
	}
	if got := ListeningPorts(quiet.PID); len(got) != 0 {
		t.Fatalf("a process with no sockets listens on nothing: %v", got)
	}
}

func TestParseListening(t *testing.T) {
	table := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 111 1 0000000000000000 100 0 0 10 0
   1: 00000000:165D 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 222 1 0000000000000000 100 0 0 10 0
   2: 0100007F:1F90 0100007F:D2F0 01 00000000:00000000 00:00000000 00000000     0        0 111 1 0000000000000000 20 4 30 10 -1
   3: 0100007F:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 333 1 0000000000000000 100 0 0 10 0
`
	got := parseListening(table, map[string]bool{"111": true, "222": true})
	if fmt.Sprint(got) != "[127.0.0.1:8080 0.0.0.0:5725]" {
		t.Fatalf("listening sockets of ours only: %v", got)
	}
	v6 := `  sl  local_address                         remote_address                        st
   0: 00000000000000000000000001000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 444 1
   1: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 555 1
`
	got = parseListening(v6, map[string]bool{"444": true, "555": true})
	if fmt.Sprint(got) != "[[::1]:3000 [::]:8080]" {
		t.Fatalf("ipv6: %v", got)
	}
}

// A rolling restart of servers that take a while to listen must never have
// all of them down at once.
func TestGroupRestartWaitsUntilEachServes(t *testing.T) {
	m, _ := newTestManager(t)
	p1, p2 := freePort(t), freePort(t)
	s := listenerSpec(t, "web", 700*time.Millisecond, p1, p2)
	s.Instances = 2
	specs, err := Expand(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range specs {
		if _, err := m.Start(sp); err != nil {
			t.Fatal(err)
		}
	}
	a1 := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), p1)
	a2 := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), p2)
	eventually(t, "both to listen", func() bool { return dialable(a1) && dialable(a2) })

	var mu sync.Mutex
	bothDown := 0
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if !dialable(a1) && !dialable(a2) {
				mu.Lock()
				bothDown++
				mu.Unlock()
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	began := time.Now()
	if _, err := m.Restart("web"); err != nil {
		t.Fatal(err)
	}
	took := time.Since(began)
	if !dialable(a1) || !dialable(a2) {
		t.Error("the restart should return once the last instance serves again")
	}
	eventually(t, "both to serve again", func() bool { return dialable(a1) && dialable(a2) })
	close(stop)
	<-done
	if bothDown > 0 {
		t.Errorf("both instances were down at once in %d probes", bothDown)
	}
	if took < 1300*time.Millisecond {
		t.Errorf("two servers that take 0.7 s each to listen were restarted in %v: the restart did not wait", took)
	}
}
