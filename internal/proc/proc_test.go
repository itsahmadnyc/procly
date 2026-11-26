package proc

import (
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	dir := t.TempDir()
	m := NewManager(dir, log.New(io.Discard, "", 0))
	t.Cleanup(func() { m.StopAll(); m.WaitIdle() }) // no late write may land in the temp folder
	return m, dir
}

func spec(t *testing.T, name, command string) Spec {
	return Spec{Name: name, Command: command, Cwd: t.TempDir()}
}

func off() *bool { b := false; return &b }

func waitStatus(t *testing.T, m *Manager, name string, want Status) Info {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var in Info
	for time.Now().Before(deadline) {
		var err error
		in, err = m.Get(name)
		if err == nil && in.Status == want {
			return in
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: wanted status %s, last saw %s", name, want, in.Status)
	return in
}

func TestStartListStop(t *testing.T) {
	m, _ := newTestManager(t)
	in, err := m.Start(spec(t, "sleeper", "sleep 30"))
	if err != nil {
		t.Fatal(err)
	}
	if in.Status != StatusOnline || in.PID == 0 {
		t.Fatalf("after start: %+v", in)
	}
	list := m.List()
	if len(list) != 1 || list[0].Memory == 0 {
		t.Fatalf("list should show the process with memory from ps: %+v", list)
	}
	if _, err := m.Stop("sleeper"); err != nil {
		t.Fatal(err)
	}
	in, _ = m.Get("sleeper")
	if in.Status != StatusStopped || in.PID != 0 {
		t.Fatalf("after stop: %+v", in)
	}
	if err := syscall.Kill(list[0].PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("process %d still exists after stop", list[0].PID)
	}
}

func TestStartingARunningProcessFails(t *testing.T) {
	m, _ := newTestManager(t)
	s := spec(t, "api", "sleep 30")
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(s); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second start: %v", err)
	}
}

func TestCrashLoopRestartsThenGivesUp(t *testing.T) {
	m, _ := newTestManager(t)
	s := spec(t, "crasher", "exit 1")
	s.RestartDelayMs = 20
	s.MaxRestarts = 3
	start := time.Now()
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	in := waitStatus(t, m, "crasher", StatusErrored)
	if in.Restarts != 3 || in.ExitCode != 1 {
		t.Fatalf("want 3 restarts and exit code 1, got %+v", in)
	}
	// Backoff doubles: 20 + 40 + 80 ms at least.
	if elapsed := time.Since(start); elapsed < 140*time.Millisecond {
		t.Fatalf("restarts happened too fast for exponential backoff: %s", elapsed)
	}
}

func TestNoAutorestart(t *testing.T) {
	m, _ := newTestManager(t)
	bad := spec(t, "bad", "exit 3")
	bad.AutoRestart = off()
	good := spec(t, "good", "exit 0")
	good.AutoRestart = off()
	for _, s := range []Spec{bad, good} {
		if _, err := m.Start(s); err != nil {
			t.Fatal(err)
		}
	}
	if in := waitStatus(t, m, "bad", StatusErrored); in.ExitCode != 3 || in.Restarts != 0 {
		t.Fatalf("bad: %+v", in)
	}
	if in := waitStatus(t, m, "good", StatusStopped); in.ExitCode != 0 || in.Restarts != 0 {
		t.Fatalf("good: %+v", in)
	}
}

func TestStopEscalatesToSIGKILL(t *testing.T) {
	m, _ := newTestManager(t)
	// Ignored signals are inherited, so both the shell and sleep ignore TERM.
	s := spec(t, "stubborn", "trap '' TERM; sleep 30")
	s.KillTimeoutMs = 300
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let the trap be installed
	start := time.Now()
	if _, err := m.Stop("stubborn"); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 300*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("stop took %s; expected the 300ms kill timeout, then SIGKILL", elapsed)
	}
	if in, _ := m.Get("stubborn"); in.Status != StatusStopped {
		t.Fatalf("status after SIGKILL: %s", in.Status)
	}
}

