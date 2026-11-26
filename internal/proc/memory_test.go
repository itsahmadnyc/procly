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

func TestMaxMemoryRestartsAProcessThatGrows(t *testing.T) {
	fastMemoryChecks(t)
	m, _ := newTestManager(t)
	got := make(chan Event, 4)
	m.OnEvent(func(e Event) { got <- e })

	s := spec(t, "leaky", growBy(t, 120))
	s.MaxMemory = "60MB"
	first, err := m.Start(s)
	if err != nil {
		t.Fatal(err)
	}

	var e Event
	select {
	case e = <-got:
	case <-time.After(15 * time.Second):
		t.Fatal("a process above its limit was never restarted")
	}
	if e.Kind != EventMemory || e.Process != "leaky" || !strings.Contains(e.Detail, "the limit is 60 MB") {
		t.Errorf("event = %+v", e)
	}

	// It was restarted (new pid), the history says why, and it did not count as a crash.
	eventually(t, "a new pid", func() bool { in, _ := m.Get("leaky"); return in.PID > 0 && in.PID != first.PID })
	in, _ := m.Get("leaky")
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
	m.mu.Lock()
	crashes := m.procs["leaky"].crashes
	m.mu.Unlock()
	if crashes != 0 {
		t.Errorf("a memory restart must not count toward max_restarts, crashes = %d", crashes)
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
