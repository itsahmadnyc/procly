//go:build linux

package proc

import (
	"os"
	"strconv"
	"strings"
)

// userHZ is the unit of the times in /proc/<pid>/stat. It is 100 on every
// common architecture (it is not the kernel's internal tick rate).
const userHZ = 100

// readStats reads /proc directly. Starting ps(1) for every list costs more than
// the whole rest of the request on a typical server (about 35 ms on one VPS).
func readStats(pids []int) map[int]procStat {
	stats := map[int]procStat{}
	up := uptimeSeconds()
	page := int64(os.Getpagesize())
	for _, pid := range pids {
		dir := "/proc/" + strconv.Itoa(pid)
		st, err := os.ReadFile(dir + "/stat")
		if err != nil {
			continue
		}
		sm, err := os.ReadFile(dir + "/statm")
		if err != nil {
			continue
		}
		if s, ok := parseProcStat(string(st), string(sm), up, page); ok {
			stats[pid] = s
		}
	}
	return stats
}

func uptimeSeconds() float64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// parseProcStat turns the contents of /proc/<pid>/stat and /proc/<pid>/statm
// into what ps shows: %cpu is the CPU time used divided by the time since the
// process started, and the resident size comes from statm.
func parseProcStat(stat, statm string, uptime float64, pageSize int64) (procStat, bool) {
	// The command name is in parentheses and may contain spaces and parentheses
	// itself, so count fields after the last one.
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return procStat{}, false
	}
	f := strings.Fields(stat[i+1:])
	// f[0] is field 3 (state), so field N is f[N-3]: utime 14, stime 15, starttime 22.
	if len(f) < 20 {
		return procStat{}, false
	}
	utime, err1 := strconv.ParseFloat(f[11], 64)
	stime, err2 := strconv.ParseFloat(f[12], 64)
	start, err3 := strconv.ParseFloat(f[19], 64)
	m := strings.Fields(statm)
	if err1 != nil || err2 != nil || err3 != nil || len(m) < 2 {
		return procStat{}, false
	}
	pages, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return procStat{}, false
	}
	var cpu float64
	if elapsed := uptime - start/userHZ; elapsed > 0 {
		cpu = 100 * ((utime + stime) / userHZ) / elapsed
	}
	return procStat{cpu: cpu, rss: pages * pageSize}, true
}
