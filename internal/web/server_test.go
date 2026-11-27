package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/itsahmadnyc/procly/internal/domains"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/itsahmadnyc/procly/internal/client"
	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/remote"
	"github.com/itsahmadnyc/procly/internal/sysinfo"
	"github.com/itsahmadnyc/procly/internal/tlsid"
)

// fakeBackend stands in for the daemon so the panel can be tested alone.
type fakeBackend struct {
	mu      sync.Mutex
	views   []proc.View
	listErr error
	actErr  error
	actions []string
	saved   int
	started []proc.Spec
}

func (f *fakeBackend) List(context.Context) ([]proc.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.views, f.listErr
}

func (f *fakeBackend) Action(_ context.Context, name, verb string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, verb+" "+name)
	return f.actErr
}

func (f *fakeBackend) Save(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved++
	return len(f.views), nil
}

func (f *fakeBackend) Logs(ctx context.Context, name string, send func([]logtail.Event) bool) error {
	if name != "api" {
		return &client.APIError{Status: http.StatusNotFound, Message: "process not found: " + name}
	}
	if !send([]logtail.Event{{Stream: "o", Line: "listening"}, {Stream: "e", Line: "<script>alert(1)</script>"}}) {
		return nil
	}
	send([]logtail.Event{{Stream: "o", Line: "GET / 200"}})
	return nil
}

func (f *fakeBackend) Scale(_ context.Context, name string, n int, relative bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, fmt.Sprintf("scale %s %d %v", name, n, relative))
	return nil
}
func (f *fakeBackend) Preflight(_ context.Context, name, mode string, workers int) ([]proc.Warning, error) {
	return []proc.Warning{{Kind: "startup", Text: "It syncs the database at start."}}, nil
}

func (f *fakeBackend) SetMode(_ context.Context, name, mode string, instances int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, fmt.Sprintf("mode %s %s %d", name, mode, instances))
	return nil
}

func (f *fakeBackend) Domains(context.Context, bool) (domains.Report, error) {
	return domains.Report{Processes: []domains.ProcessDomains{{Name: "api", Ports: []int{3000}, Domains: []domains.Domain{{Host: "api.example.com", Scheme: "https", Source: "nginx"}}}}}, nil
}

func (f *fakeBackend) System(context.Context) (sysinfo.System, error) {
	return sysinfo.System{Hostname: "prod-1", OS: "linux", CPUs: 4, MemTotal: 8 << 30}, nil
}

func (f *fakeBackend) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.actions...)
}

type panel struct {
	t        *testing.T
	url      string
	password string
	backend  *fakeBackend
	c        *http.Client
}

// newPanel serves the panel with a fake local backend and an empty PROCLY_HOME.
func newPanel(t *testing.T) *panel { return newPanelBehind(t) }

// newPanelBehind is newPanel with proxies trusted to say who the visitor is.
func newPanelBehind(t *testing.T, trusted ...string) *panel {
	t.Helper()
	proxies, err := ParseTrustedProxies(trusted)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("PROCLY_HOME", home)
	a, pw, err := loadAuth(filepath.Join(home, "web.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	fb := &fakeBackend{views: []proc.View{
		{ID: 0, Name: "api", Status: proc.StatusOnline, PID: 42, EnvKeys: []string{"DB_PASSWORD"}},
		{ID: 1, Name: "mailer", Status: proc.StatusErrored, EnvKeys: []string{}},
	}}
	s := &server{servers: newRegistry(fb), auth: a, host: "test-host", proxies: proxies}
	srv := httptest.NewServer(s.routes())
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &panel{t: t, url: srv.URL, password: pw, backend: fb, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}}
}

type resp struct {
	code   int
	body   string
	header http.Header
}

func (p *panel) req(method, path, body string, csrf bool) resp {
	p.t.Helper()
	return p.reqFrom(method, path, body, csrf, "")
}

// reqFrom is req with an X-Forwarded-For header, as a reverse proxy sends it.
func (p *panel) reqFrom(method, path, body string, csrf bool, forwardedFor string) resp {
	p.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, p.url+path, r)
	if csrf {
		req.Header.Set("X-Procly", "1")
	}
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	res, err := p.c.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, string(data), res.Header}
}

func (p *panel) login() {
	p.t.Helper()
	body, _ := json.Marshal(map[string]string{"password": p.password})
	if r := p.req(http.MethodPost, "/api/login", string(body), true); r.code != http.StatusOK {
		p.t.Fatalf("login: %d %s", r.code, r.body)
	}
}

