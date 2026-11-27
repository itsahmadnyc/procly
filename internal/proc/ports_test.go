package proc

import (
	"net"
	"net/netip"
	"testing"
	"time"
)

func freePort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

func TestWaitListening(t *testing.T) {
	port := freePort(t)
	addr := netip.AddrPortFrom(netip.IPv4Unspecified(), port) // 0.0.0.0 is tried on loopback

	go func() {
		time.Sleep(300 * time.Millisecond)
		l, err := net.Listen("tcp", netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port).String())
		if err != nil {
			return
		}
		time.Sleep(3 * time.Second)
		l.Close()
	}()
	began := time.Now()
	if !WaitListening([]netip.AddrPort{addr}, 5*time.Second, nil) {
		t.Fatal("the port opened after 0.3 s")
	}
	if took := time.Since(began); took < 250*time.Millisecond {
		t.Fatalf("returned before the port opened: %v", took)
	}

	closed := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), freePort(t))
	began = time.Now()
	if WaitListening([]netip.AddrPort{closed}, 400*time.Millisecond, nil) {
		t.Fatal("nothing listens there")
	}
	if took := time.Since(began); took < 350*time.Millisecond || took > 2*time.Second {
		t.Fatalf("should give up after the limit: %v", took)
	}

	began = time.Now()
	if WaitListening([]netip.AddrPort{closed}, 10*time.Second, func() bool { return false }) {
		t.Fatal("nothing listens there")
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("a dead process ends the wait at once: %v", took)
	}
}
