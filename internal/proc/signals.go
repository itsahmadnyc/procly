package proc

import (
	"fmt"
	"sort"
	"strings"
	"syscall"
)

// signalNames are the signals a spec may use. SIGKILL is only for stopping (it
// gives the process no time to clean up); SIGSTOP and friends make no sense for either.
var signalNames = map[string]syscall.Signal{
	"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "QUIT": syscall.SIGQUIT, "TERM": syscall.SIGTERM,
	"USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2, "KILL": syscall.SIGKILL,
}

// ParseSignal accepts SIGHUP, HUP and hup alike.
func ParseSignal(name string) (syscall.Signal, error) {
	n := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(name)), "SIG")
	if sig, ok := signalNames[n]; ok {
		return sig, nil
	}
	var all []string
	for k := range signalNames {
		all = append(all, "SIG"+k)
	}
	sort.Strings(all)
	return 0, fmt.Errorf("unknown signal %q, use one of %s", name, strings.Join(all, ", "))
}

// SignalName is the canonical name (SIGHUP) of a configured signal, or the text
// as it was written when it is not a signal procly knows.
func SignalName(name string) string {
	if name == "" {
		return ""
	}
	sig, err := ParseSignal(name)
	if err != nil {
		return name
	}
	for k, v := range signalNames {
		if v == sig {
			return "SIG" + k
		}
	}
	return name
}

// stopSignal is what stopping sends first (SIGTERM unless the spec says otherwise).
func (s Spec) stopSignal() syscall.Signal {
	if s.StopSignal == "" {
		return syscall.SIGTERM
	}
	sig, err := ParseSignal(s.StopSignal)
	if err != nil {
		return syscall.SIGTERM // Validate refuses it at start; a saved spec from elsewhere still stops
	}
	return sig
}

// reloadSignal is the signal for `procly reload`, and whether one is set.
func (s Spec) reloadSignal() (syscall.Signal, bool) {
	if s.ReloadSignal == "" {
		if s.isCluster() {
			return syscall.SIGUSR2, true // the launcher replaces its workers one at a time
		}
		return 0, false
	}
	sig, err := ParseSignal(s.ReloadSignal)
	return sig, err == nil
}

func (s Spec) validateSignals() error {
	if s.StopSignal != "" {
		if _, err := ParseSignal(s.StopSignal); err != nil {
			return fmt.Errorf("stop_signal: %v", err)
		}
	}
	if s.ReloadSignal != "" {
		sig, err := ParseSignal(s.ReloadSignal)
		if err != nil {
			return fmt.Errorf("reload_signal: %v", err)
		}
		if sig == syscall.SIGKILL {
			return fmt.Errorf("reload_signal: SIGKILL cannot reload anything, it ends the process")
		}
	}
	return nil
}

// Reload sends the reload signal to the process (not its children: a master
// such as nginx or gunicorn passes it on itself). It is not a restart and does
// not touch the restart counters; the history records it.
func (p *Process) Reload() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	sig, ok := p.spec.reloadSignal()
	switch {
	case !ok:
		return fmt.Errorf("%s has no reload_signal; set one (for example reload_signal: SIGHUP) or use restart", p.spec.Name)
	case p.status == StatusOrphaned:
		return orphanError(p.spec.Name, p.pid)
	case p.cmd == nil:
		return fmt.Errorf("%s is not running, there is nothing to reload", p.spec.Name)
	}
	if err := syscall.Kill(p.pid, sig); err != nil {
		return fmt.Errorf("reload %s: %w", p.spec.Name, err)
	}
	p.noteLocked(HistoryReloaded, 0, "sent "+SignalName(p.spec.ReloadSignal))
	p.logger.Printf("%s: reloaded (%s)", p.spec.Name, SignalName(p.spec.ReloadSignal))
	return nil
}
