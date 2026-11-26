package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/remote"
	"github.com/itsahmadnyc/procly/internal/tlsid"
)

// fakeBackend (server_test.go) can start processes too, like localBackend.
func (f *fakeBackend) Start(_ context.Context, spec proc.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, spec)
	return nil
}

func (f *fakeBackend) startedSpecs() []proc.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proc.Spec(nil), f.started...)
}

func startBody(t *testing.T, fields map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(fields)
	return string(b)
}

func TestStartNewProcess(t *testing.T) {
	p := newPanel(t)
	p.login()
	t.Setenv("PATH", "/opt/node/bin:/usr/bin")
	cwd := t.TempDir()

	r := p.req(http.MethodPost, "/api/servers/local/processes", startBody(t, map[string]any{
		"name": " api ", "command": "node server.js", "cwd": cwd,
		"env": map[string]string{"PORT": "3000"}, "autorestart": false, "health": "http://127.0.0.1:3000/health",
	}), true)
	if r.code != http.StatusCreated || !strings.Contains(r.body, `"name":"api"`) {
		t.Fatalf("start: %d %s", r.code, r.body)
	}
	got := p.backend.startedSpecs()
	if len(got) != 1 {
		t.Fatalf("backend got %d starts", len(got))
	}
	s := got[0]
	if s.Name != "api" || s.Command != "node server.js" || s.Cwd != cwd || s.Env["PORT"] != "3000" ||
		s.ShouldAutoRestart() || s.Health != "http://127.0.0.1:3000/health" {
		t.Fatalf("spec: %+v", s)
	}
	if s.Env["PATH"] != "/opt/node/bin:/usr/bin" {
		t.Fatalf("the new process should get procly's PATH, like the CLI: %q", s.Env["PATH"])
	}
}

func TestStartIsProtected(t *testing.T) {
	p := newPanel(t)
	body := startBody(t, map[string]any{"name": "x", "command": "id", "cwd": "/"})
	if r := p.req(http.MethodPost, "/api/servers/local/processes", body, true); r.code != http.StatusUnauthorized {
		t.Fatalf("without a session: %d", r.code)
	}
	p.login()
	if r := p.req(http.MethodPost, "/api/servers/local/processes", body, false); r.code != http.StatusForbidden {
		t.Fatalf("without the CSRF header: %d", r.code)
	}
	if n := len(p.backend.startedSpecs()); n != 0 {
		t.Fatalf("a rejected request started %d processes", n)
	}
}

func TestStartValidatesBeforeStarting(t *testing.T) {
	p := newPanel(t)
	p.login()
	cwd := t.TempDir()
	cases := map[string]map[string]any{
		"no command":   {"name": "x", "command": " ", "cwd": cwd},
		"bad name":     {"name": "../x", "command": "id", "cwd": cwd},
		"reserved":     {"name": "all", "command": "id", "cwd": cwd},
		"relative cwd": {"name": "x", "command": "id", "cwd": "srv/app"},
		"missing cwd":  {"name": "x", "command": "id", "cwd": cwd + "/nope"},
		"bad health":   {"name": "x", "command": "id", "cwd": cwd, "health": "ftp://h:21"},
	}
	for name, fields := range cases {
		r := p.req(http.MethodPost, "/api/servers/local/processes", startBody(t, fields), true)
		if r.code != http.StatusBadRequest || !strings.Contains(r.body, `"error"`) {
			t.Errorf("%s: %d %s", name, r.code, r.body)
		}
	}
	if r := p.req(http.MethodPost, "/api/servers/local/processes", "{broken", true); r.code != http.StatusBadRequest {
		t.Errorf("bad JSON: %d", r.code)
	}
	huge := `{"name":"x","command":"id","cwd":"/","pad":"` + strings.Repeat("a", 100<<10) + `"}`
	if r := p.req(http.MethodPost, "/api/servers/local/processes", huge, true); r.code != http.StatusBadRequest {
		t.Errorf("100 KB body: %d, want 400 (limit 64 KB)", r.code)
	}
	if n := len(p.backend.startedSpecs()); n != 0 {
		t.Fatalf("invalid requests started %d processes", n)
	}
}

func TestStartRefusedForRemoteServers(t *testing.T) {
	p := newPanel(t)
	p.login()
	if err := remote.Add(remote.Server{Name: "office-1", Mode: remote.ModeTunnel, TokenHash: tlsid.TokenHash("t")}); err != nil {
		t.Fatal(err)
	}
	r := p.req(http.MethodPost, "/api/servers/office-1/processes", startBody(t, map[string]any{"name": "x", "command": "id", "cwd": "/"}), true)
	if r.code != http.StatusForbidden || !strings.Contains(r.body, "agents don't start new commands") {
		t.Fatalf("remote start: %d %s", r.code, r.body)
	}
}

func TestInfoIncludesHomeForTheForm(t *testing.T) {
	p := newPanel(t)
	p.login()
	r := p.req(http.MethodGet, "/api/info", "", false)
	var info map[string]string
	if json.Unmarshal([]byte(r.body), &info) != nil || info["home"] == "" {
		t.Fatalf("info: %s", r.body)
	}
}
