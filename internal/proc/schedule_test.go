package proc

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shortSchedule(t *testing.T) {
	old := minRestartEveryNs.Swap(int64(50 * time.Millisecond))
	t.Cleanup(func() { minRestartEveryNs.Store(old) })
}

func TestScheduleValidation(t *testing.T) {
	for _, c := range []struct {
		field, value string
		ok           bool
		want         string
	}{
		{"every", "24h", true, ""}, {"every", "90m", true, ""}, {"every", "1h30m", true, ""}, {"every", "", true, ""}, {"every", "0", true, ""},
		{"every", "5", false, "with a unit"}, {"every", "daily", false, "with a unit"}, {"every", "10s", false, "too short"},
		{"at", "04:00", true, ""}, {"at", "4:05", true, ""}, {"at", "23:59", true, ""}, {"at", "00:00", true, ""},
		{"at", "24:00", false, "time of day"}, {"at", "04:60", false, "time of day"}, {"at", "4", false, "time of day"}, {"at", "4am", false, "time of day"},
	} {
		s := spec(t, "x", "true")
		if c.field == "every" {
			s.RestartEvery = c.value
		} else {
			s.RestartAt = c.value
		}
		err := s.Validate()
		if c.ok && err != nil {
			t.Errorf("restart_%s %q should be accepted: %v", c.field, c.value, err)
		}
		if !c.ok && (err == nil || !strings.Contains(err.Error(), "restart_"+c.field) || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("restart_%s %q: error %v, want it to contain restart_%s and %q", c.field, c.value, err, c.field, c.want)
		}
	}
}

func TestNextClock(t *testing.T) {
	loc := time.FixedZone("test", 5*3600)
	at := func(h, m, s int) time.Time { return time.Date(2026, 11, 26, h, m, s, 0, loc) }
	for _, c := range []struct {
		now  time.Time
		h, m int
		want time.Time
	}{
		{at(3, 0, 0), 4, 0, at(4, 0, 0)},                                                            // later today
		{at(4, 0, 1), 4, 0, time.Date(2026, 11, 27, 4, 0, 0, 0, loc)},                               // already past: tomorrow
		{at(4, 0, 0), 4, 0, time.Date(2026, 11, 27, 4, 0, 0, 0, loc)},                               // exactly now: tomorrow, not a restart loop
		{at(23, 59, 59), 0, 0, time.Date(2026, 11, 27, 0, 0, 0, 0, loc)},                            // just before midnight
		{time.Date(2026, 12, 31, 12, 0, 0, 0, loc), 1, 30, time.Date(2027, 1, 1, 1, 30, 0, 0, loc)}, // year end
	} {
		if got := nextClock(c.now, c.h, c.m); !got.Equal(c.want) {
			t.Errorf("nextClock(%v, %02d:%02d) = %v, want %v", c.now, c.h, c.m, got, c.want)
		}
	}
}

func TestNextRestartPicksTheEarlierSetting(t *testing.T) {
	loc := time.FixedZone("test", 0)
	from := time.Date(2026, 11, 26, 10, 0, 0, 0, loc)
	s := Spec{RestartEvery: "48h", RestartAt: "11:00"}
	if next, why := s.nextRestart(from); !next.Equal(time.Date(2026, 11, 26, 11, 0, 0, 0, loc)) || why != "restart_at 11:00" {
		t.Errorf("both set: %v %q", next, why)
	}
	s = Spec{RestartEvery: "30m", RestartAt: "11:00"}
	if next, why := s.nextRestart(from); !next.Equal(from.Add(30*time.Minute)) || why != "restart_every 30m" {
		t.Errorf("every is sooner: %v %q", next, why)
	}
	if next, _ := (Spec{}).nextRestart(from); !next.IsZero() {
		t.Errorf("nothing scheduled: %v", next)
	}
}

