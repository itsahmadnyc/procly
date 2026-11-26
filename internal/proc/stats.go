package proc

// procStat is what the list shows for one running process.
type procStat struct {
	cpu float64 // percent of one core, averaged over the process's life (as ps shows it)
	rss int64   // resident set size in bytes
}

// attachStats fills CPU and memory for running processes. Where it can, it
// reads /proc (stats_linux.go); elsewhere it asks ps(1) (stats_other.go).
func attachStats(infos []Info) {
	var pids []int
	for _, in := range infos {
		if in.PID > 0 {
			pids = append(pids, in.PID)
		}
	}
	if len(pids) == 0 {
		return
	}
	stats := readStats(pids)
	for i := range infos {
		if s, ok := stats[infos[i].PID]; ok {
			infos[i].CPU = s.cpu
			infos[i].Memory = s.rss
		}
	}
}