func TestStaticPageAndSecurityHeaders(t *testing.T) {
	p := newPanel(t)
	r := p.req(http.MethodGet, "/", "", false)
	if r.code != http.StatusOK || !strings.Contains(r.body, "<title>procly</title>") {
		t.Fatalf("index: %d", r.code)
	}
	csp := r.header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP allows inline code: %s", csp)
	}
	for h, want := range map[string]string{"X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer"} {
		if got := r.header.Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	for _, asset := range []string{"/assets/app.js", "/assets/icons.js", "/assets/style.css", "/assets/favicon.svg"} {
		if r := p.req(http.MethodGet, asset, "", false); r.code != http.StatusOK {
			t.Errorf("%s: %d", asset, r.code)
		}
	}
}

func TestEverythingPrivateNeedsASession(t *testing.T) {
	p := newPanel(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/info"},
		{http.MethodGet, "/api/servers"},
		{http.MethodGet, "/api/servers/local/processes"},
		{http.MethodGet, "/api/servers/local/processes/api/logs"},
		{http.MethodPost, "/api/servers/local/processes/api/restart"},
		{http.MethodPost, "/api/servers/local/save"},
		{http.MethodGet, "/api/servers/local/system"},
		{http.MethodGet, "/api/servers/local/domains"},
	} {
		if r := p.req(c.method, c.path, "", true); r.code != http.StatusUnauthorized {
			t.Errorf("%s %s without session: %d", c.method, c.path, r.code)
		}
	}
	// A made-up session cookie is no better than none.
	req, _ := http.NewRequest(http.MethodGet, p.url+"/api/servers/local/processes", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: strings.Repeat("ab", 32)})
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Errorf("forged session cookie: %v %v", res.StatusCode, err)
	}
	if len(p.backend.recorded()) != 0 || p.backend.saved != 0 {
		t.Fatal("an unauthenticated request reached the backend")
	}
}

func TestLogin(t *testing.T) {
	p := newPanel(t)
	good, _ := json.Marshal(map[string]string{"password": p.password})

	if r := p.req(http.MethodPost, "/api/login", string(good), false); r.code != http.StatusForbidden {
		t.Fatalf("login without X-Procly: %d", r.code)
	}
	if r := p.req(http.MethodPost, "/api/login", `{"password":"wrong"}`, true); r.code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", r.code)
	}
	if r := p.req(http.MethodPost, "/api/login", `not json`, true); r.code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", r.code)
	}

	r := p.req(http.MethodPost, "/api/login", string(good), true)
	if r.code != http.StatusOK {
		t.Fatalf("right password: %d", r.code)
	}
	cookie := r.header.Get("Set-Cookie")
	for _, want := range []string{cookieName + "=", "HttpOnly", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(cookie, want) {
			t.Errorf("cookie missing %q: %s", want, cookie)
		}
	}
	if strings.Contains(cookie, "Secure") {
		t.Errorf("plain HTTP cookie marked Secure (browser would drop it): %s", cookie)
	}
	if r := p.req(http.MethodGet, "/api/info", "", false); r.code != http.StatusOK || !strings.Contains(r.body, "test-host") {
		t.Fatalf("info after login: %d %s", r.code, r.body)
	}

	u, _ := url.Parse(p.url)
	var session *http.Cookie
	for _, c := range p.c.Jar.Cookies(u) {
		if c.Name == cookieName {
			session = c
		}
	}
	if r := p.req(http.MethodPost, "/api/logout", "", true); r.code != http.StatusOK {
		t.Fatalf("logout: %d", r.code)
	}
	if r := p.req(http.MethodGet, "/api/info", "", false); r.code != http.StatusUnauthorized {
		t.Fatalf("session still works after logout: %d", r.code)
	}
	// The browser drops the cookie, but the server must also forget it,
	// in case a copy of it was taken.
	replay, _ := http.NewRequest(http.MethodGet, p.url+"/api/info", nil)
	replay.AddCookie(&http.Cookie{Name: cookieName, Value: session.Value})
	if res, err := http.DefaultClient.Do(replay); err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session cookie still accepted after logout: %v %v", res.StatusCode, err)
	}
}

