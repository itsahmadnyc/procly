//go:build linux

package proc

import (
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestParseProcStat(t *testing.T) {
	// A command name with spaces and parentheses, as the kernel prints it.
	// Fields after ")": state=S, ..., utime=300 (14), stime=100 (15), starttime=1000 (22).
	stat := "123 (my (odd) app) S 1 123 123 0 -1 4194304 500 0 0 0 300 100 0 0 20 0 1 0 1000 1000000 250 18446744073709551615"
	statm := "2000 250 100 10 0 300 0"
	s, ok := parseProcStat(stat, statm, 30, 4096) // up 30 s, started at 10 s: 20 s of life
	if !ok {
		t.Fatal("did not parse")
	}
	// 4 s of CPU in 20 s is 20 %.
	if math.Abs(s.cpu-20) > 0.01 {
		t.Errorf("cpu = %v, want 20", s.cpu)
	}
	if s.rss != 250*4096 {
		t.Errorf("rss = %d, want %d", s.rss, 250*4096)
	}
	if _, ok := parseProcStat("garbage", statm, 30, 4096); ok {
		t.Error("garbage parsed")
	}
	if _, ok := parseProcStat(stat, "1", 30, 4096); ok {
		t.Error("short statm parsed")
	}
}

// What the list shows must agree with ps, which it replaced.
func TestReadStatsAgreesWithPs(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("no ps")
	}
	pid := os.Getpid()
	got := readStats([]int{pid})[pid]
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Skip("ps failed: ", err)
	}
	kb, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	// The test binary's memory moves a little between the two reads.
	if diff := math.Abs(float64(got.rss/1024 - kb)); got.rss == 0 || diff > 4096 {
		t.Errorf("rss %d kB from /proc, %d kB from ps", got.rss/1024, kb)
	}
	if got.cpu < 0 {
		t.Errorf("cpu = %v", got.cpu)
	}
	if len(readStats([]int{1 << 30})) != 0 {
		t.Error("a pid that does not exist has stats")
	}
}
