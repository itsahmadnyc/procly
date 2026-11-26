package proc

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

var ErrNotFound = errors.New("process not found")

// Manager owns every supervised process in the daemon.
type Manager struct {
	mu     sync.Mutex
	procs  map[string]*Process
	nextID int
	logDir string
	logger *log.Logger
	events func(Event)

	runFile string         // where the list of running processes is kept; "" = not kept
	runMu   sync.Mutex     // serializes writes of that file
	bg      sync.WaitGroup // background writes of that file, see WaitIdle

	memOnce sync.Once      // starts the max_memory sampler, see watchMemory
	memStop sync.Once      // stops it, see StopAll
	memDone chan struct{}  // closed to stop it
	memWG   sync.WaitGroup // the sampler, so StopAll can wait for it
}

// OnEvent registers a function to call (in its own goroutine) for crashes,
// give-ups and failed health checks. Call it before starting processes.
func (m *Manager) OnEvent(fn func(Event)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = fn
}

func NewManager(logDir string, logger *log.Logger) *Manager {
	return &Manager{procs: map[string]*Process{}, logDir: logDir, logger: logger, memDone: make(chan struct{})}
}

// Start launches a new process, or starts an existing stopped one with a new spec.
func (m *Manager) Start(spec Spec) (Info, error) {
	if err := spec.Validate(); err != nil {
		return Info{}, err
	}
	if n, _ := spec.maxMemoryBytes(); n > 0 {
		m.watchMemory()
	}
	m.mu.Lock()
	// A group's base name and a plain process's name must not be the same word,
	// or "restart queue" would mean two different things.
	if spec.Group == "" {
		if g := m.groupMembersLocked(spec.Name); len(g) > 0 {
			m.mu.Unlock()
			return Info{}, fmt.Errorf("%s is a group of %d instances; delete it first, or choose another name", spec.Name, len(g))
		}
	} else if _, clash := m.procs[spec.Group]; clash {
		m.mu.Unlock()
		return Info{}, fmt.Errorf("%s is already a process; delete it first, or choose another name for the group", spec.Group)
	}
	p, exists := m.procs[spec.Name]
	if !exists {
		p = newProcess(m.nextID, spec, m.logDir, m.logger, m.events)
		p.changed = m.noteChanged
		m.nextID++
		m.procs[spec.Name] = p
	}
	m.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()
	if exists && p.status == StatusOrphaned {
		return p.infoLocked(), orphanError(spec.Name, p.pid)
	}
	if exists && p.running() {
		return p.infoLocked(), fmt.Errorf("%s is already running (use restart)", spec.Name)
	}
	p.spec = spec
	p.crashes = 0
	err := p.launchLocked()
	return p.infoLocked(), err
}

func (m *Manager) Get(target string) (Info, error) {
	ps, err := m.resolve(target)
	if err != nil {
		return Info{}, err
	}
	if len(ps) > 1 && target != "all" {
		names := make([]string, len(ps))
		for i, p := range ps {
			names[i] = p.Info().Name
		}
		return Info{}, groupError(target, names)
	}
	infos := []Info{ps[0].Info()}
	attachStats(infos)
	return infos[0], nil
}

func (m *Manager) List() []Info {
	m.pruneOrphans()
	m.mu.Lock()
	ps := m.sortedLocked()
	m.mu.Unlock()
	infos := make([]Info, len(ps))
	for i, p := range ps {
		infos[i] = p.Info()
	}
	attachStats(infos)
	return infos
}

func (m *Manager) Stop(target string) ([]Info, error) {
	ps, err := m.resolve(target)
	if err != nil {
		return nil, err
	}
	each(ps, func(p *Process) error { p.Stop(); return nil })
	m.forgetStoppedOrphans(ps)
	return infosOf(ps), nil
}

// Reload sends the reload signal. For "all" it reaches every running process
// that has one and fails only when none does; naming a process that has none fails.
func (m *Manager) Reload(target string) ([]Info, error) {
	ps, err := m.resolve(target)
	if err != nil {
		return nil, err
	}
	if target != "all" {
		if isGroup(target, ps) {
			return infosOf(ps), inOrder(ps, (*Process).Reload)
		}
		err = each(ps, (*Process).Reload)
		return infosOf(ps), err
	}
	var some []*Process
	for _, p := range ps {
		p.mu.Lock()
		_, has := p.spec.reloadSignal()
		up := p.cmd != nil
		p.mu.Unlock()
		if has && up {
			some = append(some, p)
		}
	}
	if len(some) == 0 {
		return nil, errors.New("no running process has a reload_signal; set one (for example reload_signal: SIGHUP) or use restart")
	}
	err = each(some, (*Process).Reload)
	return infosOf(some), err
}