func TestCookieIsSecureBehindHTTPSProxy(t *testing.T) {
	p := newPanel(t)
	body, _ := json.Marshal(map[string]string{"password": p.password})
	req, _ := http.NewRequest(http.MethodPost, p.url+"/api/login", strings.NewReader(string(body)))
	req.Header.Set("X-Procly", "1")
	req.Header.Set("X-Forwarded-Proto", "https")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if c := res.Header.Get("Set-Cookie"); !strings.Contains(c, "Secure") {
		t.Fatalf("cookie behind an HTTPS proxy should be Secure: %s", c)
	}
}

func TestLogoutCookieHasTheSameSecureFlagAsLogin(t *testing.T) {
	p := newPanel(t)
	logout := func(proto string) string {
		req, _ := http.NewRequest(http.MethodPost, p.url+"/api/logout", nil)
		req.Header.Set("X-Procly", "1")
		if proto != "" {
			req.Header.Set("X-Forwarded-Proto", proto)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.Header.Get("Set-Cookie")
	}
	behindProxy := logout("https")
	if !strings.Contains(behindProxy, "Secure") || !strings.Contains(behindProxy, "Max-Age=0") {
		t.Errorf("logout behind an HTTPS proxy should expire a Secure cookie: %s", behindProxy)
	}
	plain := logout("")
	if strings.Contains(plain, "Secure") || !strings.Contains(plain, "Max-Age=0") {
		t.Errorf("plain HTTP logout should expire a cookie that is not Secure (the login one wasn't): %s", plain)
	}
}

func TestLoginRateLimitOverHTTP(t *testing.T) {
	p := newPanel(t)
	for i := 0; i < maxFailures; i++ {
		if r := p.req(http.MethodPost, "/api/login", `{"password":"nope"}`, true); r.code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i+1, r.code)
		}
	}
	good, _ := json.Marshal(map[string]string{"password": p.password})
	r := p.req(http.MethodPost, "/api/login", string(good), true)
	if r.code != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
		t.Fatalf("even the right password must wait after %d failures: %d %v", maxFailures, r.code, r.header)
	}
}

func TestProcessesAndActions(t *testing.T) {
	p := newPanel(t)
	p.login()

	r := p.req(http.MethodGet, "/api/servers/local/processes", "", false)
	var views []proc.View
	if r.code != http.StatusOK || json.Unmarshal([]byte(r.body), &views) != nil || len(views) != 2 {
		t.Fatalf("processes: %d %s", r.code, r.body)
	}

	for _, verb := range []string{"restart", "reload", "stop", "start", "delete"} {
		if r := p.req(http.MethodPost, "/api/servers/local/processes/api/"+verb, "", true); r.code != http.StatusOK {
			t.Errorf("%s: %d %s", verb, r.code, r.body)
		}
	}
	if got := strings.Join(p.backend.recorded(), ","); got != "restart api,reload api,stop api,start api,delete api" {
		t.Fatalf("backend saw %q", got)
	}

	before := len(p.backend.recorded())
	for path, want := range map[string]int{
		"/api/servers/local/processes/api/restart": http.StatusForbidden, // no CSRF header, sent below
	} {
		if r := p.req(http.MethodPost, path, "", false); r.code != want {
			t.Errorf("without X-Procly: %d", r.code)
		}
	}
	for path, want := range map[string]int{
		"/api/servers/local/processes/all/stop":   http.StatusBadRequest,
		"/api/servers/local/processes/api/format": http.StatusNotFound,
		"/api/servers/nope/processes/api/stop":    http.StatusNotFound,
	} {
		if r := p.req(http.MethodPost, path, "", true); r.code != want {
			t.Errorf("%s: %d, want %d", path, r.code, want)
		}
	}
	if len(p.backend.recorded()) != before {
		t.Fatalf("rejected requests reached the backend: %v", p.backend.recorded()[before:])
	}

	if r := p.req(http.MethodPost, "/api/servers/local/save", "", true); r.code != http.StatusOK || !strings.Contains(r.body, `"saved":2`) {
		t.Fatalf("save: %d %s", r.code, r.body)
	}
	if r := p.req(http.MethodGet, "/api/servers/local/domains?refresh=1", "", true); r.code != http.StatusOK ||
		!strings.Contains(r.body, `"host":"api.example.com"`) || !strings.Contains(r.body, `"ports":[3000]`) {
		t.Fatalf("domains: %d %s", r.code, r.body)
	}
	if r := p.req(http.MethodGet, "/api/servers/nope/domains", "", true); r.code != http.StatusNotFound {
		t.Fatalf("domains of an unknown server: %d", r.code)
	}
	if r := p.req(http.MethodGet, "/api/servers/local/system", "", true); r.code != http.StatusOK || !strings.Contains(r.body, `"hostname":"prod-1"`) {
		t.Fatalf("system: %d %s", r.code, r.body)
	}
	if r := p.req(http.MethodGet, "/api/servers/nope/system", "", true); r.code != http.StatusNotFound {
		t.Fatalf("system for unknown server: %d", r.code)
	}
}

