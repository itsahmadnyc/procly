package proc

import "time"

// HistoryItem is one thing that happened to a process. The last few are kept
// in memory so the panel can show what went wrong after the fact.
type HistoryItem struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"` // started, stopped, exited, crashed, errored, unhealthy
	ExitCode int       `json:"exit_code,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

const (
	HistoryStarted = "started"
	HistoryStopped = "stopped"
	HistoryExited  = "exited" // finished on its own with autorestart off
	// HistoryReloaded: procly reload sent the reload signal; the process kept running.
	HistoryReloaded = "reloaded"
	// HistoryOrphaned: a new daemon found the process still running after the
	// daemon that started it died.
	HistoryOrphaned = "orphaned"
)

// historyKeep bounds memory per process. A crash loop adds two items per
// round (crashed, started), so this still holds a full give-up cycle.
const historyKeep = 40

// noteLocked records an item. Caller holds p.mu.
func (p *Process) noteLocked(kind string, code int, detail string) {
	p.history = append(p.history, HistoryItem{At: time.Now(), Kind: kind, ExitCode: code, Detail: detail})
	if n := len(p.history); n > historyKeep {
		p.history = append(p.history[:0:0], p.history[n-historyKeep:]...)
	}
}
