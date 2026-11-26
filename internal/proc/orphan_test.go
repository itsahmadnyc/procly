package proc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func gone(pid int) bool {
	_, ok := identity(pid)
	return !ok
}

func TestIdentity(t *testing.T) {
	self, ok := identity(os.Getpid())
	if !ok || self == "" {
		t.Fatalf("no identity for this very process: %q %v", self, ok)
	}
	if again, _ := identity(os.Getpid()); again != self {
		t.Errorf("identity changed between two looks: %q then %q", self, again)
	}

	a := exec.Command("sleep", "30")
	b := exec.Command("sleep", "30")
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	// ps reports start times to the second on macOS, so let a second pass: the
	// identity has to tell apart processes started at different times, which is
	// all a reused pid ever is.
	time.Sleep(1100 * time.Millisecond)
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Process.Kill(); b.Process.Kill(); a.Wait(); b.Wait() })
	ia, oka := identity(a.Process.Pid)
	ib, okb := identity(b.Process.Pid)
	if !oka || !okb || ia == ib || ia == self {
		t.Errorf("different processes need different identities: %q %q %q", self, ia, ib)
	}

	a.Process.Kill()
	a.Wait()
	if id, ok := identity(a.Process.Pid); ok {
		t.Errorf("a process that exited still has an identity: %q", id)
	}
}

func TestRunningFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "running.json")
	if got := readRunning(path); got != nil {
		t.Fatalf("missing file read as %v", got)
	}
	in := []RunningEntry{{Name: "web", PID: 42, Ident: "x:1", Command: "node app.js", StartedAt: time.Now().Round(time.Second)}}
	if err := writeRunning(path, in); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the file must be private: %v", fi)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "env") {
		t.Errorf("the file must not carry environment values: %s", b)
	}
	got := readRunning(path)
	if len(got) != 1 || got[0].Name != "web" || got[0].PID != 42 || got[0].Ident != "x:1" {
		t.Fatalf("round trip: %+v", got)
	}
	os.WriteFile(path, []byte("{nope"), 0o600)
	if got := readRunning(path); got != nil {
		t.Errorf("a damaged file should read as empty, got %v", got)
	}
	writeRunning(path, nil)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an empty list should remove the file: %v", err)
	}
}

// startPair starts two long-running processes under a manager that keeps the
// running file, waits until both are in it, and returns the manager and file.
// The processes do not restart, so the "dead" daemon's leftovers stay put.
func startPair(t *testing.T) (*Manager, string, map[string]int) {
	t.Helper()
	a, dir := newTestManager(t)
	file := filepath.Join(dir, "running.json")
	a.TrackRunning(file)
	pids := map[string]int{}
	for _, name := range []string{"web", "api"} {
		s := spec(t, name, "sleep 30")
		s.AutoRestart = off()
		in, err := a.Start(s)
		if err != nil {
			t.Fatal(err)
		}
		pids[name] = in.PID
	}
	eventually(t, "both processes in the running file", func() bool { return len(LiveOrphans(file)) == 2 })
	return a, file, pids
}

// The daemon that started the processes is "dead" for the manager b: it is a
// new manager on the same files while the processes are still running.
func TestAnOrphanIsListedAndNotStartedTwice(t *testing.T) {
	_, file, pids := startPair(t)

	b, _ := newTestManager(t)
	b.TrackRunning(file)
	found := b.RecognizeOrphans()
	if len(found) != 2 {
		t.Fatalf("recognized %d orphans, want 2: %+v", len(found), found)
	}

	list := b.List()
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	for _, in := range list {
		if in.Status != StatusOrphaned || in.PID != pids[in.Name] || in.PID == 0 {
			t.Errorf("%s: status %s pid %d, want orphaned with pid %d", in.Name, in.Status, in.PID, pids[in.Name])
		}
		if in.Spec.ShouldAutoRestart() {
			t.Errorf("%s: an orphan is not restarted, the view must not say it is", in.Name)
		}
		if in.StartedAt.IsZero() {
			t.Errorf("%s: no start time, so no uptime", in.Name)
		}
		if len(in.History) == 0 || in.History[len(in.History)-1].Kind != HistoryOrphaned {
			t.Errorf("%s: history should say it was found orphaned: %+v", in.Name, in.History)
		}
	}

	// No second copy, by start, by resurrect, and no restart.
	if _, err := b.Start(spec(t, "web", "sleep 30")); !errors.Is(err, ErrOrphaned) || !strings.Contains(err.Error(), "procly stop web") {
		t.Fatalf("start over an orphan: %v", err)
	}
	if _, err := b.Restart("web"); !errors.Is(err, ErrOrphaned) {
		t.Fatalf("restart of an orphan: %v", err)
	}
	dump := filepath.Join(t.TempDir(), "dump.json")
	os.WriteFile(dump, []byte(`[{"name":"web","command":"sleep 30","cwd":"`+t.TempDir()+`"},{"name":"fresh","command":"sleep 30","cwd":"`+t.TempDir()+`"}]`), 0o600)
	infos, err := b.Resurrect(dump)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].Name != "fresh" {
		t.Fatalf("resurrect should skip web and start only fresh, started %+v", infos)
	}
	if b.List()[0].PID != pids["web"] {
		t.Errorf("the orphan was replaced: %+v", b.List()[0])
	}

	// Saving must not write the orphans' sketchy specs into the saved list.
	saved := filepath.Join(t.TempDir(), "saved.json")
	n, err := b.Save(saved)
	if err != nil || n != 1 {
		t.Fatalf("save wrote %d specs (%v), want only the one real process", n, err)
	}
	if data, _ := os.ReadFile(saved); strings.Contains(string(data), `"api"`) || strings.Contains(string(data), `"web"`) {
		t.Errorf("the saved list holds an orphan: %s", data)
	}
}

