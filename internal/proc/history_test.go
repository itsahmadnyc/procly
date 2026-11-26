package proc

import (
	"strings"
	"testing"
)

func kinds(h []HistoryItem) string {
	var k []string
	for _, it := range h {
		k = append(k, it.Kind)
	}
	return strings.Join(k, ",")
}

func TestHistoryRecordsCrashesAndGivingUp(t *testing.T) {
	m, _ := newTestManager(t)
	s := spec(t, "flaky", "echo boom >&2; exit 3")
	s.MaxRestarts, s.RestartDelayMs = 1, 10
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	in := waitStatus(t, m, "flaky", StatusErrored)
	if got, want := kinds(in.History), "started,crashed,started,errored"; got != want {
		t.Fatalf("history = %s, want %s", got, want)
	}
	crash := in.History[1]
	if crash.ExitCode != 3 || !strings.Contains(crash.Detail, "boom") {
		t.Errorf("crash item = %+v, want exit 3 with the stderr line", crash)
	}
	if !strings.Contains(in.History[3].Detail, "gave up") {
		t.Errorf("errored detail = %q", in.History[3].Detail)
	}

	v := NewView(in)
	if len(v.Events) != 4 || v.Events[0].Kind != EventErrored || v.Events[3].Kind != HistoryStarted {
		t.Fatalf("view events should be newest first, got %+v", v.Events)
	}
	if v.Events[0].AgoMs < 0 || v.Events[0].AgoMs > v.Events[3].AgoMs {
		t.Errorf("ages out of order: %+v", v.Events)
	}
	if v.MaxRestarts != 1 || v.RestartDelayMs != 10 || v.KillTimeoutMs != 5000 || v.LogKeep != 3 || v.LogMaxBytes == 0 {
		t.Errorf("settings with defaults = %+v", v)
	}
}

func TestHistoryStopAndCleanExit(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.Start(spec(t, "sleeper", "sleep 30")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("sleeper"); err != nil {
		t.Fatal(err)
	}
	if in, _ := m.Get("sleeper"); kinds(in.History) != "started,stopped" {
		t.Errorf("history = %s", kinds(in.History))
	}

	once := spec(t, "once", "true")
	once.AutoRestart = off()
	if _, err := m.Start(once); err != nil {
		t.Fatal(err)
	}
	if in := waitStatus(t, m, "once", StatusStopped); kinds(in.History) != "started,exited" {
		t.Errorf("history = %s", kinds(in.History))
	}
}

func TestHistoryIsBounded(t *testing.T) {
	p := &Process{}
	for i := 0; i < historyKeep+15; i++ {
		p.noteLocked(HistoryStarted, i, "")
	}
	if len(p.history) != historyKeep || p.history[0].ExitCode != 15 {
		t.Fatalf("kept %d items starting at %d", len(p.history), p.history[0].ExitCode)
	}
}

func TestCrashDetailOnlyHasThisRun(t *testing.T) {
	m, _ := newTestManager(t)
	// Each run prints which attempt it is, so repeats would be visible.
	s := spec(t, "loop", `n=$(cat count 2>/dev/null || echo 0); n=$((n+1)); echo $n > count; echo "attempt $n" >&2; exit 1`)
	s.MaxRestarts, s.RestartDelayMs = 2, 10
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	in := waitStatus(t, m, "loop", StatusErrored)
	var crashes []string
	for _, it := range in.History {
		if it.Kind == EventCrashed {
			crashes = append(crashes, it.Detail)
		}
	}
	if len(crashes) != 2 || crashes[0] != "attempt 1" || crashes[1] != "attempt 2" {
		t.Fatalf("crash details = %q, want each run's own line", crashes)
	}
	last := in.History[len(in.History)-1].Detail
	if !strings.HasSuffix(last, "attempt 3") || strings.Contains(last, "attempt 2") {
		t.Errorf("give-up detail = %q", last)
	}
}
