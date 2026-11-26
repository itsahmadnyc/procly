package proc

import (
	"encoding/json"
	"errors"
	"os"
	"time"
)

// RunningEntry is what the daemon remembers about a process it started, so a
// later daemon can tell which of them are still alive after the first one died.
// It holds no environment values, only what is needed to show and stop it.
type RunningEntry struct {
	Name          string    `json:"name"`
	PID           int       `json:"pid"`
	Ident         string    `json:"ident"` // see identity: guards against a reused pid
	Command       string    `json:"command"`
	Cwd           string    `json:"cwd,omitempty"`
	OutLog        string    `json:"out_log,omitempty"`
	ErrLog        string    `json:"err_log,omitempty"`
	KillTimeoutMs int       `json:"kill_timeout_ms,omitempty"`
	StartedAt     time.Time `json:"started_at"`
}

// alive reports whether the entry's process still exists and is the same one.
func (e RunningEntry) alive() bool {
	cur, ok := identity(e.PID)
	return ok && e.Ident != "" && cur == e.Ident
}

func readRunning(path string) []RunningEntry {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []RunningEntry
	if json.Unmarshal(data, &entries) != nil {
		return nil // damaged: treat as empty, the next write replaces it
	}
	return entries
}

// writeRunning replaces the file atomically. An empty list removes it.
func writeRunning(path string, entries []RunningEntry) error {
	if len(entries) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LiveOrphans returns the entries of path whose process is still running. The
// CLI uses it to notice processes left behind by a daemon that died.
func LiveOrphans(path string) []RunningEntry {
	var live []RunningEntry
	for _, e := range readRunning(path) {
		if e.alive() {
			live = append(live, e)
		}
	}
	return live
}
