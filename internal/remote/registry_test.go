package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/sse"
	"github.com/itsahmadnyc/procly/internal/tlsid"
)

func direct(name string) Server {
	return Server{Name: name, URL: "https://10.0.0.2:4748/", Token: "tok-" + name, Fingerprint: strings.Repeat("AB", 32)}
}

func TestRegistry(t *testing.T) {
	t.Setenv("PROCLY_HOME", t.TempDir())
	if list, err := Load(); err != nil || list != nil {
		t.Fatalf("no file yet should mean no servers: %v %v", list, err)
	}

	for _, n := range []string{"prod-2", "alpha", "zeta"} {
		if err := Add(direct(n)); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := Load()
	names := []string{}
	for _, s := range list {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "alpha,prod-2,zeta" {
		t.Fatalf("servers should come back sorted: %v", names)
	}
	s, ok, _ := Get("prod-2")
	if !ok || s.URL != "https://10.0.0.2:4748" || s.Fingerprint != "sha256:"+strings.Repeat("ab", 32) {
		t.Fatalf("Add should normalize URL and fingerprint: %+v", s)
	}

	// Adding the same name replaces it.
	changed := direct("prod-2")
	changed.URL = "https://10.0.0.9:4748"
	if err := Add(changed); err != nil {
		t.Fatal(err)
	}
	if list, _ := Load(); len(list) != 3 {
		t.Fatalf("replace created a duplicate: %d servers", len(list))
	}
	if s, _, _ := Get("prod-2"); s.URL != "https://10.0.0.9:4748" {
		t.Fatalf("not replaced: %s", s.URL)
	}

	if err := Remove("alpha"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("alpha"); err == nil {
		t.Fatal("removing a missing server should say so")
	}
	if _, ok, _ := Get("alpha"); ok {
		t.Fatal("removed server still there")
	}

	fi, err := os.Stat(filepath.Join(paths.Home(), "servers.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("servers.json holds tokens and must be 0600: %v %v", fi.Mode().Perm(), err)
	}
}

func TestRegistryRejectsBadServersAndDamagedFile(t *testing.T) {
	t.Setenv("PROCLY_HOME", t.TempDir())
	bad := direct("p")
	bad.Fingerprint = "nope"
	if err := Add(bad); err == nil {
		t.Fatal("bad fingerprint saved")
	}
	if err := Add(Server{Name: "t", Mode: ModeTunnel, TokenHash: "short"}); err == nil {
		t.Fatal("tunnel server without a full hash saved")
	}
	if err := Add(Server{Name: "t", Mode: ModeTunnel, TokenHash: tlsid.TokenHash("x")}); err != nil {
		t.Fatalf("valid tunnel server refused: %v", err)
	}

	if err := os.WriteFile(filepath.Join(paths.Home(), "servers.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("damaged servers.json read as empty; a later Add would wipe it")
	}
	if err := Add(direct("q")); err == nil {
		t.Fatal("Add over a damaged file must fail instead of overwriting it")
	}
}

func TestTunnelClientWithoutHub(t *testing.T) {
	t.Setenv("PROCLY_HOME", t.TempDir())
	c := NewClient(Server{Name: "office-1", Mode: ModeTunnel, TokenHash: tlsid.TokenHash("x")})
	if _, err := c.Ping(context.Background()); !errors.Is(err, ErrHubDown) {
		t.Fatalf("want ErrHubDown, got %v", err)
	}
}

// A tunnel client talks to the hub's unix socket, prefixes the server name
// and sends no token (the hub authenticated the agent already).
func TestTunnelClientGoesThroughHubSocket(t *testing.T) {
	t.Setenv("PROCLY_HOME", t.TempDir())
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", paths.HubSocket())
	if err != nil {
		t.Fatal(err)
	}
	var gotPath, gotAuth string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `[{"id":0,"name":"worker","status":"online","env_keys":[]}]`)
	})}
	go srv.Serve(ln)
	defer srv.Close()

	c := NewClient(Server{Name: "office-1", Mode: ModeTunnel, TokenHash: tlsid.TokenHash("x")})
	views, err := c.List(context.Background())
	if err != nil || len(views) != 1 || views[0].Name != "worker" {
		t.Fatalf("list: %+v %v", views, err)
	}
	if gotPath != "/servers/office-1/v1/processes" || gotAuth != "" {
		t.Fatalf("hub saw path %q auth %q", gotPath, gotAuth)
	}
}

func TestDirectClientLogsAndActions(t *testing.T) {
	var actions []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/logs"):
			send, _ := sse.Writer(w)
			send([]logtail.Event{{Stream: "o", Line: "hello"}})
			send(nil)
			send([]logtail.Event{{Stream: "e", Line: "oops"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/save":
			_, _ = io.WriteString(w, `{"saved":4}`)
		case r.Method == http.MethodPost:
			actions = append(actions, r.URL.Path)
			_, _ = io.WriteString(w, `{"ok":true}`)
		}
	}))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	c := NewClient(Server{Name: "p", URL: srv.URL, Token: "tok", Fingerprint: hex.EncodeToString(sum[:])})

	var lines []string
	keepalives := 0
	err := c.Logs(context.Background(), "api", func(b []logtail.Event) bool {
		if b == nil {
			keepalives++
		}
		for _, e := range b {
			lines = append(lines, e.Stream+":"+e.Line)
		}
		return true
	})
	if err != nil || strings.Join(lines, ",") != "o:hello,e:oops" || keepalives != 1 {
		t.Fatalf("logs: %v keepalives=%d err=%v", lines, keepalives, err)
	}

	if err := c.Action(context.Background(), "my app", "restart"); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0] != "/v1/processes/my app/restart" {
		t.Fatalf("action path: %v", actions)
	}
	if n, err := c.Save(context.Background()); err != nil || n != 4 {
		t.Fatalf("save: %d %v", n, err)
	}
}
