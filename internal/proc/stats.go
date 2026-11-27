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
	// A cluster app is a launcher and its workers, all in one process group;
	// what the list shows for it is the sum over the group.
	var groups []int
	for _, in := range infos {
		if in.PID > 0 && in.Spec.isCluster() {
			groups = append(groups, in.PID)
		}
	}
	var groupStats map[int]procStat
	if len(groups) > 0 {
		groupStats = readGroupStats(groups)
	}
	for i := range infos {
		if s, ok := stats[infos[i].PID]; ok {
			infos[i].CPU = s.cpu
			infos[i].Memory = s.rss
		}
		if g, ok := groupStats[infos[i].PID]; ok {
			infos[i].CPU = g.cpu
			infos[i].Memory = g.rss
		}
	}
}
