package proc

import (
	"sort"
	"time"
)

// View is the shape of a process sent over the network (web panel, agents).
// It carries uptime instead of a start time, so clock differences between
// machines don't matter, and environment variable names but never values.
type View struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Status      Status   `json:"status"`
	PID         int      `json:"pid"`
	Restarts    int      `json:"restarts"`
	UptimeMs    int64    `json:"uptime_ms"`
	ExitCode    int      `json:"exit_code"`
	CPU         float64  `json:"cpu"`
	Memory      int64    `json:"memory"`
	Command     string   `json:"command"`
	Cwd         string   `json:"cwd"`
	AutoRestart bool     `json:"autorestart"`
	EnvKeys     []string `json:"env_keys"`
}

func NewView(in Info) View {
	v := View{
		ID: in.ID, Name: in.Name, Status: in.Status, PID: in.PID, Restarts: in.Restarts,
		ExitCode: in.ExitCode, CPU: in.CPU, Memory: in.Memory,
		Command: in.Spec.Command, Cwd: in.Spec.Cwd, AutoRestart: in.Spec.ShouldAutoRestart(),
		EnvKeys: []string{},
	}
	if in.PID > 0 && !in.StartedAt.IsZero() {
		v.UptimeMs = time.Since(in.StartedAt).Milliseconds()
	}
	for k := range in.Spec.Env {
		if k != "PATH" {
			v.EnvKeys = append(v.EnvKeys, k)
		}
	}
	sort.Strings(v.EnvKeys)
	return v
}

func NewViews(infos []Info) []View {
	out := make([]View, len(infos))
	for i, in := range infos {
		out[i] = NewView(in)
	}
	return out
}
