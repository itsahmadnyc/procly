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
	MaxRestarts    int            `json:"max_restarts"`
	RestartDelayMs int64          `json:"restart_delay_ms"`
	KillTimeoutMs  int64          `json:"kill_timeout_ms"`
	Group          string         `json:"group,omitempty"`       // base name when this is one of several instances
	Index          int            `json:"index,omitempty"`       // its place in the group, from 0
	Instances      int            `json:"instances,omitempty"`   // how many there are in the group
	ExecMode       string         `json:"exec_mode,omitempty"`   // "fork" or "cluster"
	Workers        int            `json:"workers,omitempty"`     // in cluster mode, how many
	WorkerList     []WorkerStatus `json:"worker_list,omitempty"` // in cluster mode, each worker now
	WorkersUp      int            `json:"workers_up,omitempty"`
	RestartEvery   string         `json:"restart_every,omitempty"`
	RestartAt      string         `json:"restart_at,omitempty"`
	StopSignal     string         `json:"stop_signal,omitempty"`   // as configured, empty for SIGTERM
	ReloadSignal   string         `json:"reload_signal,omitempty"` // empty: the process cannot be reloaded
	MaxMemory      int64          `json:"max_memory,omitempty"`    // bytes, 0: no limit
	LogMaxBytes    int64          `json:"log_max_bytes"`           // 0: never rotated
	LogKeep        int            `json:"log_keep"`
	OutLog         string         `json:"out_log"`
	ErrLog         string         `json:"err_log"`
	HealthEveryMs  int64          `json:"health_every_ms,omitempty"`
	HealthRetries  int            `json:"health_retries,omitempty"`

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
	v.Group, v.Index = in.Spec.Group, in.Spec.Index
	if in.Spec.Group != "" {
		v.Instances = in.Spec.Instances
	}
	v.ExecMode = in.Spec.ExecMode
	if in.Spec.isCluster() {
		v.Workers = in.Spec.Workers()
		if in.Cluster != nil {
			v.WorkerList = in.Cluster.Workers
			_, v.WorkersUp = in.Cluster.live()
		}
	}
	v.RestartEvery, v.RestartAt = in.Spec.RestartEvery, in.Spec.RestartAt
	v.StopSignal, v.ReloadSignal = SignalName(in.Spec.StopSignal), SignalName(in.Spec.ReloadSignal)
	if v.ReloadSignal == "" && in.Spec.isCluster() {
		v.ReloadSignal = "SIGUSR2" // the launcher's rolling reload
	}
	v.MaxMemory, _ = in.Spec.maxMemoryBytes()
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
	seen := map[string]bool{}
	for k := range in.Spec.Env {
		if k != "PATH" {
			seen[k] = true
		}
	}
	for _, k := range in.EnvFileKeys { // names from env files, never their values
		seen[k] = true
	}
	for k := range seen {
		v.EnvKeys = append(v.EnvKeys, k)
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
