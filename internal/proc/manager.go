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
}

func NewManager(logDir string, logger *log.Logger) *Manager {
	return &Manager{procs: map[string]*Process{}, logDir: logDir, logger: logger}
}

// Start launches a new process, or starts an existing stopped one with a new spec.
func (m *Manager) Start(spec Spec) (Info, error) {
	if err := spec.Validate(); err != nil {
		return Info{}, err
	}
	m.mu.Lock()
	p, exists := m.procs[spec.Name]
	if !exists {
		p = newProcess(m.nextID, spec, m.logDir, m.logger)
		m.nextID++
		m.procs[spec.Name] = p
	}
	m.mu.Unlock()

	p.mu.Lock()
	defer p.mu.Unlock()
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
	infos := []Info{ps[0].Info()}
	attachStats(infos)
	return infos[0], nil
}

func (m *Manager) List() []Info {
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
	return infosOf(ps), nil
}

func (m *Manager) Restart(target string) ([]Info, error) {
	ps, err := m.resolve(target)
	if err != nil {
		return nil, err
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
	infos, _ := m.Stop("all")
	return infos
}

// Save writes every process spec to path so it can be resurrected later.
func (m *Manager) Save(path string) (int, error) {
	infos := m.List()
	specs := make([]Spec, len(infos))
	for i, in := range infos {
		specs[i] = in.Spec
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
		_, exists := m.procs[s.Name]
		m.mu.Unlock()
		if exists {
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if target == "all" {
		return m.sortedLocked(), nil
	}
	if p, ok := m.procs[target]; ok {
		return []*Process{p}, nil
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
