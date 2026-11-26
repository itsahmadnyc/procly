package proc

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- log rotation ----

func TestRotateFile(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "app-out.log")
	write := func(s string) {
		if err := os.WriteFile(log, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }

	write("small")
	if rotated, _ := RotateFile(log, 100, 3); rotated {
		t.Fatal("rotated a file under the limit")
	}
	for i, content := range []string{"first-" + strings.Repeat("a", 100), "second-" + strings.Repeat("b", 100), "third-" + strings.Repeat("c", 100), "fourth-" + strings.Repeat("d", 100)} {
		write(content)
		rotated, err := RotateFile(log, 100, 3)
		if err != nil || !rotated {
			t.Fatalf("round %d: rotated=%v err=%v", i, rotated, err)
		}
		if read(log) != "" {
			t.Fatalf("round %d: current log not emptied", i)
		}
	}
	// Newest in .1, oldest kept in .3, the very first one dropped.
	for suffix, prefix := range map[string]string{".1": "fourth-", ".2": "third-", ".3": "second-"} {
		if !strings.HasPrefix(read(log+suffix), prefix) {
			t.Errorf("%s holds %.10q, want %s…", suffix, read(log+suffix), prefix)
		}
	}
	if _, err := os.Stat(log + ".4"); !os.IsNotExist(err) {
		t.Error("kept more files than log_keep")
	}
	if fi, _ := os.Stat(log + ".1"); fi.Mode().Perm() != 0o600 {
		t.Errorf("rotated log mode %v, want 0600", fi.Mode().Perm())
	}

	write(strings.Repeat("x", 200))
	if rotated, _ := RotateFile(log, 0, 3); rotated {
		t.Error("max 0 must turn rotation off")
	}
	other := filepath.Join(dir, "nokeep.log")
	if err := os.WriteFile(other, []byte(strings.Repeat("y", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	if rotated, _ := RotateFile(other, 100, 0); !rotated || read(other) != "" {
		t.Error("keep 0 should just empty the log")
	}
	if _, err := os.Stat(other + ".1"); !os.IsNotExist(err) {
		t.Error("keep 0 must not keep old files")
	}
}

// A running process keeps writing to the same log after rotation, without
// being restarted.
func TestRotationWhileRunning(t *testing.T) {
	m, dir := newTestManager(t)
	s := spec(t, "chatty", "while true; do echo 0123456789; sleep 0.01; done")
	s.LogMaxSize = "200"
	keep := 2
	s.LogKeep = &keep
	in, err := m.Start(s)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "chatty-out.log")
	waitSize := func(min int64) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if fi, err := os.Stat(out); err == nil && fi.Size() > min {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("log never grew past %d bytes", min)
	}
	waitSize(250)
	m.RotateLogs()
	if fi, _ := os.Stat(out + ".1"); fi == nil || fi.Size() < 200 {
		t.Fatal("rotated file missing or too small")
	}
	waitSize(20) // still writing after the truncate
	if got, _ := m.Get("chatty"); got.PID != in.PID || got.Restarts != 0 {
		t.Fatalf("rotation restarted the process: %+v", got)
	}
	// With O_APPEND the writer continues at the new end, not at its old offset.
	if fi, _ := os.Stat(out); fi.Size() > 250 {
		t.Fatalf("log is %d bytes right after rotation; the writer kept its old offset (sparse file)", fi.Size())
	}
}

func TestParseSizeAndDuration(t *testing.T) {
	for in, want := range map[string]int64{"": defaultLogMaxSize, "0": 0, "512": 512, "10MB": 10 << 20, "10mb": 10 << 20, "1.5K": 1536, "2G": 2 << 30, " 3 MB ": 3 << 20} {
		if got, err := ParseSize(in); err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"ten", "-1MB", "MB"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) accepted", bad)
		}
	}
	for in, want := range map[string]time.Duration{"": 7 * time.Second, "10s": 10 * time.Second, "1m30s": 90 * time.Second, "5": 5 * time.Second, "0.5": 500 * time.Millisecond} {
		if got, err := parseDuration(in, 7*time.Second); err != nil || got != want {
			t.Errorf("parseDuration(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "0", "-5s", "0s"} {
		if _, err := parseDuration(bad, time.Second); err == nil {
			t.Errorf("parseDuration(%q) accepted", bad)
		}
	}
}

func TestSpecValidatesNewFields(t *testing.T) {
	dir := t.TempDir()
	neg := -1
	for name, s := range map[string]Spec{
		"bad size":         {LogMaxSize: "lots"},
		"negative keep":    {LogKeep: &neg},
		"health scheme":    {Health: "ftp://x:21"},
		"health no host":   {Health: "http://"},
		"bad interval":     {Health: "tcp://127.0.0.1:1", HealthInterval: "often"},
		"negative retries": {HealthRetries: -2},
	} {
		s.Name, s.Command, s.Cwd = "a", "true", dir
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := Spec{Name: "a", Command: "true", Cwd: dir, LogMaxSize: "5MB", Health: "https://example.com/health", HealthInterval: "30s", HealthRetries: 5}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
}

// ---- health checks ----

type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) add(e Event) { l.mu.Lock(); l.events = append(l.events, e); l.mu.Unlock() }
func (l *eventLog) kinds() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.events {
		out = append(out, e.Kind)
	}
	return out
}
func (l *eventLog) wait(t *testing.T, kind string) Event {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		for _, e := range l.events {
			if e.Kind == kind {
				l.mu.Unlock()
				return e
			}
		}
		l.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no %s event; got %v", kind, l.kinds())
	return Event{}
}

func fastHealth(s *Spec, target string) {
	s.Health, s.HealthGrace, s.HealthInterval, s.HealthTimeout, s.HealthRetries = target, "0.05", "0.05", "1", 2
}

func waitHealth(t *testing.T, m *Manager, name string, want Health) Info {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var in Info
	for time.Now().Before(deadline) {
		in, _ = m.Get(name)
		if in.Health == want {
			return in
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: wanted health %q, last saw %q (%s)", name, want, in.Health, in.HealthErr)
	return in
}

func TestHealthyThenUnhealthyRestarts(t *testing.T) {
	var failing atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	m, _ := newTestManager(t)
	events := &eventLog{}
	m.OnEvent(events.add)
	s := spec(t, "api", "sleep 30")
	fastHealth(&s, srv.URL+"/health")
	first, err := m.Start(s)
	if err != nil {
		t.Fatal(err)
	}
	if first.Health != HealthStarting {
		t.Fatalf("before the grace period ends health should be %q, got %q", HealthStarting, first.Health)
	}
	waitHealth(t, m, "api", HealthHealthy)

	failing.Store(true)
	e := events.wait(t, EventUnhealthy)
	if e.Process != "api" || !strings.Contains(e.Detail, "HTTP 500") {
		t.Fatalf("unhealthy event: %+v", e)
	}
	// It comes back as a new process and is checked again.
	failing.Store(false)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		in, _ := m.Get("api")
		if in.PID != 0 && in.PID != first.PID && in.Restarts >= 1 && in.Health == HealthHealthy {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	in, _ := m.Get("api")
	t.Fatalf("not restarted and healthy again: %+v", in)
}

func TestTCPHealthCheck(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	m, _ := newTestManager(t)
	s := spec(t, "redis", "sleep 30")
	fastHealth(&s, "tcp://"+ln.Addr().String())
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	waitHealth(t, m, "redis", HealthHealthy)

	addr := ln.Addr().String()
	ln.Close()
	in := waitHealth(t, m, "redis", HealthUnhealthy)
	if !strings.Contains(in.HealthErr, addr) {
		t.Fatalf("health error should name the address: %q", in.HealthErr)
	}
}

func TestNoHealthCheckMeansNoHealth(t *testing.T) {
	m, _ := newTestManager(t)
	in, err := m.Start(spec(t, "plain", "sleep 30"))
	if err != nil || in.Health != "" {
		t.Fatalf("health without a check: %q %v", in.Health, err)
	}
}

func TestStoppingDoesNotTriggerHealthRestart(t *testing.T) {
	m, _ := newTestManager(t)
	events := &eventLog{}
	m.OnEvent(events.add)
	s := spec(t, "api", "sleep 30")
	fastHealth(&s, "tcp://127.0.0.1:1") // nothing listens: always failing
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stop("api"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if in, _ := m.Get("api"); in.Status != StatusStopped {
		t.Fatalf("a stopped process was brought back by its health check: %s", in.Status)
	}
	for _, k := range events.kinds() {
		if k == EventUnhealthy {
			t.Fatal("unhealthy alert for a process that was stopped on purpose")
		}
	}
}

// ---- events ----

func TestCrashAndGiveUpEvents(t *testing.T) {
	m, _ := newTestManager(t)
	events := &eventLog{}
	m.OnEvent(events.add)
	s := spec(t, "crasher", "echo boom >&2; exit 3")
	s.RestartDelayMs, s.MaxRestarts = 10, 2
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	crash := events.wait(t, EventCrashed)
	if crash.ExitCode != 3 || crash.Retry <= 0 || !strings.Contains(crash.Detail, "boom") {
		t.Fatalf("crash event: %+v", crash)
	}
	gaveUp := events.wait(t, EventErrored)
	if !strings.Contains(gaveUp.Detail, "gave up") {
		t.Fatalf("errored event: %+v", gaveUp)
	}
}

func TestNoEventsForCleanExitsOrStops(t *testing.T) {
	m, _ := newTestManager(t)
	events := &eventLog{}
	m.OnEvent(events.add)
	done := spec(t, "oneshot", "exit 0")
	done.AutoRestart = off()
	if _, err := m.Start(done); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(spec(t, "svc", "sleep 30")); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, m, "oneshot", StatusStopped)
	if _, err := m.Stop("svc"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if k := events.kinds(); len(k) != 0 {
		t.Fatalf("clean exits and stops should be quiet, got %v", k)
	}
}

// Failures that aren't consecutive must not add up to a restart.
func TestFlakyButMostlyHealthyIsNotRestarted(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%2 == 0 { // every other check fails
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	m, _ := newTestManager(t)
	events := &eventLog{}
	m.OnEvent(events.add)
	s := spec(t, "flaky", "sleep 30")
	fastHealth(&s, srv.URL) // 2 failures in a row restart it
	first, err := m.Start(s)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(800 * time.Millisecond) // ~15 checks, half of them failing
	if n.Load() < 8 {
		t.Fatalf("only %d checks ran", n.Load())
	}
	in, _ := m.Get("flaky")
	if in.PID != first.PID || in.Restarts != 0 {
		t.Fatalf("restarted although failures were never consecutive: %+v", in)
	}
	for _, k := range events.kinds() {
		if k == EventUnhealthy {
			t.Fatal("unhealthy event for a process whose failures weren't consecutive")
		}
	}
}

// While a slow process is being stopped, its failing health check must not
// restart it.
func TestHealthCheckLeavesAStoppingProcessAlone(t *testing.T) {
	m, _ := newTestManager(t)
	events := &eventLog{}
	m.OnEvent(events.add)
	s := spec(t, "slowstop", "trap '' TERM; sleep 30")
	s.KillTimeoutMs = 1000
	fastHealth(&s, "tcp://127.0.0.1:1") // always failing
	s.HealthGrace = "0.3"
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // trap installed, still in the grace period
	if _, err := m.Stop("slowstop"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if in, _ := m.Get("slowstop"); in.Status != StatusStopped || in.PID != 0 {
		t.Fatalf("the health check revived a process that was being stopped: %+v", in)
	}
	for _, k := range events.kinds() {
		if k == EventUnhealthy {
			t.Fatal("unhealthy alert while stopping on purpose")
		}
	}
}
