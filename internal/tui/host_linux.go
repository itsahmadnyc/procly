//go:build linux

package tui

import (
	"bytes"
	"os"
	"strconv"
	"strings"
	"sync"
)

var cpuPrev struct {
	sync.Mutex
	total, idle uint64
	ok          bool
}

// cpuPercent is the share of all cores busy since the previous call, from
// /proc/stat; -1 on the first call.
func cpuPercent() float64 {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return -1
	}
	line, _, _ := bytes.Cut(b, []byte("\n"))
	f := strings.Fields(string(line))
	if len(f) < 8 || f[0] != "cpu" {
		return -1
	}
	var total, idle uint64
	for i, s := range f[1:8] { // user nice system idle iowait irq softirq
		n, _ := strconv.ParseUint(s, 10, 64)
		total += n
		if i == 3 || i == 4 {
			idle += n
		}
	}
	cpuPrev.Lock()
	defer cpuPrev.Unlock()
	pct := -1.0
	if cpuPrev.ok && total > cpuPrev.total {
		dt, di := total-cpuPrev.total, idle-cpuPrev.idle
		pct = float64(dt-di) / float64(dt) * 100
	}
	cpuPrev.total, cpuPrev.idle, cpuPrev.ok = total, idle, true
	return pct
}
