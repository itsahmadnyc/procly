package proc

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// growBy is a process that takes about mb megabytes, then sleeps. It needs
// python3, which every machine that runs these tests has.
func growBy(t *testing.T, mb int) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	return `python3 -c "x = bytearray(` + strconv.Itoa(mb) + ` * 1024 * 1024); import time; time.sleep(60)"`
}

func fastMemoryChecks(t *testing.T) {
	old := memoryCheckEvery
	memoryCheckEvery = 100 * time.Millisecond
	t.Cleanup(func() { memoryCheckEvery = old })
}

// slowSampler stops the background sampler from getting in the way, so a test
// can call checkMemory itself at the moment it wants.
func slowSampler(t *testing.T) {
	old := memoryCheckEvery
	memoryCheckEvery = time.Hour
	t.Cleanup(func() { memoryCheckEvery = old })
}

// overTheLimit waits until the process is really using more than limit.
func overTheLimit(t *testing.T, m *Manager, name string, limit int64) {
	t.Helper()
	eventually(t, name+" to use more than its limit", func() bool {
		in, err := m.Get(name)
		return err == nil && in.PID > 0 && groupMemory([]int{in.PID})[in.PID] > limit
	})
}

// A leak shows after the process has been up a while: that is restarted without
// counting as a crash.
func TestMaxMemoryRestartsALeakWithoutCountingItAsACrash(t *testing.T) {
	slowSampler(t)
	m, _ := newTestManager(t)
	got := make(chan Event, 4)
	m.OnEvent(func(e Event) { got <- e })

	s := spec(t, "leaky", growBy(t, 120))
	s.MaxMemory = "60MB"
	first, err := m.Start(s)
	if err != nil {
		t.Fatal(err)
	}
	overTheLimit(t, m, "leaky", 60<<20)
	m.mu.Lock()
	p := m.procs["leaky"]
	m.mu.Unlock()
	p.mu.Lock()
	p.startedAt = time.Now().Add(-time.Minute) // it has been running for a while
	p.crashes = 2                              // and had a couple of crashes some time ago
	p.mu.Unlock()
	m.checkMemory()

	select {
	case e := <-got:
		if e.Kind != EventMemory || e.Process != "leaky" || !strings.Contains(e.Detail, "the limit is 60 MB") {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a process above its limit was never restarted")
	}
	in, _ := m.Get("leaky")
	if in.PID == 0 || in.PID == first.PID {
		t.Errorf("it should be running again under a new pid: %d -> %d", first.PID, in.PID)
	}
	if in.Restarts < 1 {
		t.Errorf("a memory restart shows in the restarts column: %d", in.Restarts)
	}
	why := false
	for _, h := range in.History {
		if h.Kind == EventMemory && strings.Contains(h.Detail, "used ") {
			why = true
		}
	}
	if !why {
		t.Errorf("the history should say why: %+v", in.History)
	}
	p.mu.Lock()
	crashes := p.crashes
	p.mu.Unlock()
	if crashes != 0 {
		t.Errorf("a leak found after a long run is not a crash, and the restart is a fresh start: crashes = %d", crashes)
	}
}

// Over the limit right after starting means the limit is wrong for this app.
// It must not restart every few seconds for ever.
func TestMaxMemoryGivesUpWhenTheLimitIsExceededRightAfterStarting(t *testing.T) {
	slowSampler(t)
	m, _ := newTestManager(t)
	events := make(chan Event, 16)
	m.OnEvent(func(e Event) { events <- e })

	s := spec(t, "toolow", growBy(t, 120))
	s.MaxMemory = "60MB"
	s.MaxRestarts = 3
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		in, _ := m.Get("toolow")
		if in.Status == StatusErrored {
			break
		}
		overTheLimit(t, m, "toolow", 60<<20)
		m.checkMemory()
	}
	in, _ := m.Get("toolow")
	if in.Status != StatusErrored || in.PID != 0 {
		t.Fatalf("it should have given up: %+v", in)
	}
	if in.Restarts != 3 {
		t.Errorf("max_restarts 3 means three restarts, then giving up: restarts = %d", in.Restarts)
	}
	last := in.History[len(in.History)-1]
	if last.Kind != EventErrored || !strings.HasPrefix(last.Detail, "gave up after 3 restarts for using too much memory") || !strings.Contains(last.Detail, "Raise max_memory") {
		t.Errorf("the last event should say it gave up and what to do: %+v", last)
	}
	// Events are delivered in their own goroutines, so wait for the one that matters.
	deadline := time.After(5 * time.Second)
	for gaveUp := false; !gaveUp; {
		select {
		case e := <-events:
			gaveUp = e.Kind == EventErrored && strings.HasPrefix(e.Detail, "gave up")
		case <-deadline:
			t.Fatal("an alert event for giving up should have been sent")
		}
	}
	// And it stays down: the sampler leaves a stopped process alone.
	m.checkMemory()
	time.Sleep(300 * time.Millisecond)
	if in, _ := m.Get("toolow"); in.PID != 0 || in.Status != StatusErrored {
		t.Errorf("it came back: %+v", in)
	}
}

