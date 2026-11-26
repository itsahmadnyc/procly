package sysinfo

import (
	"runtime"
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