func (m *Manager) Restart(target string) ([]Info, error) {
	ps, err := m.resolve(target)
	if err != nil {
		return nil, err
	}
	if isGroup(target, ps) {
		return infosOf(ps), inOrder(ps, (*Process).Restart) // one at a time: the others keep serving
	}
	err = each(ps, (*Process).Restart)
	return infosOf(ps), err
}

func (m *Manager) Delete(target string) ([]Info, error) {
	ps, err := m.resolve(target)
	if err != nil {
		return nil, err
	}
	each(ps, func(p *Process) error { p.Stop(); return nil })
	infos := infosOf(ps)
	m.mu.Lock()
	for _, p := range ps {
		if m.procs[p.spec.Name] == p {
			delete(m.procs, p.spec.Name)
		}
	}
	m.mu.Unlock()
	return infos, nil
}

func (m *Manager) StopAll() []Info {
	// The memory sampler goes first, and StopAll waits for it, so a restart it
	// was in the middle of cannot bring a process back after this returns.
	m.memStop.Do(func() { close(m.memDone) })
	m.memWG.Wait()
	infos, _ := m.Stop("all")
	return infos
}

// Save writes every process spec to path so it can be resurrected later.
func (m *Manager) Save(path string) (int, error) {
	var specs []Spec
	for _, in := range m.List() {
		if in.Status == StatusOrphaned {
			continue // its spec is a sketch without the environment; saving it would lose that
		}
		specs = append(specs, in.Spec)
	}
	if specs == nil {
		specs = []Spec{}
	}
	data, err := json.MarshalIndent(specs, "", "  ")
	if err != nil {
		return 0, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return 0, err
	}
	return len(specs), os.Rename(tmp, path)
}