func TestMaxMemoryLeavesAProcessUnderTheLimitAlone(t *testing.T) {
	fastMemoryChecks(t)
	m, _ := newTestManager(t)
	got := make(chan Event, 2)
	m.OnEvent(func(e Event) { got <- e })
	s := spec(t, "fine", growBy(t, 20))
	s.MaxMemory = "500MB"
	first, err := m.Start(s)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		t.Fatalf("a process under its limit was restarted: %+v", e)
	case <-time.After(1500 * time.Millisecond):
	}
	if in, _ := m.Get("fine"); in.PID != first.PID {
		t.Errorf("pid changed from %d to %d", first.PID, in.PID)
	}
}

func TestMaxMemoryCountsChildren(t *testing.T) {
	fastMemoryChecks(t)
	m, _ := newTestManager(t)
	got := make(chan Event, 2)
	m.OnEvent(func(e Event) { got <- e })
	// The shell stays small; the memory is in the child it starts, like npm and node.
	s := spec(t, "wrapped", "("+growBy(t, 120)+") & wait")
	s.MaxMemory = "60MB"
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		if e.Kind != EventMemory {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("memory used by a child was not counted")
	}
}

func TestMaxMemoryValidation(t *testing.T) {
	s := spec(t, "x", "true")
	for _, ok := range []string{"", "0", "500MB", "1.5GB", "64M"} {
		s.MaxMemory = ok
		if err := s.Validate(); err != nil {
			t.Errorf("max_memory %q should be accepted: %v", ok, err)
		}
	}
	for _, bad := range []string{"lots", "-5MB", "MB"} {
		s.MaxMemory = bad
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "max_memory") {
			t.Errorf("max_memory %q should be rejected: %v", bad, err)
		}
	}
}

func TestGroupMemory(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30 & sleep 30; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() })
	time.Sleep(300 * time.Millisecond)
	got := groupMemory([]int{cmd.Process.Pid, os.Getpid() + 100000})
	if got[cmd.Process.Pid] <= 0 {
		t.Errorf("no memory counted for the group: %v", got)
	}
	if _, ok := got[os.Getpid()+100000]; ok {
		t.Errorf("a group that does not exist has memory: %v", got)
	}
}

func TestViewShowsTheMemoryLimit(t *testing.T) {
	in := Info{Name: "x", Spec: Spec{Name: "x", Command: "true", Cwd: "/", MaxMemory: "500MB"}}
	if got := NewView(in).MaxMemory; got != 500<<20 {
		t.Errorf("MaxMemory in the view = %d, want %d", got, 500<<20)
	}
	if got := NewView(Info{Name: "y", Spec: Spec{Name: "y", Command: "true", Cwd: "/"}}).MaxMemory; got != 0 {
		t.Errorf("no limit should be 0 in the view, got %d", got)
	}
}

// A process that keeps going over its limit is restarted again and again. When
// the manager is stopped, nothing may come back afterwards.
func TestStopAllLeavesNothingToTheMemorySampler(t *testing.T) {
	fastMemoryChecks(t)
	m, _ := newTestManager(t)
	got := make(chan Event, 16)
	m.OnEvent(func(e Event) { got <- e })
	s := spec(t, "loop", growBy(t, 120))
	s.MaxMemory = "60MB"
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(15 * time.Second):
		t.Fatal("no memory restart to interrupt")
	}
	m.StopAll()
	time.Sleep(600 * time.Millisecond) // several sampler ticks
	for _, in := range m.List() {
		if in.PID != 0 || in.Status == StatusOnline || in.Status == StatusWaiting {
			t.Errorf("%s is running again after StopAll: pid %d, %s", in.Name, in.PID, in.Status)
		}
	}
}