func TestRestartEveryRestartsAndKeepsTheCrashCounter(t *testing.T) {
	shortSchedule(t)
	m, _ := newTestManager(t)
	s := spec(t, "nightly", "sleep 30")
	s.RestartEvery = "400ms"
	first, err := m.Start(s)
	if err != nil {
		t.Fatal(err)
	}
	// Pretend it has crashed a few times lately: a scheduled restart must not reset that.
	m.mu.Lock()
	p := m.procs["nightly"]
	m.mu.Unlock()
	p.mu.Lock()
	p.crashes = 3
	p.mu.Unlock()

	eventually(t, "the scheduled restart", func() bool { in, _ := m.Get("nightly"); return in.PID > 0 && in.PID != first.PID })
	in, _ := m.Get("nightly")
	if in.Restarts < 1 {
		t.Errorf("a scheduled restart shows in the restarts column: %d", in.Restarts)
	}
	p.mu.Lock()
	crashes := p.crashes
	p.mu.Unlock()
	if crashes != 3 {
		t.Errorf("a scheduled restart must leave the give-up counter alone: %d", crashes)
	}
	found := false
	for _, h := range in.History {
		if h.Kind == HistoryScheduled && h.Detail == "restart_every 400ms" {
			found = true
		}
	}
	if !found {
		t.Errorf("the history should say why: %+v", in.History)
	}
	// And it keeps happening: the new run has its own schedule.
	second := in.PID
	eventually(t, "the next scheduled restart", func() bool { in, _ := m.Get("nightly"); return in.PID > 0 && in.PID != second })
}

func TestRestartAtUsesTheClock(t *testing.T) {
	shortSchedule(t)
	loc := time.FixedZone("test", 3600)
	base := time.Date(2026, 11, 26, 3, 59, 59, 700_000_000, loc) // 300 ms before 04:00
	start := time.Now()
	fake := func() time.Time { return base.Add(time.Since(start)) }
	clock.Store(&fake)
	t.Cleanup(func() { clock.Store(nil) })

	m, _ := newTestManager(t)
	s := spec(t, "daily", "sleep 30")
	s.RestartAt = "04:00"
	first, err := m.Start(s)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the 04:00 restart", func() bool { in, _ := m.Get("daily"); return in.PID > 0 && in.PID != first.PID })
	in, _ := m.Get("daily")
	if !hasKind(in, HistoryScheduled, "restart_at 04:00") {
		t.Errorf("the history should say why: %+v", in.History)
	}
	// It does not fire again within the same minute: the next 04:00 is tomorrow.
	again := in.PID
	time.Sleep(600 * time.Millisecond)
	if in, _ := m.Get("daily"); in.PID != again {
		t.Errorf("restarted a second time at once: %d -> %d", again, in.PID)
	}
}

func hasKind(in Info, kind, detail string) bool {
	for _, h := range in.History {
		if h.Kind == kind && h.Detail == detail {
			return true
		}
	}
	return false
}

func TestScheduledRestartDoesNotRestartAStoppedProcess(t *testing.T) {
	shortSchedule(t)
	m, _ := newTestManager(t)
	s := spec(t, "stopped", "sleep 30")
	s.RestartEvery = "300ms"
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	m.Stop("stopped")
	time.Sleep(900 * time.Millisecond)
	if in, _ := m.Get("stopped"); in.PID != 0 || in.Status != StatusStopped {
		t.Errorf("a stopped process must stay stopped: %+v", in)
	}
}

func TestScheduleSurvivesSaveAndResurrect(t *testing.T) {
	shortSchedule(t)
	a, dir := newTestManager(t)
	s := spec(t, "kept", "sleep 30")
	s.RestartEvery = "400ms"
	s.RestartAt = "04:00"
	if _, err := a.Start(s); err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(dir, "dump.json")
	if n, err := a.Save(dump); err != nil || n != 1 {
		t.Fatalf("save: %d %v", n, err)
	}
	a.StopAll()

	b, _ := newTestManager(t)
	infos, err := b.Resurrect(dump)
	if err != nil || len(infos) != 1 {
		t.Fatalf("resurrect: %v %v", infos, err)
	}
	if got := infos[0].Spec; got.RestartEvery != "400ms" || got.RestartAt != "04:00" {
		t.Errorf("the schedule did not survive: %+v", got)
	}
	first := infos[0].PID
	eventually(t, "a scheduled restart after resurrect", func() bool { in, _ := b.Get("kept"); return in.PID > 0 && in.PID != first })
}

func TestViewShowsTheSchedule(t *testing.T) {
	v := NewView(Info{Name: "x", Spec: Spec{Name: "x", Command: "true", Cwd: "/", RestartEvery: "24h", RestartAt: "04:00"}})
	if v.RestartEvery != "24h" || v.RestartAt != "04:00" {
		t.Errorf("view: %+v", v)
	}
}
