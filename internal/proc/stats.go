package proc

import (
	"bufio"
	"bytes"
	"os/exec"
	"strconv"
	"strings"
)

// attachStats fills CPU and memory for running processes using ps(1),
// which behaves the same on Linux and macOS.
func attachStats(infos []Info) {
	var pids []string
	for _, in := range infos {
		if in.PID > 0 {
			pids = append(pids, strconv.Itoa(in.PID))
		}
	}
	if len(pids) == 0 {
		return
	}
	// ps exits non-zero if any pid vanished; the rest of the output is still valid.
	out, _ := exec.Command("ps", "-o", "pid=,%cpu=,rss=", "-p", strings.Join(pids, ",")).Output()
	type stat struct {
		cpu float64
		rss int64
	}
	stats := map[int]stat{}
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
		stats[pid] = stat{cpu, rss * 1024}
	}
	for i := range infos {
		if s, ok := stats[infos[i].PID]; ok {
			infos[i].CPU = s.cpu
			infos[i].Memory = s.rss
		}
	}
}