func TestStoppingAnOrphan(t *testing.T) {
	_, file, pids := startPair(t)
	b, _ := newTestManager(t)
	b.TrackRunning(file)
	b.RecognizeOrphans()

	infos, err := b.Stop("web")
	if err != nil || len(infos) != 1 || infos[0].Status != StatusStopped {
		t.Fatalf("stop web: %+v %v", infos, err)
	}
	eventually(t, "the web process to be gone", func() bool { return gone(pids["web"]) })
	if !gone(pids["web"]) {
		t.Fatal("web still running")
	}
	if gone(pids["api"]) {
		t.Fatal("stopping web also stopped api")
	}
	for _, in := range b.List() {
		if in.Name == "web" {
			t.Errorf("a stopped orphan must be forgotten, not kept as a stopped process to start from a sketch: %+v", in)
		}
	}
	// With the orphan gone the name is free again.
	if _, err := b.Start(spec(t, "web", "sleep 30")); err != nil {
		t.Fatalf("start after stopping the orphan: %v", err)
	}

	// "all" and delete reach the other orphan too.
	if _, err := b.Delete("api"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "api to be gone", func() bool { return gone(pids["api"]) })
}

func TestStopAllReachesOrphans(t *testing.T) {
	_, file, pids := startPair(t)
	b, _ := newTestManager(t)
	b.TrackRunning(file)
	b.RecognizeOrphans()
	b.StopAll()
	for name, pid := range pids {
		eventually(t, name+" to be gone", func() bool { return gone(pid) })
	}
	b.FlushRunning()
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("nothing is running, so the file should be gone: %v", err)
	}
}

func TestAnOrphanThatExitsDisappears(t *testing.T) {
	a, file, pids := startPair(t)
	b, _ := newTestManager(t)
	b.TrackRunning(file)
	b.RecognizeOrphans()

	// Kill it from outside, the way a crash or an admin would.
	a.mu.Lock()
	cmd := a.procs["api"].cmd
	a.mu.Unlock()
	cmd.Process.Kill()
	eventually(t, "api to be gone", func() bool { return gone(pids["api"]) })

	names := ""
	for _, in := range b.List() {
		names += in.Name + " "
	}
	if names != "web " {
		t.Errorf("list = %q, want only web: api exited by itself", names)
	}
}

// A pid that has been reused by an unrelated process must never be signalled.
func TestAReusedPidIsLeftAlone(t *testing.T) {
	stranger := exec.Command("sleep", "30")
	if err := stranger.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stranger.Process.Kill(); stranger.Wait() })

	file := filepath.Join(t.TempDir(), "running.json")
	// An entry for that pid, with the identity of some other, long gone process.
	writeRunning(file, []RunningEntry{{Name: "ghost", PID: stranger.Process.Pid, Ident: "not-this-process", Command: "x", StartedAt: time.Now()}})
	if live := LiveOrphans(file); len(live) != 0 {
		t.Fatalf("an entry whose identity does not match is not an orphan: %+v", live)
	}

	b, _ := newTestManager(t)
	b.TrackRunning(file)
	if found := b.RecognizeOrphans(); len(found) != 0 {
		t.Fatalf("recognized %+v", found)
	}
	// Even if an orphan with a stale identity were stopped, the stranger lives.
	p := newOrphan(1, RunningEntry{Name: "ghost", PID: stranger.Process.Pid, Ident: "not-this-process", Command: "x", StartedAt: time.Now()}, b.logger)
	p.stopOrphan()
	time.Sleep(200 * time.Millisecond)
	if gone(stranger.Process.Pid) {
		t.Fatal("stopOrphan signalled a process it did not start")
	}
}

func TestTheRunningFileTracksStartsAndExits(t *testing.T) {
	m, dir := newTestManager(t)
	file := filepath.Join(dir, "running.json")
	m.TrackRunning(file)
	s := spec(t, "one", "sleep 30")
	s.AutoRestart = off()
	in, err := m.Start(s)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the entry to appear", func() bool {
		l := readRunning(file)
		return len(l) == 1 && l[0].Name == "one" && l[0].PID == in.PID && l[0].Ident != ""
	})
	if _, err := m.Stop("one"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the file to go away", func() bool { _, err := os.Stat(file); return errors.Is(err, os.ErrNotExist) })
}
