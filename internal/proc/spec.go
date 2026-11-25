package proc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Spec describes how to run a process. It is what gets saved and resurrected.
type Spec struct {
	Name           string            `json:"name" yaml:"name"`
	Command        string            `json:"command" yaml:"command"`
	Cwd            string            `json:"cwd,omitempty" yaml:"cwd"`
	Env            map[string]string `json:"env,omitempty" yaml:"env"`
	AutoRestart    *bool             `json:"autorestart,omitempty" yaml:"autorestart"`
	MaxRestarts    int               `json:"max_restarts,omitempty" yaml:"max_restarts"`
	RestartDelayMs int               `json:"restart_delay_ms,omitempty" yaml:"restart_delay_ms"`
	KillTimeoutMs  int               `json:"kill_timeout_ms,omitempty" yaml:"kill_timeout_ms"`
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
	if fi, err := os.Stat(s.Cwd); err != nil || !fi.IsDir() {
		return fmt.Errorf("cwd %s is not a directory", s.Cwd)
	}
	if s.MaxRestarts < 0 || s.RestartDelayMs < 0 || s.KillTimeoutMs < 0 {
		return errors.New("max_restarts, restart_delay_ms and kill_timeout_ms must not be negative")
	}
	return nil
}