// Resurrect starts every saved process that is not already known.
func (m *Manager) Resurrect(path string) ([]Info, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var specs []Spec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	var infos []Info
	var errs []error
	for _, s := range specs {
		m.mu.Lock()
		p, exists := m.procs[s.Name]
		m.mu.Unlock()
		if exists {
			if p.isOrphan() {
				m.logger.Printf("%s: not started, it is still running from before the daemon died; stop it first", s.Name)
			}
			continue
		}
		info, err := m.Start(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		infos = append(infos, info)
	}
	return infos, errors.Join(errs...)
}

// resolve accepts a name, a numeric id, or "all".
func (m *Manager) resolve(target string) ([]*Process, error) {
	m.pruneOrphans()
	m.mu.Lock()
	defer m.mu.Unlock()
	if target == "all" {
		return m.sortedLocked(), nil
	}
	if p, ok := m.procs[target]; ok {
		return []*Process{p}, nil
	}
	if ps := m.groupMembersLocked(target); len(ps) > 0 {
		return ps, nil // the base name of instances stands for all of them
	}
	if id, err := strconv.Atoi(target); err == nil {
		for _, p := range m.procs {
			if p.id == id {
				return []*Process{p}, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, target)
}

func (m *Manager) sortedLocked() []*Process {
	ps := make([]*Process, 0, len(m.procs))
	for _, p := range m.procs {
		ps = append(ps, p)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].id < ps[j].id })
	return ps
}

// each runs fn on every process concurrently and joins the errors.
// isGroup reports whether target named several processes, as a group's base name does.
func isGroup(target string, ps []*Process) bool {
	if target == "all" || len(ps) == 0 {
		return false
	}
	ps[0].mu.Lock()
	defer ps[0].mu.Unlock()
	return ps[0].spec.Group == target
}

// inOrder runs fn on each process in turn and gathers the errors; a restart
// that goes one by one never takes every instance down at once.
func inOrder(ps []*Process, fn func(*Process) error) error {
	var errs []error
	for _, p := range ps {
		if err := fn(p); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func each(ps []*Process, fn func(*Process) error) error {
	var wg sync.WaitGroup
	errs := make([]error, len(ps))
	for i, p := range ps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = fn(p)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func infosOf(ps []*Process) []Info {
	infos := make([]Info, len(ps))
	for i, p := range ps {
		infos[i] = p.Info()
	}
	return infos
}

// TrackRunning makes the manager keep, in path, a list of the processes it has
// started and that are still running. A later daemon reads it to find out which
// of them outlived this one. Call it before starting anything.
func (m *Manager) TrackRunning(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runFile = path
}

// RecognizeOrphans looks at the list a previous daemon left and takes in every
// process that is still running as an orphan: listed, refused a second copy,
// stoppable, but not supervised. Call it once at startup, before resurrecting.
func (m *Manager) RecognizeOrphans() []Info {
	m.mu.Lock()
	path := m.runFile
	m.mu.Unlock()
	if path == "" {
		return nil
	}
	var found []*Process
	for _, e := range LiveOrphans(path) {
		m.mu.Lock()
		if _, exists := m.procs[e.Name]; exists {
			m.mu.Unlock()
			continue
		}
		p := newOrphan(m.nextID, e, m.logger)
		p.changed = m.noteChanged
		m.nextID++
		m.procs[e.Name] = p
		m.mu.Unlock()

		p.mu.Lock()
		p.noteLocked(HistoryOrphaned, 0, "still running as pid "+strconv.Itoa(e.PID)+" after the daemon that started it died")
		p.mu.Unlock()
		m.logger.Printf("%s: still running as pid %d, left over from a daemon that died; listed as orphaned, not started again", e.Name, e.PID)
		found = append(found, p)
	}
	m.FlushRunning() // drops the entries that did not survive
	return infosOf(found)
}

func (m *Manager) noteChanged() {
	m.bg.Add(1)
	go func() {
		defer m.bg.Done()
		m.FlushRunning()
	}()
}

// WaitIdle returns once every background write of the running file has
// finished. Call it after StopAll (which returns after all the Add calls) and
// before the final FlushRunning, so a late write cannot undo the final one.
func (m *Manager) WaitIdle() { m.bg.Wait() }

// FlushRunning writes the list of running processes now. The manager does it
// by itself whenever a process starts or ends; the daemon calls it once more
// as it shuts down.
func (m *Manager) FlushRunning() {
	m.mu.Lock()
	path := m.runFile
	ps := m.sortedLocked()
	m.mu.Unlock()
	if path == "" {
		return
	}
	m.runMu.Lock()
	defer m.runMu.Unlock()
	var entries []RunningEntry
	for _, p := range ps {
		p.mu.Lock()
		e := RunningEntry{
			Name: p.spec.Name, PID: p.pid, Ident: p.ident, Command: p.spec.Command, Cwd: p.spec.Cwd,
			OutLog: p.outLog, ErrLog: p.errLog, KillTimeoutMs: p.spec.KillTimeoutMs, StartedAt: p.startedAt,
		}
		running := p.pid > 0 && (p.cmd != nil || p.status == StatusOrphaned)
		p.mu.Unlock()
		if !running {
			continue
		}
		if e.Ident == "" {
			// A child we have not reaped keeps its pid, so the pid is still ours here.
			id, ok := identity(e.PID)
			if !ok {
				continue
			}
			e.Ident = id
			p.mu.Lock()
			if p.pid == e.PID && p.ident == "" {
				p.ident = id
			}
			p.mu.Unlock()
		}
		// A child that was just stopped can still have its cmd set for a moment;
		// only write what is really alive, so a clean shutdown leaves nothing.
		if !e.alive() {
			continue
		}
		entries = append(entries, e)
	}
	if err := writeRunning(path, entries); err != nil {
		m.logger.Printf("writing %s: %v", filepath.Base(path), err)
	}
}

// pruneOrphans forgets orphans that have exited on their own since we last looked.
func (m *Manager) pruneOrphans() {
	m.mu.Lock()
	var orphans []*Process
	for _, p := range m.procs {
		orphans = append(orphans, p)
	}
	m.mu.Unlock()
	for _, p := range orphans {
		if !p.isOrphan() || p.orphanAlive() {
			continue
		}
		m.mu.Lock()
		if m.procs[p.spec.Name] == p {
			delete(m.procs, p.spec.Name)
		}
		m.mu.Unlock()
		m.logger.Printf("%s: the orphan has exited", p.spec.Name)
	}
}

// forgetStoppedOrphans drops orphans that were just stopped. A normal process
// stays listed as stopped so it can be started again, but an orphan's spec has
// no environment, so it must not be started from it.
func (m *Manager) forgetStoppedOrphans(ps []*Process) {
	for _, p := range ps {
		p.mu.Lock()
		was, name := p.wasOrphan, p.spec.Name
		p.mu.Unlock()
		if !was {
			continue
		}
		m.mu.Lock()
		if m.procs[name] == p {
			delete(m.procs, name)
		}
		m.mu.Unlock()
	}
}
