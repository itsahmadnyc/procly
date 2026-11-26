package proc

import (
	"errors"
	"fmt"
	"log"
	"syscall"
	"time"
)

// An orphan is a process an earlier daemon started that is still running after
// that daemon died. The new daemon does not supervise it (nothing restarts it,
// nothing rotates its logs), but it knows it exists: it lists it, refuses to
// start a second copy under the same name, and can stop it.

// ErrOrphaned is wrapped by the error for an action that cannot be done to an
// orphan.
var ErrOrphaned = errors.New("left over from a daemon that died")

func orphanError(name string, pid int) error {
	return fmt.Errorf("%w: %s is still running as pid %d (orphaned). Stop it first with \"procly stop %s\", then start it again; \"procly resurrect\" brings back the saved list", ErrOrphaned, name, pid, name)
}

func newOrphan(id int, e RunningEntry, logger *log.Logger) *Process {
	no := false
	return &Process{
		id:        id,
		spec:      Spec{Name: e.Name, Command: e.Command, Cwd: e.Cwd, AutoRestart: &no, KillTimeoutMs: e.KillTimeoutMs},
		status:    StatusOrphaned,
		pid:       e.PID,
		startedAt: e.StartedAt,
		outLog:    e.OutLog,
		errLog:    e.ErrLog,
		ident:     e.Ident,
		logger:    logger,
	}
}

func (p *Process) isOrphan() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status == StatusOrphaned
}

// orphanAlive reports whether an orphan's process is still the one we knew.
func (p *Process) orphanAlive() bool {
	p.mu.Lock()
	pid, ident := p.pid, p.ident
	p.mu.Unlock()
	cur, ok := identity(pid)
	return ok && cur == ident
}

// stopOrphan asks the orphan's process group to stop (SIGTERM, then SIGKILL
// after the kill timeout), the way a supervised process is stopped. It checks
// that the pid is still the same process before it signals anything.
func (p *Process) stopOrphan() {
	p.mu.Lock()
	if p.status != StatusOrphaned {
		p.mu.Unlock()
		return
	}
	pid, ident, timeout, name := p.pid, p.ident, p.spec.killTimeout(), p.spec.Name
	p.mu.Unlock()

	same := func() bool {
		cur, ok := identity(pid)
		return ok && cur == ident
	}
	if same() {
		target := pid
		if pg, err := syscall.Getpgid(pid); err == nil && pg == pid {
			target = -pid // it leads its own group, as every process procly starts does
		}
		_ = syscall.Kill(target, syscall.SIGTERM)
		deadline := time.Now().Add(timeout)
		for same() && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if same() {
			p.logger.Printf("%s: orphan pid %d did not exit within %s, sending SIGKILL", name, pid, timeout)
			_ = syscall.Kill(target, syscall.SIGKILL)
			for i := 0; i < 40 && same(); i++ {
				time.Sleep(50 * time.Millisecond)
			}
		}
	}
	p.mu.Lock()
	p.status, p.pid, p.ident, p.wasOrphan = StatusStopped, 0, "", true
	p.noteLocked(HistoryStopped, 0, "an orphan of a previous daemon")
	p.mu.Unlock()
	p.logger.Printf("%s: orphan stopped", name)
	p.pidChanged()
}
