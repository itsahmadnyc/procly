package proc

import (
	"fmt"
	"time"
)

// memoryCheckEvery is how often processes with a max_memory limit are measured.
// A variable so tests can shorten it.
var memoryCheckEvery = 5 * time.Second

// watchMemory starts the sampler the first time a process with a limit runs.
// It runs until StopAll; with no limits it only wakes up to find nothing to do.
func (m *Manager) watchMemory() {
	m.memOnce.Do(func() {
		m.memWG.Add(1)
		go func() {
			defer m.memWG.Done()
			t := time.NewTicker(memoryCheckEvery)
			defer t.Stop()
			for {
				select {
				case <-m.memDone:
					return
				case <-t.C:
					m.checkMemory()
				}
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
// the current one.
//
// After a run of any length it is not a crash: it does not count toward
// max_restarts (Restart resets the crash counter), but it shows in the restarts
// column. A run that goes over the limit within stableAfter of starting is a
// different thing: that is a limit set too low, or an app that needs more than
// it is allowed, not a leak, and restarting it every few seconds for ever helps
// nobody. Those count like quick crashes, and procly gives up after the same
// number of them.
func (p *Process) restartForMemory(pid int, used, limit int64) {
	p.mu.Lock()
	if p.cmd == nil || p.pid != pid || p.stopping {
		p.mu.Unlock()
		return
	}
	detail := fmt.Sprintf("used %s, the limit is %s", fmtMB(used), fmtMB(limit))
	ev := Event{Kind: EventMemory, Process: p.spec.Name, Restarts: p.restarts + 1, Detail: detail}
	p.noteLocked(EventMemory, 0, detail)
	quick := time.Since(p.startedAt) < stableAfter
	if quick {
		p.crashes++
	}
	giveUp := quick && p.crashes > p.spec.MaxRestartsOrDefault()
	name, quickRestarts := p.spec.Name, p.crashes-1
	p.mu.Unlock()

	if giveUp {
		p.logger.Printf("%s: %s within %s of starting, %d times in a row, giving up", name, detail, stableAfter, quickRestarts)
		p.Stop()
		p.mu.Lock()
		p.status = StatusErrored
		why := fmt.Sprintf("gave up after %d restarts for using too much memory right after starting (%s). Raise max_memory, or fix what makes it need that much.", quickRestarts, detail)
		p.noteLocked(EventErrored, 0, why)
		p.mu.Unlock()
		p.emit(Event{Kind: EventErrored, Process: name, Restarts: p.restarts, Detail: why})
		return
	}
	p.logger.Printf("%s: %s, restarting", name, detail)
	p.emit(ev)
	// Not in a goroutine of its own: the sampler waits for it, and StopAll waits for the sampler.
	if quick {
		_ = p.restart(false) // keep the count that leads to giving up
	} else {
		_ = p.Restart()
	}
}

func fmtMB(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return fmt.Sprintf("%d MB", n>>20)
}

// StopWatching ends the max_memory sampler and waits for it, without stopping
// any process. A handover uses it so this manager restarts nothing while it
// leaves; StopAll does the same before it stops everything.
func (m *Manager) StopWatching() {
	m.memStop.Do(func() { close(m.memDone) })
	m.memWG.Wait()
}
