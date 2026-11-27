package proc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// The launcher keeps a small file up to date with the state of each worker;
// procly reads it to show workers, to tell when a switch to cluster mode has
// settled, and writes a control file next to it to scale without a restart.

func clusterStatusPath(dir, name string) string { return filepath.Join(dir, name+".cluster.json") }
func clusterCtlPath(dir, name string) string    { return filepath.Join(dir, name+".cluster.ctl") }

// WorkerStatus is one worker of a cluster-mode app.
type WorkerStatus struct {
	Slot     int    `json:"slot"`
	PID      int    `json:"pid"`
	State    string `json:"state"` // starting, online, listening, dead
	Restarts int    `json:"restarts"`
	RSS      uint64 `json:"rss"`
	Started  int64  `json:"started"` // unix ms
}

// ClusterStatus is what the launcher reports.
type ClusterStatus struct {
	PID       int            `json:"pid"`
	Target    int            `json:"target"`
	Ready     bool           `json:"ready"` // some worker has been listening
	Reloading bool           `json:"reloading"`
	Stopping  bool           `json:"stopping"`
	Updated   int64          `json:"updated"`
	Workers   []WorkerStatus `json:"workers"`
}

// readClusterStatus reads the file of a launcher that is running with the
// given process id; a file left by an earlier run (another pid, or one that
// has not been updated for a while) is ignored.
func readClusterStatus(path string, pid int) (ClusterStatus, bool) {
	var st ClusterStatus
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &st) != nil {
		return st, false
	}
	if st.PID != pid || time.Since(time.UnixMilli(st.Updated)) > 30*time.Second {
		return st, false
	}
	return st, true
}

// live counts the workers that are up.
func (s ClusterStatus) live() (listening, up int) {
	for _, w := range s.Workers {
		switch w.State {
		case "listening":
			listening++
			up++
		case "online":
			up++
		}
	}
	return
}
