//go:build !linux

package proc

import (
	"bufio"
	"bytes"
	"os/exec"
	"strconv"
	"strings"
)

// readStats asks ps(1), which behaves the same on macOS and the BSDs.
func readStats(pids []int) map[int]procStat {
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	// ps exits non-zero if any pid vanished; the rest of the output is still valid.
	out, _ := exec.Command("ps", "-o", "pid=,%cpu=,rss=", "-p", strings.Join(ids, ",")).Output()
	stats := map[int]procStat{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		cpu, err2 := strconv.ParseFloat(f[1], 64)
		rss, err3 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		stats[pid] = procStat{cpu, rss * 1024}
	}
	return stats
}

// groupMemory returns, for each process group id in groups, the resident
// memory in bytes of every process in that group.
func groupMemory(groups []int) map[int]int64 {
	want := map[int]bool{}
	for _, g := range groups {
		want[g] = true
	}
	out := map[int]int64{}
	data, _ := exec.Command("ps", "-axo", "pgid=,rss=").Output()
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		pgid, err1 := strconv.Atoi(f[0])
		rss, err2 := strconv.ParseInt(f[1], 10, 64)
		if err1 == nil && err2 == nil && want[pgid] {
			out[pgid] += rss * 1024
		}
	}
	return out
}
