package proc

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseSignal(t *testing.T) {
	for in, want := range map[string]syscall.Signal{
		"SIGHUP": syscall.SIGHUP, "HUP": syscall.SIGHUP, "hup": syscall.SIGHUP, " sigterm ": syscall.SIGTERM,
		"INT": syscall.SIGINT, "SIGUSR1": syscall.SIGUSR1, "usr2": syscall.SIGUSR2, "QUIT": syscall.SIGQUIT, "KILL": syscall.SIGKILL,
	} {
		if got, err := ParseSignal(in); err != nil || got != want {
			t.Errorf("ParseSignal(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "SIGNOPE", "9", "STOP", "SIGSTOP"} {
		if _, err := ParseSignal(bad); err == nil || !strings.Contains(err.Error(), "SIGHUP") {
			t.Errorf("ParseSignal(%q) should fail and list the signals: %v", bad, err)
		}
	}
}

func TestSignalValidation(t *testing.T) {
	s := spec(t, "x", "true")
	s.StopSignal, s.ReloadSignal = "SIGINT", "hup"
	if err := s.Validate(); err != nil {
		t.Errorf("valid signals: %v", err)
	}
	for field, bad := range map[string]string{"stop_signal": "SIGNOPE", "reload_signal": "SIGNOPE"} {
		s := spec(t, "x", "true")
		if field == "stop_signal" {
			s.StopSignal = bad
		} else {
			s.ReloadSignal = bad
		}
		if err := s.Validate(); err == nil || !strings.Contains(err.Error(), field) {
			t.Errorf("%s %q should be refused, naming the setting: %v", field, bad, err)
		}
	}
	s = spec(t, "x", "true")
	s.ReloadSignal = "SIGKILL"
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "reload_signal") {
		t.Errorf("SIGKILL cannot be a reload signal: %v", err)
	}
	s = spec(t, "x", "true")
	s.StopSignal = "SIGKILL"
	if err := s.Validate(); err != nil {
		t.Errorf("SIGKILL is a fine stop signal: %v", err)
	}
}

// trapper counts the HUP signals it gets in file, and also records TERM and INT.
func trapper(file string) string {
	return `trap 'echo hup >> ` + file + `' HUP; trap 'echo term >> ` + file + `; exit 0' TERM; trap 'echo int >> ` + file + `; exit 0' INT; while true; do sleep 0.1; done`
}

func lines(path string) []string {
	b, _ := os.ReadFile(path)
	return strings.Fields(string(b))
}

func TestReloadSendsTheSignalWithoutRestarting(t *testing.T) {
	m, dir := newTestManager(t)
	log := filepath.Join(dir, "signals.txt")
	s := spec(t, "web", trapper(log))
	s.ReloadSignal = "SIGHUP"
	first, err := m.Start(s)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // let the shell install its traps

	for i := 1; i <= 2; i++ {
		infos, err := m.Reload("web")
		if err != nil || len(infos) != 1 {
			t.Fatalf("reload: %v %v", infos, err)
		}
		n := i
		eventually(t, "the process to see the signal", func() bool { return len(lines(log)) >= n })
	}
	in, _ := m.Get("web")
	if in.PID != first.PID || in.Restarts != 0 {
		t.Errorf("a reload is not a restart: pid %d -> %d, restarts %d", first.PID, in.PID, in.Restarts)
	}
	m.mu.Lock()
	crashes := m.procs["web"].crashes
	m.mu.Unlock()
	if crashes != 0 {
		t.Errorf("crash counter touched: %d", crashes)
	}
	reloads := 0
	for _, h := range in.History {
		if h.Kind == HistoryReloaded && strings.Contains(h.Detail, "SIGHUP") {
			reloads++
		}
	}
	if reloads != 2 {
		t.Errorf("the history should record both reloads: %+v", in.History)
	}
	if got := lines(log); got[0] != "hup" || got[1] != "hup" {
		t.Errorf("the process saw %v", got)
	}
}

func TestReloadNeedsASignalAndARunningProcess(t *testing.T) {
	m, _ := newTestManager(t)
	if _, err := m.Start(spec(t, "plain", "sleep 30")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Reload("plain"); err == nil || !strings.Contains(err.Error(), "no reload_signal") || !strings.Contains(err.Error(), "plain") {
		t.Errorf("a process without a reload_signal: %v", err)
	}

	s := spec(t, "down", "sleep 30")
	s.ReloadSignal = "SIGHUP"
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	m.Stop("down")
	if _, err := m.Reload("down"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("a stopped process: %v", err)
	}
	if _, err := m.Reload("nope"); err == nil {
		t.Error("an unknown process")
	}
}

func TestReloadAllReachesOnlyTheOnesThatCan(t *testing.T) {
	m, dir := newTestManager(t)
	log := filepath.Join(dir, "all.txt")
	a := spec(t, "can", trapper(log))
	a.ReloadSignal = "HUP"
	if _, err := m.Start(a); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(spec(t, "cannot", "sleep 30")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	infos, err := m.Reload("all")
	if err != nil || len(infos) != 1 || infos[0].Name != "can" {
		t.Fatalf("reload all = %v, %v", infos, err)
	}
	eventually(t, "the signal", func() bool { return len(lines(log)) == 1 })

	m2, _ := newTestManager(t)
	m2.Start(spec(t, "only-plain", "sleep 30"))
	if _, err := m2.Reload("all"); err == nil || !strings.Contains(err.Error(), "no running process has a reload_signal") {
		t.Errorf("reload all with nothing to reload: %v", err)
	}
}

func TestStopUsesTheConfiguredSignal(t *testing.T) {
	for _, c := range []struct{ signal, want string }{{"", "term"}, {"SIGINT", "int"}} {
		m, dir := newTestManager(t)
		log := filepath.Join(dir, "stop.txt")
		s := spec(t, "svc", trapper(log))
		s.StopSignal = c.signal
		s.AutoRestart = off()
		if _, err := m.Start(s); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
		if _, err := m.Stop("svc"); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the stop signal", func() bool { return len(lines(log)) > 0 })
		if got := lines(log); got[0] != c.want {
			t.Errorf("stop_signal %q: the process got %v, want %s first", c.signal, got, c.want)
		}
	}
}

func TestSignalNameIsCanonical(t *testing.T) {
	for in, want := range map[string]string{"": "", "hup": "SIGHUP", "SIGUSR1": "SIGUSR1", " usr2 ": "SIGUSR2", "bogus": "bogus"} {
		if got := SignalName(in); got != want {
			t.Errorf("SignalName(%q) = %q, want %q", in, got, want)
		}
	}
}
