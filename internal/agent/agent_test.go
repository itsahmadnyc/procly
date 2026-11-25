package agent

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itsahmadnyc/procly/internal/hub"
	"github.com/itsahmadnyc/procly/internal/tlsid"
)

// fakeHub accepts one TLS connection, records the hello line and answers
// with reply(hello). It stands in for `procly hub` so the agent side can be
// tested on its own.
type fakeHub struct {
	addr  string
	fp    string
	hello chan hub.Hello
}

func startFakeHub(t *testing.T, reply func(hub.Hello) string) *fakeHub {
	t.Helper()
	certPEM, keyPEM, err := tlsid.NewCert("fake hub")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeHub{addr: ln.Addr().String(), fp: tlsid.Fingerprint(cert.Certificate[0]), hello: make(chan hub.Hello, 4)}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, err := readLine(conn)
				if err != nil {
					return
				}
				var h hub.Hello
				_ = json.Unmarshal(line, &h)
				f.hello <- h
				if r := reply(h); r != "" {
					_, _ = io.WriteString(conn, r)
				}
				time.Sleep(200 * time.Millisecond)
			}()
		}
	}()
	return f
}

func okReply(hub.Hello) string { return `{"ok":true}` + "\n" }

func cfgFor(f *fakeHub) JoinConfig {
	return JoinConfig{Hub: f.addr, Name: "office-1", Fingerprint: f.fp, Token: "secret-token"}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestDialHubSendsHelloAndAcceptsOK(t *testing.T) {
	f := startFakeHub(t, okReply)
	conn, err := dialHub(ctx(t), cfgFor(f), false)
	if err != nil {
		t.Fatalf("dialHub: %v", err)
	}
	conn.Close()
	h := <-f.hello
	if h.Name != "office-1" || h.Token != "secret-token" || h.Probe || h.Version == "" {
		t.Fatalf("hello: %+v", h)
	}
}

func TestDialHubReportsRejection(t *testing.T) {
	f := startFakeHub(t, func(hub.Hello) string { return `{"ok":false,"error":"unknown server or bad token"}` + "\n" })
	_, err := dialHub(ctx(t), cfgFor(f), false)
	var rej *ErrRejected
	if !errors.As(err, &rej) || rej.Reason != "unknown server or bad token" {
		t.Fatalf("want ErrRejected with the hub's reason, got %v", err)
	}
}

func TestDialHubRefusesWrongCertificateBeforeSendingToken(t *testing.T) {
	f := startFakeHub(t, okReply)
	cfg := cfgFor(f)
	cfg.Fingerprint = "sha256:" + strings.Repeat("ef", 32)
	_, err := dialHub(ctx(t), cfg, false)
	if !errors.Is(err, tlsid.ErrMismatch) {
		t.Fatalf("want ErrMismatch, got %v", err)
	}
	select {
	case h := <-f.hello:
		t.Fatalf("token sent to a hub with the wrong certificate: %+v", h)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestDialHubHandlesBadReplies(t *testing.T) {
	for name, reply := range map[string]string{
		"not json":     "HTTP/1.1 400 Bad Request\n",
		"closed early": "",
		"no newline":   `{"ok":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := startFakeHub(t, func(hub.Hello) string { return reply })
			conn, err := dialHub(ctx(t), cfgFor(f), false)
			if err == nil {
				conn.Close()
				t.Fatal("dialHub accepted a bad reply")
			}
		})
	}
}

func TestDialHubUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens here now
	_, err := dialHub(ctx(t), JoinConfig{Hub: addr, Name: "x", Fingerprint: "sha256:" + strings.Repeat("00", 32), Token: "t"}, false)
	if err == nil || !strings.Contains(err.Error(), "can't reach the hub") {
		t.Fatalf("got %v", err)
	}
}

func TestJoinProbesAndSavesOnlyOnSuccess(t *testing.T) {
	t.Setenv("PROCLY_HOME", t.TempDir())
	path := filepath.Join(os.Getenv("PROCLY_HOME"), "agent-connect.json")

	rejecting := startFakeHub(t, func(hub.Hello) string { return `{"ok":false,"error":"nope"}` + "\n" })
	if err := Join(cfgFor(rejecting)); err == nil {
		t.Fatal("Join succeeded against a hub that refused")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Join saved settings after a refusal")
	}

	f := startFakeHub(t, okReply)
	cfg := cfgFor(f)
	cfg.Fingerprint = strings.ToUpper(strings.TrimPrefix(f.fp, "sha256:")) // users may paste it any way
	if err := Join(cfg); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if h := <-f.hello; !h.Probe {
		t.Fatal("Join must probe, not open a tunnel")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("agent-connect.json mode %v, want 0600 (it holds the token)", fi.Mode().Perm())
	}
	saved, err := loadJoin()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Fingerprint != f.fp || saved.Token != "secret-token" || saved.Name != "office-1" {
		t.Fatalf("saved %+v", saved)
	}
}

func TestJoinValidatesInput(t *testing.T) {
	t.Setenv("PROCLY_HOME", t.TempDir())
	good := JoinConfig{Hub: "127.0.0.1:4749", Name: "office-1", Fingerprint: "sha256:" + strings.Repeat("ab", 32), Token: "t"}
	for name, mutate := range map[string]func(*JoinConfig){
		"bad fingerprint": func(c *JoinConfig) { c.Fingerprint = "abc" },
		"reserved name":   func(c *JoinConfig) { c.Name = "local" },
		"no port":         func(c *JoinConfig) { c.Hub = "hub.example.com" },
	} {
		c := good
		mutate(&c)
		if err := Join(c); err == nil {
			t.Errorf("%s: Join accepted it", name)
		}
	}
}

func TestLoadJoinExplainsMissingSettings(t *testing.T) {
	t.Setenv("PROCLY_HOME", t.TempDir())
	if _, err := loadJoin(); err == nil || !strings.Contains(err.Error(), "procly agent join") {
		t.Fatalf("got %v", err)
	}
}

func TestAuthenticate(t *testing.T) {
	sum := tlsid.TokenHash("right-token")
	hash, _ := hex.DecodeString(sum)
	a := &agent{tokenHash: hash, logger: log.New(io.Discard, "", 0)}
	h := a.authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))

	for header, want := range map[string]int{
		"":                    http.StatusUnauthorized,
		"right-token":         http.StatusUnauthorized, // missing "Bearer "
		"Bearer wrong":        http.StatusUnauthorized,
		"Bearer right-token ": http.StatusUnauthorized,
		"bearer right-token":  http.StatusUnauthorized,
		"Bearer right-token":  http.StatusTeapot,
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("Authorization %q: got %d, want %d", header, rec.Code, want)
		}
	}
}

func TestIdentityKeepsTokenHashOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.json")
	id, err := loadIdentity(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if id.newToken == "" {
		t.Fatal("first start should create a token")
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), id.newToken) {
		t.Fatal("agent.json contains the raw token")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("agent.json mode %v", fi.Mode().Perm())
	}

	again, err := loadIdentity(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if again.newToken != "" || again.fingerprint != id.fingerprint {
		t.Fatal("restart changed the token or certificate")
	}

	reset, err := loadIdentity(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if reset.newToken == "" || reset.newToken == id.newToken || reset.fingerprint != id.fingerprint {
		t.Fatal("--reset-token should change the token and keep the certificate")
	}
}
