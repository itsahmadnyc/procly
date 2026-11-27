package sysinfo

import (
	"runtime"
	"strings"
	"testing"
)

func TestRead(t *testing.T) {
	s := Read()
	t.Logf("%+v", s)
	if s.Hostname == "" || s.CPUs < 1 || s.OS != runtime.GOOS || s.Version == "" {
		t.Fatalf("basic facts missing: %+v", s)
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return
	}
	if s.MemTotal < 64<<20 || s.UptimeS <= 0 || s.DiskTotal == 0 || s.DiskFree > s.DiskTotal {
		t.Errorf("memory, uptime or disk look wrong: %+v", s)
	}
	if s.Load[0] < 0 || s.Load[0] > float64(s.CPUs)*50 {
		t.Errorf("load = %v", s.Load)
	}
	if runtime.GOOS == "linux" && (s.MemAvailable == 0 || s.MemAvailable > s.MemTotal) {
		t.Errorf("available memory = %d of %d", s.MemAvailable, s.MemTotal)
	}
}

func TestReadHasTheDetailsAServerPageShows(t *testing.T) {
	s := Read()
	if s.User == "" || s.Timezone == "" {
		t.Errorf("who it runs as and the timezone: %+v", s)
	}
	for _, a := range s.Addrs {
		if strings.Contains(a, "127.0.0.1") || strings.Contains(a, "::1") || strings.Contains(a, "fe80") {
			t.Errorf("no loopback or link-local addresses: %s", a)
		}
	}
	if len(s.Addrs) > 8 {
		t.Errorf("at most eight addresses: %v", s.Addrs)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if s.CPUModel == "" && runtime.GOOS == "darwin" {
			t.Errorf("a CPU model on macOS: %+v", s)
		}
	}
	if a, b := nodeVersion(), nodeVersion(); a != b {
		t.Errorf("the node version is asked once in a while, not each time: %q %q", a, b)
	}
}
