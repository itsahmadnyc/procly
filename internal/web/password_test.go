package web

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (p *panel) change(current, next string, csrf bool) resp {
	p.t.Helper()
	body, _ := json.Marshal(map[string]string{"current": current, "next": next})
	return p.req(http.MethodPost, "/api/password", string(body), csrf)
}

func TestChangePassword(t *testing.T) {
	bcryptCost = 4
	p := newPanel(t)
	if r := p.change(p.password, "a-better-password", true); r.code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", r.code)
	}
	p.login()

	// a second browser, signed in too
	jar, _ := cookiejar.New(nil)
	other := &panel{t: t, url: p.url, password: p.password, backend: p.backend, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
	other.login()
	if r := other.req(http.MethodGet, "/api/info", "", false); r.code != http.StatusOK {
		t.Fatalf("the second session works: %d", r.code)
	}

	if r := p.change(p.password, "a-better-password", false); r.code != http.StatusForbidden {
		t.Fatalf("without the X-Procly header: %d", r.code)
	}
	for next, want := range map[string]string{"short": "at least 10", strings.Repeat("x", 73): "at most 72", p.password: "same as the current"} {
		if r := p.change(p.password, next, true); r.code != http.StatusBadRequest || !strings.Contains(r.body, want) {
			t.Errorf("new password %.12q: %d %s", next, r.code, r.body)
		}
	}
	if r := p.change("not the password", "a-better-password", true); r.code != http.StatusUnauthorized || !strings.Contains(r.body, "current password is wrong") {
		t.Fatalf("wrong current password: %d %s", r.code, r.body)
	}

	r := p.change(p.password, "a-better-password", true)
	if r.code != http.StatusOK || !strings.Contains(r.body, `"signed_out":1`) {
		t.Fatalf("change: %d %s", r.code, r.body)
	}
	if r := p.req(http.MethodGet, "/api/info", "", false); r.code != http.StatusOK {
		t.Fatalf("the session that changed it stays signed in: %d", r.code)
	}
	if r := other.req(http.MethodGet, "/api/info", "", false); r.code != http.StatusUnauthorized {
		t.Fatalf("every other session is signed out: %d", r.code)
	}

	old, _ := json.Marshal(map[string]string{"password": p.password})
	fresh, _ := json.Marshal(map[string]string{"password": "a-better-password"})
	if r := other.req(http.MethodPost, "/api/login", string(old), true); r.code != http.StatusUnauthorized {
		t.Fatalf("the old password no longer works: %d", r.code)
	}
	if r := other.req(http.MethodPost, "/api/login", string(fresh), true); r.code != http.StatusOK {
		t.Fatalf("the new one does: %d", r.code)
	}
}

func TestChangedPasswordSurvivesARestartAndGuessesAreLimited(t *testing.T) {
	bcryptCost = 4
	home := t.TempDir()
	path := filepath.Join(home, "web.json")
	a, pw, err := loadAuth(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.changePassword(pw, "a-better-password", ""); err != nil {
		t.Fatal(err)
	}
	again, shown, err := loadAuth(path, false)
	if err != nil || shown != "" {
		t.Fatalf("reload: %v %q", err, shown)
	}
	if !again.checkPassword("a-better-password") || again.checkPassword(pw) {
		t.Fatal("the file holds the new password's hash, not the old one's")
	}

	// wrong "current" guesses count against the login limit
	p := newPanel(t)
	p.login()
	for i := 0; i < maxFailures; i++ {
		if r := p.change("wrong-guess-"+string(rune('a'+i)), "a-better-password", true); r.code != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d", i, r.code)
		}
	}
	if r := p.change(p.password, "a-better-password", true); r.code != http.StatusTooManyRequests {
		t.Fatalf("after %d wrong guesses even the right one waits: %d", maxFailures, r.code)
	}
}

func TestModeRoute(t *testing.T) {
	p := newPanel(t)
	post := func(path, body string, csrf bool) resp { return p.req(http.MethodPost, path, body, csrf) }
	if r := post("/api/servers/local/processes/api/mode", `{"mode":"cluster","instances":2}`, true); r.code != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", r.code)
	}
	p.login()
	before := len(p.backend.recorded())
	if r := post("/api/servers/local/processes/api/mode", `{"mode":"cluster","instances":2}`, false); r.code != http.StatusForbidden {
		t.Fatalf("without the X-Procly header: %d", r.code)
	}
	for _, bad := range []string{`{"mode":"turbo"}`, `{"mode":"cluster","instances":-1}`, `{"mode":"cluster","instances":9999}`, `nope`, ``} {
		if r := post("/api/servers/local/processes/api/mode", bad, true); r.code != http.StatusBadRequest {
			t.Errorf("%q: %d", bad, r.code)
		}
	}
	if len(p.backend.recorded()) != before {
		t.Fatal("refused requests must not reach the backend")
	}
	if r := post("/api/servers/local/processes/api/mode", `{"mode":"cluster","instances":4}`, true); r.code != http.StatusOK {
		t.Fatalf("mode: %d %s", r.code, r.body)
	}
	if got := p.backend.recorded(); got[len(got)-1] != "mode api cluster 4" {
		t.Fatalf("the backend was told: %v", got)
	}
	if r := post("/api/servers/nope/processes/api/mode", `{"mode":"fork"}`, true); r.code != http.StatusNotFound {
		t.Fatalf("an unknown server: %d", r.code)
	}
}

func TestScaleAndPreflightRoutes(t *testing.T) {
	p := newPanel(t)
	p.login()
	if r := p.req(http.MethodPost, "/api/servers/local/processes/web/scale", `{"workers":3}`, false); r.code != http.StatusForbidden {
		t.Fatalf("without the X-Procly header: %d", r.code)
	}
	for _, bad := range []string{`{"workers":0}`, `{"workers":9999}`, `nope`} {
		if r := p.req(http.MethodPost, "/api/servers/local/processes/web/scale", bad, true); r.code != http.StatusBadRequest {
			t.Errorf("%q: %d", bad, r.code)
		}
	}
	if r := p.req(http.MethodPost, "/api/servers/local/processes/web/scale", `{"workers":2,"relative":true}`, true); r.code != http.StatusOK {
		t.Fatalf("scale: %d %s", r.code, r.body)
	}
	if got := p.backend.recorded(); got[len(got)-1] != "scale web 2 true" {
		t.Fatalf("the backend was told: %v", got)
	}
	r := p.req(http.MethodGet, "/api/servers/local/processes/web/preflight?mode=cluster&workers=2", "", false)
	if r.code != http.StatusOK || !strings.Contains(r.body, "syncs the database") {
		t.Fatalf("preflight: %d %s", r.code, r.body)
	}
}
