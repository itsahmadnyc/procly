package alerts

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsahmadnyc/procly/internal/proc"
)

// inbox is a fake Telegram + Slack that records what it receives.
type inbox struct {
	mu       sync.Mutex
	telegram []map[string]any
	slack    []map[string]any
	paths    []string
	status   int
}

func newInbox(t *testing.T) (*inbox, *httptest.Server) {
	in := &inbox{status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		in.mu.Lock()
		defer in.mu.Unlock()
		in.paths = append(in.paths, r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/bot") {
			in.telegram = append(in.telegram, body)
		} else {
			in.slack = append(in.slack, body)
		}
		w.WriteHeader(in.status)
		if in.status >= 300 {
			_, _ = io.WriteString(w, `{"ok":false,"description":"Unauthorized"}`)
		}
	}))
	t.Cleanup(srv.Close)
	old := telegramAPI
	telegramAPI = srv.URL
	t.Cleanup(func() { telegramAPI = old })
	return in, srv
}

func (in *inbox) counts() (int, int) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return len(in.telegram), len(in.slack)
}

func TestSendToBoth(t *testing.T) {
	in, srv := newInbox(t)
	cfg := Config{
		Telegram: &Telegram{Token: "123:SECRET", ChatID: "-1001"},
		Slack:    &Slack{WebhookURL: srv.URL + "/services/T/B/X"},
	}
	if err := Send(context.Background(), cfg, "hello"); err != nil {
		t.Fatal(err)
	}
	if in.paths[0] != "/bot123:SECRET/sendMessage" || in.telegram[0]["chat_id"] != "-1001" || in.telegram[0]["text"] != "hello" {
		t.Fatalf("telegram request: %v %v", in.paths, in.telegram)
	}
	if in.slack[0]["text"] != "hello" {
		t.Fatalf("slack request: %v", in.slack)
	}
}

func TestSendErrorsDontLeakSecrets(t *testing.T) {
	in, _ := newInbox(t)
	in.status = http.StatusUnauthorized
	err := Send(context.Background(), Config{Telegram: &Telegram{Token: "123:SECRET", ChatID: "1"}}, "x")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("want an HTTP 401 error, got %v", err)
	}

	// A network failure: Go puts the whole URL in the error.
	telegramAPI = "http://127.0.0.1:1"
	err = Send(context.Background(), Config{
		Telegram: &Telegram{Token: "123:SECRET", ChatID: "1"},
		Slack:    &Slack{WebhookURL: "https://hooks.slack.com/services/T000/B000/XXXXSECRET"},
	}, "x")
	if err == nil {
		t.Fatal("expected errors")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error message leaks a secret: %v", err)
	}
}

func notifier(t *testing.T) (*Notifier, *time.Time) {
	t.Helper()
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	n := NewNotifier("prod-1", log.New(io.Discard, "", 0))
	n.now = func() time.Time { return clock }
	return n, &clock
}

func setup(t *testing.T, srv *httptest.Server) {
	t.Helper()
	t.Setenv("PROCLY_HOME", t.TempDir())
	if err := Save(Config{Slack: &Slack{WebhookURL: srv.URL + "/hook"}}); err != nil {
		t.Fatal(err)
	}
}

func TestNotifierThrottlesCrashLoops(t *testing.T) {
	in, srv := newInbox(t)
	setup(t, srv)
	n, clock := notifier(t)

	crash := proc.Event{Kind: proc.EventCrashed, Process: "api", ExitCode: 1, Retry: 2 * time.Second, Restarts: 1}
	for i := 0; i < 5; i++ {
		n.Handle(crash)
	}
	if _, s := in.counts(); s != 1 {
		t.Fatalf("a crash loop sent %d messages, want 1", s)
	}
	// Another process isn't throttled by api's crashes.
	n.Handle(proc.Event{Kind: proc.EventCrashed, Process: "web", ExitCode: 1})
	if _, s := in.counts(); s != 2 {
		t.Fatalf("other process: %d messages", s)
	}
	// Giving up is always sent.
	n.Handle(proc.Event{Kind: proc.EventErrored, Process: "api", ExitCode: 1})
	n.Handle(proc.Event{Kind: proc.EventErrored, Process: "api", ExitCode: 1})
	if _, s := in.counts(); s != 4 {
		t.Fatalf("errored events must not be throttled: %d messages", s)
	}
	// After the window, the next crash goes out and mentions the skipped ones.
	*clock = clock.Add(6 * time.Minute)
	n.Handle(crash)
	in.mu.Lock()
	last := in.slack[len(in.slack)-1]["text"].(string)
	in.mu.Unlock()
	if !strings.Contains(last, "4 more like this") {
		t.Fatalf("message after the window should count skipped alerts: %q", last)
	}
}

func TestNotifierQuietWithoutConfig(t *testing.T) {
	in, _ := newInbox(t)
	t.Setenv("PROCLY_HOME", t.TempDir())
	n, _ := notifier(t)
	n.Handle(proc.Event{Kind: proc.EventCrashed, Process: "api"})
	if tg, s := in.counts(); tg+s != 0 {
		t.Fatal("sent an alert with nothing configured")
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		e    proc.Event
		want []string
	}{
		{proc.Event{Kind: proc.EventCrashed, Process: "api", ExitCode: 2, Retry: 4 * time.Second, Restarts: 3, Detail: "Error: boom"},
			[]string{"api crashed on prod-1 (exit 2)", "Restarting in 4s", "restart #3", "Error: boom"}},
		{proc.Event{Kind: proc.EventErrored, Process: "api", ExitCode: 1},
			[]string{"api stopped on prod-1", "gave up", "procly logs api"}},
		{proc.Event{Kind: proc.EventUnhealthy, Process: "web", Detail: "3 failed checks"},
			[]string{"web failed its health check on prod-1", "restarted", "3 failed checks"}},
	}
	for _, c := range cases {
		got := Format("prod-1", c.e, 0)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s message is missing %q:\n%s", c.e.Kind, w, got)
			}
		}
	}
	long := Format("h", proc.Event{Kind: proc.EventCrashed, Process: "x", Detail: strings.Repeat("y", 5000)}, 0)
	if len(long) > 2000 {
		t.Errorf("message should be capped for chat apps, got %d chars", len(long))
	}
}

func TestConfigFileIsPrivate(t *testing.T) {
	t.Setenv("PROCLY_HOME", t.TempDir())
	if c, err := Load(); err != nil || !c.Empty() {
		t.Fatalf("no file should mean no alerts: %+v %v", c, err)
	}
	if err := Save(Config{Telegram: &Telegram{Token: "t", ChatID: "c"}}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("alerts.json holds a bot token and must be 0600: %v %v", fi.Mode().Perm(), err)
	}
	if c, _ := Load(); c.Telegram == nil || c.Telegram.Token != "t" {
		t.Fatalf("round trip: %+v", c)
	}
	if err := os.WriteFile(Path(), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("damaged file read as empty")
	}
}

func TestMask(t *testing.T) {
	if got := Mask("123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"); got != "1234…saw" {
		t.Errorf("Mask: %q", got)
	}
	if got := Mask("short"); strings.Contains(got, "short") {
		t.Errorf("short secrets must be fully hidden: %q", got)
	}
}