func TestStopKillsTheWholeProcessGroup(t *testing.T) {
	m, _ := newTestManager(t)
	in, err := m.Start(spec(t, "family", "sleep 300 & sleep 300; wait"))
	if err != nil {
		t.Fatal(err)
	}
	pgid := in.PID
	// Wait until the shell has started both children, or there is nothing to test.
	members := func() int {
		out, _ := exec.Command("pgrep", "-g", strconv.Itoa(pgid)).Output()
		return len(strings.Fields(string(out)))
	}
	deadline := time.Now().Add(3 * time.Second)
	for members() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := members(); n < 3 {
		t.Fatalf("expected the shell and two sleeps in group %d, found %d", pgid, n)
	}
	if _, err := m.Stop("family"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if aliveInGroup(pgid) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%d child processes outlived stop", aliveInGroup(pgid))
}

// aliveInGroup counts processes in a group that are not zombies. Killed
// children whose parent died are zombies until init reaps them, and in a
// container without an init that can take forever; they are dead either way.
func aliveInGroup(pgid int) int {
	out, _ := exec.Command("pgrep", "-g", strconv.Itoa(pgid)).Output()
	alive := 0
	for _, pid := range strings.Fields(string(out)) {
		stat, err := exec.Command("ps", "-o", "stat=", "-p", pid).Output()
		if err == nil && !strings.HasPrefix(strings.TrimSpace(string(stat)), "Z") {
			alive++
		}
	}
	return alive
}

func TestRestartAndDelete(t *testing.T) {
	m, _ := newTestManager(t)
	first, err := m.Start(spec(t, "web", "sleep 30"))
	if err != nil {
		t.Fatal(err)
	}
	infos, err := m.Restart("web")
	if err != nil {
		t.Fatal(err)
	}
	if infos[0].PID == first.PID || infos[0].Restarts != 1 || infos[0].Status != StatusOnline {
		t.Fatalf("after restart: %+v", infos[0])
	}
	// Restart also brings a stopped process back.
	_, _ = m.Stop("web")
	if infos, _ := m.Restart("web"); infos[0].Status != StatusOnline {
		t.Fatalf("restart of stopped process: %+v", infos[0])
	}
	if _, err := m.Delete("web"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get("web"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted process still found: %v", err)
	}
}

func TestTargetsByNameIDAndAll(t *testing.T) {
	m, _ := newTestManager(t)
	for _, n := range []string{"a", "b", "c"} {
		if _, err := m.Start(spec(t, n, "sleep 30")); err != nil {
			t.Fatal(err)
		}
	}
	if in, err := m.Get("1"); err != nil || in.Name != "b" {
		t.Fatalf("get by id: %+v %v", in, err)
	}
	infos, err := m.Stop("all")
	if err != nil || len(infos) != 3 {
		t.Fatalf("stop all: %d, %v", len(infos), err)
	}
	for _, in := range infos {
		if in.Status != StatusStopped {
			t.Fatalf("%s still %s", in.Name, in.Status)
		}
	}
	if _, err := m.Stop("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown target: %v", err)
	}
}

func TestLogsAndEnvironment(t *testing.T) {
	m, dir := newTestManager(t)
	s := spec(t, "talker", `echo "out $GREETING $PROCLY_NAME"; echo "err line" >&2; sleep 30`)
	s.Env = map[string]string{"GREETING": "hello"}
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			data, _ := os.ReadFile(filepath.Join(dir, name))
			if len(data) > 0 {
				return string(data)
			}
			time.Sleep(20 * time.Millisecond)
		}
		return ""
	}
	if out := read("talker-out.log"); out != "out hello talker\n" {
		t.Fatalf("stdout log: %q", out)
	}
	if errOut := read("talker-error.log"); errOut != "err line\n" {
		t.Fatalf("stderr log: %q", errOut)
	}
	fi, _ := os.Stat(filepath.Join(dir, "talker-out.log"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("log mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestCwdIsUsed(t *testing.T) {
	m, dir := newTestManager(t)
	s := spec(t, "where", "pwd; sleep 30")
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	out, _ := os.ReadFile(filepath.Join(dir, "where-out.log"))
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	want, _ := filepath.EvalSymlinks(s.Cwd)
	if got != want {
		t.Fatalf("ran in %q, want %q", got, want)
	}
}

func TestSaveAndResurrect(t *testing.T) {
	m, _ := newTestManager(t)
	dump := filepath.Join(t.TempDir(), "dump.json")
	s := spec(t, "keep", "sleep 30")
	s.Env = map[string]string{"A": "1"}
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	if n, err := m.Save(dump); err != nil || n != 1 {
		t.Fatalf("save: %d, %v", n, err)
	}
	if fi, _ := os.Stat(dump); fi.Mode().Perm() != 0o600 {
		t.Fatalf("dump mode %v, want 0600 (it holds env values)", fi.Mode().Perm())
	}

	fresh, _ := newTestManager(t)
	infos, err := fresh.Resurrect(dump)
	if err != nil || len(infos) != 1 || infos[0].Status != StatusOnline || infos[0].Spec.Env["A"] != "1" {
		t.Fatalf("resurrect: %+v, %v", infos, err)
	}
	// Running it again skips what is already there: no duplicates, no errors.
	if again, err := fresh.Resurrect(dump); len(again) != 0 || err != nil {
		t.Fatalf("second resurrect: started %d more, err %v", len(again), err)
	}
	if infos, err := fresh.Resurrect(filepath.Join(t.TempDir(), "missing.json")); err != nil || infos != nil {
		t.Fatalf("missing dump should be a no-op: %v", err)
	}
}

func TestSpecValidation(t *testing.T) {
	dir := t.TempDir()
	ok := Spec{Name: "api-1.v2_x", Command: "true", Cwd: dir}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]Spec{
		"reserved name":  {Name: "all", Command: "true", Cwd: dir},
		"slash in name":  {Name: "../x", Command: "true", Cwd: dir},
		"empty command":  {Name: "a", Command: "  ", Cwd: dir},
		"relative cwd":   {Name: "a", Command: "true", Cwd: "relative"},
		"missing cwd":    {Name: "a", Command: "true", Cwd: filepath.Join(dir, "nope")},
		"negative limit": {Name: "a", Command: "true", Cwd: dir, MaxRestarts: -1},
	} {
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestBackoff(t *testing.T) {
	base := 100 * time.Millisecond
	for n, want := range map[int]time.Duration{1: 100 * time.Millisecond, 2: 200 * time.Millisecond, 4: 800 * time.Millisecond, 20: maxBackoff} {
		if got := backoff(base, n); got != want {
			t.Errorf("backoff(%d) = %s, want %s", n, got, want)
		}
	}
}

func TestShellCommand(t *testing.T) {
	for in, want := range map[string]string{
		"node server.js":                  "exec node server.js",
		"  node   server.js":              "exec node   server.js",
		"PORT=3000 node server.js":        "PORT=3000 exec node server.js",
		"A=1 B=2 node -e 'x y'":           "A=1 B=2 exec node -e 'x y'",
		`FOO="a b" node x`:                `FOO="a b" node x`,
		"PATH=$PATH:/x node":              "PATH=$PATH:/x node",
		"PORT=3000":                       "PORT=3000",
		"exit 1":                          "exit 1",
		"cd /srv && node x":               "cd /srv && node x",
		"export A=1":                      "export A=1",
		"npm run build && node x":         "npm run build && node x",
		"a; b":                            "a; b",
		"a | b":                           "a | b",
		"(cd x && y)":                     "(cd x && y)",
		"python3 -m http.server --bind=0": "exec python3 -m http.server --bind=0",
	} {
		if got := shellCommand(in); got != want {
			t.Errorf("shellCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

// Leading VAR=value words must reach the program, and the tracked PID must
// be the program itself, not a wrapper shell. Kept free of ";" and quotes so
// it means the same in bash (macOS) and dash (Debian/Ubuntu).
func TestEnvPrefixReachesTheProgram(t *testing.T) {
	m, dir := newTestManager(t)
	s := spec(t, "printer", "GREETING=hello printenv GREETING")
	s.AutoRestart = off()
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	if in := waitStatus(t, m, "printer", StatusStopped); in.ExitCode != 0 {
		t.Fatalf("exit code %d (127 means exec was put before the variable)", in.ExitCode)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "printer-out.log"))
	if strings.TrimSpace(string(out)) != "hello" {
		t.Fatalf("variable did not reach the program: %q", out)
	}
}

func TestEnvPrefixIsExeced(t *testing.T) {
	m, _ := newTestManager(t)
	in, err := m.Start(spec(t, "sleeper", "GREETING=hello sleep 30"))
	if err != nil {
		t.Fatal(err)
	}
	// The shell needs a moment to exec into the program.
	var comm []byte
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		comm, _ = exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(in.PID)).Output()
		if strings.Contains(string(comm), "sleep") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("tracked pid runs %q, want the program (sleep), not a wrapper shell", strings.TrimSpace(string(comm)))
}

func TestViewHidesEnvValues(t *testing.T) {
	v := NewView(Info{Name: "api", PID: 1, StartedAt: time.Now().Add(-90 * time.Second),
		Spec: Spec{Env: map[string]string{"DB_PASSWORD": "hunter2", "PATH": "/bin", "A": "1"}}})
	if strings.Join(v.EnvKeys, ",") != "A,DB_PASSWORD" {
		t.Fatalf("env keys: %v (PATH should be left out, keys sorted)", v.EnvKeys)
	}
	if v.UptimeMs < 89_000 || v.UptimeMs > 100_000 {
		t.Fatalf("uptime %dms", v.UptimeMs)
	}
	if stopped := NewView(Info{Name: "x"}); stopped.UptimeMs != 0 || stopped.EnvKeys == nil {
		t.Fatalf("stopped view: %+v (env_keys must be [] not null)", stopped)
	}
}
