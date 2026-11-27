package proc

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"time"
)

// SetMode changes how an app runs, keeping its command, environment and
// settings: as separate processes (fork) or as workers that share the app's
// ports (cluster). target names one process or the base name of a group.
//
//   - cluster: one process with that many workers (workers 0 means one per
//     CPU core). A group of instances becomes that one process.
//   - fork with 1 worker or 0: one process, whatever it was before. Going back
//     from cluster to fork must give the app back as it was, not N copies that
//     fight over its port. With more than 1: that many separate instances,
//     name-1 .. name-N, which is only right for an app that picks its own port.
//
// The old setup is stopped first and the new one started; if the new one
// cannot start, the old one is put back. Nothing is changed when the new
// setup is refused (for example a command that is not a Node script, in
// cluster mode).
func (m *Manager) SetMode(target, mode string, workers int) ([]Info, error) {
	if target == "all" {
		return nil, errors.New("change one app at a time, not all of them")
	}
	if mode != ExecFork && mode != ExecCluster {
		return nil, fmt.Errorf("mode must be %q or %q, got %q", ExecFork, ExecCluster, mode)
	}
	if workers < 0 || workers > MaxInstances {
		return nil, fmt.Errorf("workers must be between 0 and %d, got %d", MaxInstances, workers)
	}
	old, err := m.resolve(target)
	if err != nil {
		return nil, err
	}
	specs := make([]Spec, len(old))
	for i, p := range old {
		p.mu.Lock()
		if p.status == StatusOrphaned {
			name, pid := p.spec.Name, p.pid
			p.mu.Unlock()
			return nil, orphanError(name, pid)
		}
		specs[i] = p.spec
		p.mu.Unlock()
	}
	sort.SliceStable(specs, func(i, j int) bool { return specs[i].Index < specs[j].Index })
	first := specs[0]
	if first.Group != "" && target != first.Group {
		return nil, fmt.Errorf("%s is one of %d instances of %s; change %s to change them all", target, first.Instances, first.Group, first.Group)
	}
	base := first
	name := first.Name
	if first.Group != "" {
		name = first.Group
	}
	base.Name, base.Group, base.Index = name, "", 0

	var next []Spec
	switch mode {
	case ExecCluster:
		if workers == 0 && len(specs) > 1 {
			workers = len(specs) // a group of four becomes four workers
		} else if workers == 0 && first.isCluster() {
			workers = first.Instances
		}
		s := base
		s.ExecMode, s.Instances = ExecCluster, workers
		next = []Spec{s}
	case ExecFork:
		s := base
		s.ExecMode = ""
		switch {
		case workers > 1:
			s.Instances = workers
		default:
			s.Instances = 0
		}
		next, err = Expand(s)
		if err != nil {
			return nil, err
		}
	}
	for _, s := range next {
		if err := s.Validate(); err != nil {
			return nil, err
		}
	}
	if sameSetup(specs, next) {
		return nil, fmt.Errorf("%s already runs that way", name)
	}

	wasUp := false
	for _, p := range old {
		p.mu.Lock()
		wasUp = wasUp || p.running()
		p.mu.Unlock()
	}
	// The ports the old setup holds: the new one must not start before they are
	// free, or it dies on "address already in use" and the switch looks failed.
	var held []int
	if wasUp {
		pn := m.PortNumbers()
		for _, sp := range specs {
			held = append(held, pn[sp.Name]...)
		}
	}
	each(old, func(p *Process) error { p.Stop(); return nil })
	waitPortsFree(held, 10*time.Second)

	// A single process keeps its place (and id) when only its settings change.
	if len(specs) == 1 && len(next) == 1 && specs[0].Name == next[0].Name {
		putBack := func(err error) error {
			m.Stop(specs[0].Name)
			if _, rerr := m.restartWith(old[0], specs[0], wasUp); rerr != nil {
				return fmt.Errorf("%w; and putting it back failed: %v", err, rerr)
			}
			return fmt.Errorf("%w; the previous setup was put back", err)
		}
		in, err := m.restartWith(old[0], next[0], wasUp)
		if err != nil {
			return nil, putBack(err)
		}
		if wasUp && !SkipSettle {
			if err := m.settle(next[0].Name, next[0]); err != nil {
				return nil, putBack(fmt.Errorf("the new %s setup did not work: %w", modeWord(next[0]), err))
			}
		}
		return []Info{in}, nil
	}

	if _, err := m.Delete(target); err != nil {
		return nil, err
	}
	var started []Info
	for _, s := range next {
		var in Info
		if wasUp {
			in, err = m.Start(s)
		} else {
			in, err = m.Add(s)
		}
		if err != nil {
			break
		}
		started = append(started, in)
	}
	if err == nil && wasUp && !SkipSettle {
		for _, s := range next {
			if err = m.settle(s.Name, s); err != nil {
				err = fmt.Errorf("the new %s setup did not work: %w", modeWord(s), err)
				break
			}
		}
	}
	if err != nil { // put the old setup back
		for _, s := range next {
			m.Delete(s.Name)
		}
		for _, s := range specs {
			if wasUp {
				m.Start(s)
			} else {
				m.Add(s)
			}
		}
		return nil, fmt.Errorf("%w; the previous setup was put back", err)
	}
	return started, nil
}

// restartWith gives a stopped process a new spec and starts it again when it
// was running.
func (m *Manager) restartWith(p *Process, spec Spec, start bool) (Info, error) {
	if !start {
		p.mu.Lock()
		p.spec = spec
		in := p.infoLocked()
		p.mu.Unlock()
		return in, nil
	}
	return m.Start(spec)
}

// sameSetup reports whether the processes already run the way next describes:
// the same names in the same mode with the same worker count.
func sameSetup(have, next []Spec) bool {
	if len(have) != len(next) {
		return false
	}
	for i := range have {
		a, b := have[i], next[i]
		if a.Name != b.Name || a.isCluster() != b.isCluster() || a.Group != b.Group {
			return false
		}
		if a.isCluster() && a.Workers() != b.Workers() {
			return false
		}
	}
	return true
}

// SkipSettle turns off the wait after a switch that checks the new setup works
// (only tests that do not care set it).
var SkipSettle bool

func modeWord(s Spec) string {
	if s.isCluster() {
		return "cluster"
	}
	return "fork"
}

// waitPortsFree waits until nothing listens on the ports any more, or the time
// is up.
func waitPortsFree(ports []int, d time.Duration) {
	deadline := time.Now().Add(d)
	for _, p := range ports {
		for time.Now().Before(deadline) {
			l, err := net.Listen("tcp", ":"+strconv.Itoa(p))
			if err == nil {
				l.Close()
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}
