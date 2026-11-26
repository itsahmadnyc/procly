package proc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Spec describes how to run a process. It is what gets saved and resurrected.
type Spec struct {
	Name    string            `json:"name" yaml:"name"`
	Command string            `json:"command" yaml:"command"`
	Cwd     string            `json:"cwd,omitempty" yaml:"cwd"`
	Env     map[string]string `json:"env,omitempty" yaml:"env"`
	// EnvFile lists .env style files (absolute paths) read on every start.
	// Later files override earlier ones and Env overrides them all.
	EnvFile        StringList `json:"env_file,omitempty" yaml:"env_file"`
	AutoRestart    *bool      `json:"autorestart,omitempty" yaml:"autorestart"`
	MaxRestarts    int        `json:"max_restarts,omitempty" yaml:"max_restarts"`
	RestartDelayMs int        `json:"restart_delay_ms,omitempty" yaml:"restart_delay_ms"`
	KillTimeoutMs  int        `json:"kill_timeout_ms,omitempty" yaml:"kill_timeout_ms"`

	// Log rotation: when a log passes LogMaxSize ("10MB" by default, "0" to
	// turn it off) it moves to <log>.1 and LogKeep (default 3) old files are kept.
	LogMaxSize string `json:"log_max_size,omitempty" yaml:"log_max_size"`
	LogKeep    *int   `json:"log_keep,omitempty" yaml:"log_keep"`

	// Health check: http(s)://... must answer 2xx/3xx, tcp://host:port must
	// accept a connection. After HealthRetries failures in a row the process
	// is restarted. Durations are like "10s"; a plain number means seconds.
	Health         string `json:"health,omitempty" yaml:"health"`
	HealthInterval string `json:"health_interval,omitempty" yaml:"health_interval"`
	HealthTimeout  string `json:"health_timeout,omitempty" yaml:"health_timeout"`
	HealthRetries  int    `json:"health_retries,omitempty" yaml:"health_retries"`
	HealthGrace    string `json:"health_grace,omitempty" yaml:"health_grace"`
}

// StringList is a list that YAML may write as one string or as a sequence
// ("env_file: .env" or "env_file: [.env, .env.local]").
type StringList []string

func (l *StringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var one string
		if err := n.Decode(&one); err != nil {
			return err
		}
		*l = StringList{one}
	case yaml.SequenceNode:
		var many []string
		if err := n.Decode(&many); err != nil {
			return err
		}
		*l = many
	default:
		return fmt.Errorf("line %d: expected a path or a list of paths", n.Line)
	}
	return nil
}

func (s Spec) ShouldAutoRestart() bool { return s.AutoRestart == nil || *s.AutoRestart }

func (s Spec) MaxRestartsOrDefault() int {
	if s.MaxRestarts <= 0 {
		return 15
	}
	return s.MaxRestarts
}

func (s Spec) restartDelay() time.Duration {
	if s.RestartDelayMs <= 0 {
		return time.Second
	}
	return time.Duration(s.RestartDelayMs) * time.Millisecond
}

func (s Spec) killTimeout() time.Duration {
	if s.KillTimeoutMs <= 0 {
		return 5 * time.Second
	}
	return time.Duration(s.KillTimeoutMs) * time.Millisecond
}

// ValidName reports whether name can be used as a process name.
func ValidName(name string) bool { return nameRe.MatchString(name) && name != "all" }

func (s Spec) Validate() error {
	if !nameRe.MatchString(s.Name) {
		return fmt.Errorf("invalid name %q: use letters, digits, '.', '_' or '-' (max 64)", s.Name)
	}
	if s.Name == "all" {
		return errors.New(`"all" is a reserved name`)
	}
	if strings.TrimSpace(s.Command) == "" {
		return errors.New("command is required")
	}
	if !filepath.IsAbs(s.Cwd) {
		return fmt.Errorf("cwd must be an absolute path, got %q", s.Cwd)
	}
	for _, f := range s.EnvFile {
		if !filepath.IsAbs(f) {
			return fmt.Errorf("env_file must be an absolute path, got %q", f)
		}
	}
	if fi, err := os.Stat(s.Cwd); err != nil || !fi.IsDir() {
		return fmt.Errorf("cwd %s is not a directory", s.Cwd)
	}
	if s.MaxRestarts < 0 || s.RestartDelayMs < 0 || s.KillTimeoutMs < 0 || s.HealthRetries < 0 {
		return errors.New("max_restarts, restart_delay_ms, kill_timeout_ms and health_retries must not be negative")
	}
	if _, err := ParseSize(s.LogMaxSize); err != nil {
		return fmt.Errorf("log_max_size: %w", err)
	}
	if s.LogKeep != nil && (*s.LogKeep < 0 || *s.LogKeep > 100) {
		return errors.New("log_keep must be between 0 and 100")
	}
	if s.Health != "" {
		if err := validHealthTarget(s.Health); err != nil {
			return err
		}
	}
	for field, v := range map[string]string{"health_interval": s.HealthInterval, "health_timeout": s.HealthTimeout, "health_grace": s.HealthGrace} {
		if _, err := parseDuration(v, time.Second); err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
	}
	return nil
}

const (
	defaultLogMaxSize = 10 << 20
	defaultLogKeep    = 3
)

// logLimits returns the rotation size (0 = never) and how many old files to keep.
func (s Spec) logLimits() (int64, int) {
	max := int64(defaultLogMaxSize)
	if s.LogMaxSize != "" {
		max, _ = ParseSize(s.LogMaxSize)
	}
	keep := defaultLogKeep
	if s.LogKeep != nil {
		keep = *s.LogKeep
	}
	return max, keep
}

// healthSettings returns grace, interval, timeout and retries with defaults.
func (s Spec) healthSettings() (grace, interval, timeout time.Duration, retries int) {
	grace, _ = parseDuration(s.HealthGrace, 10*time.Second)
	interval, _ = parseDuration(s.HealthInterval, 10*time.Second)
	timeout, _ = parseDuration(s.HealthTimeout, 3*time.Second)
	retries = s.HealthRetries
	if retries <= 0 {
		retries = 3
	}
	return grace, interval, timeout, retries
}

// ParseSize reads sizes like "10MB", "512KB", "1G" or plain bytes. Empty means unset.
func ParseSize(v string) (int64, error) {
	v = strings.ToUpper(strings.TrimSpace(v))
	if v == "" {
		return defaultLogMaxSize, nil
	}
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		m      int64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(v, u.suffix) {
			mult = u.m
			v = strings.TrimSpace(strings.TrimSuffix(v, u.suffix))
			break
		}
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid size %q, use something like 10MB", v)
	}
	return int64(n * float64(mult)), nil
}

// parseDuration reads "10s", "1m30s" or a plain number of seconds; empty gives def.
func parseDuration(v string, def time.Duration) (time.Duration, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return def, nil
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		if n <= 0 {
			return 0, fmt.Errorf("must be positive, got %q", v)
		}
		return time.Duration(n * float64(time.Second)), nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q, use something like 10s", v)
	}
	return d, nil
}
