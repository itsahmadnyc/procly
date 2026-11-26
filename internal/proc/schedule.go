package proc

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// minRestartEvery guards against a typo that would restart a process in a loop.
// Atomic, because tests change it while goroutines of earlier tests may still read it.
var minRestartEveryNs atomic.Int64

func init() { minRestartEveryNs.Store(int64(time.Minute)) }

func minRestartEvery() time.Duration { return time.Duration(minRestartEveryNs.Load()) }

// clock is the schedule's source of time, so tests can move it.
var clock atomic.Pointer[func() time.Time]

func timeNow() time.Time {
	if f := clock.Load(); f != nil {
		return (*f)()
	}
	return time.Now()
}

var clockRe = regexp.MustCompile(`^([0-9]{1,2}):([0-9]{2})$`)

// restartEvery is the interval from restart_every, 0 when there is none.
func (s Spec) restartEvery() (time.Duration, error) {
	v := strings.TrimSpace(s.RestartEvery)
	if v == "" || v == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("restart_every %q: give a duration with a unit, like 24h or 30m", s.RestartEvery)
	}
	if min := minRestartEvery(); d < min {
		return 0, fmt.Errorf("restart_every %q is too short: use at least %s", s.RestartEvery, min)
	}
	return d, nil
}

// restartClock is the time of day from restart_at ("04:00", 24 hours, the
// server's local time).
func (s Spec) restartClock() (hour, minute int, ok bool, err error) {
	v := strings.TrimSpace(s.RestartAt)
	if v == "" {
		return 0, 0, false, nil
	}
	m := clockRe.FindStringSubmatch(v)
	if m != nil {
		hour, _ = strconv.Atoi(m[1])
		minute, _ = strconv.Atoi(m[2])
	}
	if m == nil || hour > 23 || minute > 59 {
		return 0, 0, false, fmt.Errorf("restart_at %q: use a time of day like \"04:00\" (24 hours, the server's local time)", s.RestartAt)
	}
	return hour, minute, true, nil
}

func (s Spec) validateSchedule() error {
	if _, err := s.restartEvery(); err != nil {
		return err
	}
	if _, _, _, err := s.restartClock(); err != nil {
		return err
	}
	return nil
}

func (s Spec) scheduled() bool {
	return strings.TrimSpace(s.RestartEvery) != "" && strings.TrimSpace(s.RestartEvery) != "0" || strings.TrimSpace(s.RestartAt) != ""
}

// nextClock is the next time of day h:m strictly after now, in now's zone.
func nextClock(now time.Time, h, m int) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !t.After(now) {
		t = time.Date(now.Year(), now.Month(), now.Day()+1, h, m, 0, 0, now.Location())
	}
	return t
}

// nextRestart is when the schedule next fires for a run that starts at from,
// and which setting it was; zero when nothing is scheduled.
func (s Spec) nextRestart(from time.Time) (time.Time, string) {
	var next time.Time
	var why string
	if every, _ := s.restartEvery(); every > 0 {
		next, why = from.Add(every), "restart_every "+s.RestartEvery
	}
	if h, m, ok, _ := s.restartClock(); ok {
		if t := nextClock(from, h, m); next.IsZero() || t.Before(next) {
			next, why = t, "restart_at "+s.RestartAt
		}
	}
	return next, why
}

// scheduleLoop waits for the time to restart the run that cmd is, and restarts
// it. After a restart the new run has a loop of its own, so this one ends. A
// scheduled restart is not a crash: it leaves the give-up counter alone.
func (p *Process) scheduleLoop(cmd *exec.Cmd, exited <-chan struct{}, spec Spec) {
	now := timeNow()
	next, why := spec.nextRestart(now)
	if next.IsZero() {
		return
	}
	select {
	case <-exited:
		return
	case <-time.After(next.Sub(now)):
	}
	p.mu.Lock()
	if p.cmd != cmd || p.stopping {
		p.mu.Unlock()
		return
	}
	p.noteLocked(HistoryScheduled, 0, why)
	name := p.spec.Name
	p.mu.Unlock()
	p.logger.Printf("%s: scheduled restart (%s)", name, why)
	_ = p.restart(false)
}
