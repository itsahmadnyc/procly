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
	Health      Health   `json:"health,omitempty"`
	HealthCheck string   `json:"health_check,omitempty"`
	HealthError string   `json:"health_error,omitempty"`

	// Settings, with defaults filled in, so the panel shows what really applies.
	MaxRestarts    int    `json:"max_restarts"`
	RestartDelayMs int64  `json:"restart_delay_ms"`
	KillTimeoutMs  int64  `json:"kill_timeout_ms"`
	LogMaxBytes    int64  `json:"log_max_bytes"` // 0: never rotated
	LogKeep        int    `json:"log_keep"`
	OutLog         string `json:"out_log"`
	ErrLog         string `json:"err_log"`
	HealthEveryMs  int64  `json:"health_every_ms,omitempty"`
	HealthRetries  int    `json:"health_retries,omitempty"`

	// Events is the recent history, newest first, with times as ages for
	// the same reason as UptimeMs.
	Events []ViewEvent `json:"events"`
}

type ViewEvent struct {
	Kind     string `json:"kind"`
	AgoMs    int64  `json:"ago_ms"`
	ExitCode int    `json:"exit_code,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

func NewView(in Info) View {
	v := View{
		ID: in.ID, Name: in.Name, Status: in.Status, PID: in.PID, Restarts: in.Restarts,
		ExitCode: in.ExitCode, CPU: in.CPU, Memory: in.Memory,
		Command: in.Spec.Command, Cwd: in.Spec.Cwd, AutoRestart: in.Spec.ShouldAutoRestart(),
		EnvKeys: []string{}, Health: in.Health, HealthCheck: in.Spec.Health, HealthError: in.HealthErr,
	}
	if in.PID > 0 && !in.StartedAt.IsZero() {
		v.UptimeMs = time.Since(in.StartedAt).Milliseconds()
	}
	v.MaxRestarts = in.Spec.MaxRestartsOrDefault()
	v.RestartDelayMs = in.Spec.restartDelay().Milliseconds()
	v.KillTimeoutMs = in.Spec.killTimeout().Milliseconds()
	v.LogMaxBytes, v.LogKeep = in.Spec.logLimits()
	v.OutLog, v.ErrLog = in.OutLog, in.ErrLog
	if in.Spec.Health != "" {
		_, every, _, retries := in.Spec.healthSettings()
		v.HealthEveryMs, v.HealthRetries = every.Milliseconds(), retries
	}
	v.Events = make([]ViewEvent, 0, len(in.History))
	for i := len(in.History) - 1; i >= 0; i-- {
		h := in.History[i]
		v.Events = append(v.Events, ViewEvent{Kind: h.Kind, AgoMs: time.Since(h.At).Milliseconds(), ExitCode: h.ExitCode, Detail: h.Detail})
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
