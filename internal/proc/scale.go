package proc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Scale changes how many workers a cluster-mode app runs, without restarting
// the others: the launcher starts the new ones one at a time, or retires the
// extra ones gracefully. n is the new count, or with relative the change to the
// current one (procly scale web +2, -1). A stopped app only has its setting
// changed, for its next start.
func (m *Manager) Scale(target string, n int, relative bool) (Info, error) {
	ps, err := m.resolve(target)
	if err != nil {
		return Info{}, err
	}
	if len(ps) != 1 || target == "all" {
		return Info{}, errors.New("scale one app at a time")
	}
	p := ps[0]
	p.mu.Lock()
	if !p.spec.isCluster() {
		name := p.spec.Name
		p.mu.Unlock()
		return Info{}, fmt.Errorf("%s does not run in cluster mode; scale changes the workers of a cluster app (procly mode %s cluster -i %d switches it)", name, name, max(n, 2))
	}
	if p.status == StatusOrphaned {
		name, pid := p.spec.Name, p.pid
		p.mu.Unlock()
		return Info{}, orphanError(name, pid)
	}
	cur := p.spec.Workers()
	if relative {
		n += cur
	}
	if n < 1 || n > MaxInstances {
		p.mu.Unlock()
		return Info{}, fmt.Errorf("workers must be between 1 and %d, not %d", MaxInstances, n)
	}
	if n == cur {
		p.mu.Unlock()
		return Info{}, fmt.Errorf("%s already runs %d workers", target, cur)
	}
	p.spec.Instances = n
	running := p.cmd != nil && p.pid > 0 && !p.stopping
	name, dir := p.spec.Name, p.logDir
	p.noteLocked(HistoryScaled, 0, fmt.Sprintf("from %d to %d workers", cur, n))
	p.mu.Unlock()
	if running {
		b, _ := json.Marshal(map[string]int{"workers": n})
		if err := os.WriteFile(clusterCtlPath(dir, name), b, 0o644); err != nil {
			return Info{}, fmt.Errorf("telling the launcher: %w", err)
		}
	}
	if p.changed != nil {
		p.changed()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.infoLocked(), nil
}
