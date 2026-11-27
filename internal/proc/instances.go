package proc

import (
	"fmt"
	"sort"
	"strconv"
)

// MaxInstances bounds `instances`: more copies than this is almost certainly a typo.
const MaxInstances = 64

// Expand turns what a person wrote into what runs. A spec with instances: 4
// becomes four specs named name-1 .. name-4, each knowing its group, its place
// and the total, with its own logs. Anything else comes back as it was (instances
// of 1 means a single process with the plain name).
func Expand(s Spec) ([]Spec, error) {
	if s.Instances < 0 || s.Instances > MaxInstances {
		return nil, fmt.Errorf("%s: instances must be between 1 and %d, got %d", s.Name, MaxInstances, s.Instances)
	}
	if s.isCluster() { // its instances are workers inside one process, not copies
		if err := s.Validate(); err != nil {
			return nil, err
		}
		return []Spec{s}, nil
	}
	if s.Instances <= 1 || s.Group != "" {
		if s.Instances == 1 {
			s.Instances = 0
		}
		return []Spec{s}, nil
	}
	base := s
	base.Instances = 0
	if err := base.Validate(); err != nil {
		return nil, err
	}
	out := make([]Spec, s.Instances)
	for i := range out {
		c := s
		c.Name = s.Name + "-" + strconv.Itoa(i+1)
		c.Group, c.Index = s.Name, i
		c.Env = copyMap(s.Env)
		c.EnvFile = append(StringList(nil), s.EnvFile...)
		if s.AutoRestart != nil {
			v := *s.AutoRestart
			c.AutoRestart = &v
		}
		if s.LogKeep != nil {
			v := *s.LogKeep
			c.LogKeep = &v
		}
		if err := c.Validate(); err != nil {
			return nil, err
		}
		out[i] = c
	}
	return out, nil
}

func copyMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// groupMembers returns the processes of the group named group, by index.
// The manager's lock is held by the caller.
func (m *Manager) groupMembersLocked(group string) []*Process {
	var ps []*Process
	for _, p := range m.procs {
		p.mu.Lock()
		in := p.spec.Group == group
		p.mu.Unlock()
		if in {
			ps = append(ps, p)
		}
	}
	sort.Slice(ps, func(i, j int) bool {
		ps[i].mu.Lock()
		a := ps[i].spec.Index
		ps[i].mu.Unlock()
		ps[j].mu.Lock()
		b := ps[j].spec.Index
		ps[j].mu.Unlock()
		return a < b
	})
	return ps
}

// ErrGroup says a name stands for several processes where one was needed.
func groupError(group string, names []string) error {
	return fmt.Errorf("%s is a group of %d instances (%s ...): name one of them, or use stop, restart, reload or delete on the group", group, len(names), names[0])
}
