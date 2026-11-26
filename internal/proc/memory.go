package proc

import (
	"fmt"
	"time"
)

// memoryCheckEvery is how often processes with a max_memory limit are measured.
// A variable so tests can shorten it.
var memoryCheckEvery = 5 * time.Second

// watchMemory starts the sampler the first time a process with a limit runs.
// It lives as long as the daemon; with no limits it only wakes up to find nothing to do.
func (m *Manager) watchMemory() {
	m.memOnce.Do(func() {
		go func() {
			for range time.Tick(memoryCheckEvery) {
				m.checkMemory()
			}
		}()
	})
}

// checkMemory measures every running process that has a limit and restarts the
// ones over it. Memory is the process plus everything it started (its process
// group), so `npm run start` is counted by the node it runs, not just by npm.
func (m *Manager) checkMemory() {
	type watched struct {
		p     *Process
		pid   int
		limit int64
	}
	var list []watched
	var pids []int
	m.mu.Lock()
	for _, p := range m.procs {
		p.mu.Lock()
		if limit, _ := p.spec.maxMemoryBytes(); limit > 0 && p.cmd != nil && !p.stopping && p.pid > 0 {
			list = append(list, watched{p, p.pid, limit})
			pids = append(pids, p.pid)
		}
		p.mu.Unlock()
	}
	m.mu.Unlock()
	if len(list) == 0 {
		return
	}
	used := groupMemory(pids)
	for _, w := range list {
		if n := used[w.pid]; n > w.limit {
			w.p.restartForMemory(w.pid, n, w.limit)
		}
	}
}

// restartForMemory restarts the run that has process id pid, if it is still
// the current one. It is not a crash: it does not count toward max_restarts
// (Restart resets the crash counter), but it does show in the restarts column.
func (p *Process) restartForMemory(pid int, used, limit int64) {
	p.mu.Lock()
	if p.cmd == nil || p.pid != pid || p.stopping {
		p.mu.Unlock()
		return
	}
	ev := Event{Kind: EventMemory, Process: p.spec.Name, Restarts: p.restarts + 1,
		Detail: fmt.Sprintf("used %s, the limit is %s", fmtMB(used), fmtMB(limit))}
	p.noteLocked(EventMemory, 0, ev.Detail)
	p.mu.Unlock()
	p.logger.Printf("%s: %s, restarting", p.spec.Name, ev.Detail)
	p.emit(ev)
	go func() { _ = p.Restart() }()
}

func fmtMB(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%d MB", n>>20)
}
