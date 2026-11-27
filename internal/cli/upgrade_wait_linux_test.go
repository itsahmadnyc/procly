//go:build linux

package cli

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// TestHelperSlowServer is not a test: apps in TestUpgradeWaitsUntilAppsServe
// run the test binary, which waits PROCLY_TEST_DELAY and then listens on
// PROCLY_TEST_PORT, like a server that takes a while to boot.
func TestHelperSlowServer(t *testing.T) {
	port := os.Getenv("PROCLY_TEST_PORT")
	if port == "" {
		t.Skip("helper process only")
	}
	d, _ := time.ParseDuration(os.Getenv("PROCLY_TEST_DELAY"))
	time.Sleep(d)
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
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

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func eventually(within time.Duration, ok func() bool) bool {
	for deadline := time.Now().Add(within); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return true
		}
	}
	return false
}

func answers(port int) bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Two servers that take 0.7 s to listen: the upgrade must wait for the first
// to serve again before it takes the second down, so they are never both down.
func TestUpgradeWaitsUntilAppsServe(t *testing.T) {
	oldBin, newBin := buildVersion(t, "1.0.0"), buildVersion(t, "2.0.0")
	base, latest := releaseMirror(t, newBin, "2.0.0", false)
	mirror := []string{"PROCLY_DOWNLOAD_BASE=" + base, "PROCLY_LATEST_URL=" + latest}
	installed := t.TempDir() + "/bin/procly"
	copyExe(t, oldBin, installed)

	c := newCLI(t)
	p1, p2 := freeTCPPort(t), freeTCPPort(t)
	for i, p := range []int{p1, p2} {
		c.file(fmt.Sprintf("srv%d.sh", i+1), fmt.Sprintf("PROCLY_TEST_PORT=%d PROCLY_TEST_DELAY=700ms exec %s -test.run='^TestHelperSlowServer$'\n", p, os.Args[0]))
		c.okExe(installed, nil, "start", fmt.Sprintf("srv%d.sh", i+1), "--name", fmt.Sprintf("srv%d", i+1))
	}
	if !eventually(10*time.Second, func() bool { return answers(p1) && answers(p2) }) {
		for _, n := range []string{"srv1-out.log", "srv1-error.log"} {
			b, _ := os.ReadFile(c.home + "/logs/" + n)
			t.Logf("%s:\n%s", n, b)
		}
		t.Log(c.okExe(installed, nil, "ls"))
		t.Fatal("the servers never listened")
	}

	var bothDown atomic.Int32
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if !answers(p1) && !answers(p2) {
				bothDown.Add(1)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	out := c.okExe(installed, mirror, "upgrade")
	waitFor(t, "both servers to serve again", func() bool { return answers(p1) && answers(p2) })
	close(stop)
	<-done

	if n := bothDown.Load(); n > 0 {
		t.Errorf("both servers were down at once in %d probes:\n%s", n, out)
	}
	contains(t, out, "restarted srv1", fmt.Sprintf("serving on 127.0.0.1:%d", p1), "restarted srv2", fmt.Sprintf("serving on 127.0.0.1:%d", p2))
}