func TestBackendErrorsBecomeClearStatuses(t *testing.T) {
	p := newPanel(t)
	p.login()

	p.backend.listErr = errDaemonDown
	if r := p.req(http.MethodGet, "/api/servers/local/processes", "", false); r.code != http.StatusServiceUnavailable {
		t.Errorf("daemon down: %d", r.code)
	}
	p.backend.listErr = nil

	p.backend.actErr = &client.APIError{Status: http.StatusNotFound, Message: "process not found: ghost"}
	if r := p.req(http.MethodPost, "/api/servers/local/processes/ghost/stop", "", true); r.code != http.StatusNotFound || !strings.Contains(r.body, "ghost") {
		t.Errorf("unknown process: %d %s", r.code, r.body)
	}
}

func TestServerListIncludesRemoteServers(t *testing.T) {
	p := newPanel(t)
	p.login()
	// A tunnel server with no hub running here: listed, but unreachable.
	if err := remote.Add(remote.Server{Name: "office-1", Mode: remote.ModeTunnel, TokenHash: tlsid.TokenHash("t")}); err != nil {
		t.Fatal(err)
	}
	r := p.req(http.MethodGet, "/api/servers", "", false)
	var data struct {
		Servers []serverStatus `json:"servers"`
	}
	if r.code != http.StatusOK || json.Unmarshal([]byte(r.body), &data) != nil || len(data.Servers) != 2 {
		t.Fatalf("servers: %d %s", r.code, r.body)
	}
	local, office := data.Servers[0], data.Servers[1]
	if !local.Local || !local.OK || local.Label != "test-host" || local.Total != 2 || local.ByStatus["errored"] != 1 {
		t.Errorf("local: %+v", local)
	}
	if office.Name != "office-1" || office.OK || !strings.Contains(office.Error, "hub is not running") {
		t.Errorf("office-1: %+v", office)
	}
	if r := p.req(http.MethodGet, "/api/servers/office-1/processes", "", false); r.code != http.StatusBadGateway || !strings.Contains(r.body, "office-1:") {
		t.Errorf("unreachable remote: %d %s", r.code, r.body)
	}
}

func TestLogsStreamAsEvents(t *testing.T) {
	p := newPanel(t)
	p.login()
	r := p.req(http.MethodGet, "/api/servers/local/processes/api/logs", "", false)
	if r.code != http.StatusOK || r.header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("logs: %d %s", r.code, r.header.Get("Content-Type"))
	}
	events := strings.Count(r.body, "data: ")
	if events != 2 || !strings.Contains(r.body, `"l":"listening"`) || !strings.Contains(r.body, `"s":"e"`) {
		t.Fatalf("stream body: %q", r.body)
	}
	// Log lines are JSON-encoded data; the browser renders them as text.
	if strings.Contains(r.body, "<script>") {
		t.Fatalf("log line reached the stream unescaped: %q", r.body)
	}

	if r := p.req(http.MethodGet, "/api/servers/local/processes/ghost/logs", "", false); r.code != http.StatusNotFound {
		t.Fatalf("logs of unknown process: %d %s", r.code, r.body)
	}
}

func TestFriendlyErrors(t *testing.T) {
	cases := map[error]string{
		remote.ErrFingerprint:                                "fingerprint does not match",
		&remote.StatusError{Code: 401, Message: "bad token"}: "rejected the token",
		context.DeadlineExceeded:                             "timed out",
		remote.ErrHubDown:                                    "hub is not running",
	}
	for err, want := range cases {
		if got := friendly(err); !strings.Contains(got, want) {
			t.Errorf("friendly(%v) = %q, want it to mention %q", err, got, want)
		}
	}
	if got := friendly(errors.New("plain")); got != "plain" {
		t.Errorf("plain error changed: %q", got)
	}
}
