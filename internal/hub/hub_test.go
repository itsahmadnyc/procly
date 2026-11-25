package hub

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/itsahmadnyc/procly/internal/remote"
	"github.com/itsahmadnyc/procly/internal/tlsid"
)

// testHub starts a hub listening for agents on a random local port, with
// its own empty PROCLY_HOME.
func testHub(t *testing.T) (h *hub, addr, fingerprint string) {
	t.Helper()
	t.Setenv("PROCLY_HOME", t.TempDir())
	cert, fp, err := Identity()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	h = newHub(log.New(io.Discard, "", 0))
	go h.acceptAgents(ln)
	t.Cleanup(func() {
		ln.Close()
		h.closeAll()
	})
	return h, ln.Addr().String(), fp
}

// invite registers a tunnel server the way `procly server invite` does and
// returns its token.
func invite(t *testing.T, name string) string {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := remote.Add(remote.Server{Name: name, Mode: remote.ModeTunnel, TokenHash: tlsid.TokenHash(token)}); err != nil {
		t.Fatal(err)
	}
	return token
}

// dial plays the agent's side of the handshake: pinned TLS, hello line,
// reply line. On OK the returned conn is ready for HTTP/2.
func dial(t *testing.T, addr, fingerprint string, hello Hello) (*tls.Conn, Reply) {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, tlsid.Pinned(fingerprint))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	line, _ := json.Marshal(hello)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reply, err := readLine(conn)
	if err != nil {
		t.Fatalf("reading reply: %v", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	var r Reply
	if err := json.Unmarshal(reply, &r); err != nil {
		t.Fatalf("bad reply %q: %v", reply, err)
	}
	return conn, r
}

// serveAgent answers the hub's HTTP/2 requests on conn with handler, the
// way `procly agent connect` does.
func serveAgent(conn net.Conn, handler http.Handler) {
	go (&http2.Server{}).ServeConn(conn, &http2.ServeConnOpts{Handler: handler, BaseConfig: &http.Server{}})
}

func pingHandler(host string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "test", "host": host, "daemon": true})
	})
	return mux
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// closed reports whether the peer has closed conn.
func closed(conn net.Conn) bool {
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := conn.Read(make([]byte, 1))
	return errors.Is(err, io.EOF) || (err != nil && !isTimeout(err))
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func tunnelCount(h *hub) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.tunnels)
}

func get(t *testing.T, h *hub, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

func TestInvitedAgentConnectsAndIsReachable(t *testing.T) {
	h, addr, fp := testHub(t)
	token := invite(t, "prod-3")

	conn, reply := dial(t, addr, fp, Hello{Name: "prod-3", Token: token, Host: "office-box", Version: "test"})
	if !reply.OK {
		t.Fatalf("hub refused a valid agent: %q", reply.Error)
	}
	serveAgent(conn, pingHandler("office-box"))
	waitFor(t, "tunnel to register", func() bool { return h.get("prod-3") != nil })

	code, body := get(t, h, "/servers/prod-3/v1/ping")
	if code != http.StatusOK || !strings.Contains(body, `"host":"office-box"`) {
		t.Fatalf("proxy to agent: %d %s", code, body)
	}

	code, body = get(t, h, "/servers")
	var list []TunnelInfo
	if code != http.StatusOK || json.Unmarshal([]byte(body), &list) != nil || len(list) != 1 ||
		list[0].Name != "prod-3" || list[0].Host != "office-box" {
		t.Fatalf("GET /servers: %d %s", code, body)
	}
}

func TestBadTokenAndUnknownNameGetTheSameAnswer(t *testing.T) {
	h, addr, fp := testHub(t)
	invite(t, "prod-3")

	badConn, bad := dial(t, addr, fp, Hello{Name: "prod-3", Token: "not-the-token"})
	unknownConn, unknown := dial(t, addr, fp, Hello{Name: "nobody", Token: "x"})

	if bad.OK || unknown.OK {
		t.Fatal("hub accepted a bad token or an unknown name")
	}
	if bad.Error == "" || bad.Error != unknown.Error {
		t.Fatalf("answers differ, so names can be probed: %q vs %q", bad.Error, unknown.Error)
	}
	if !closed(badConn) || !closed(unknownConn) {
		t.Fatal("hub kept a rejected connection open")
	}
	if h.get("prod-3") != nil || h.get("nobody") != nil {
		t.Fatal("a rejected agent was registered")
	}
}

func TestDirectServerCannotDialIn(t *testing.T) {
	h, addr, fp := testHub(t)
	// A direct server's token is the one the hub sends out; it must not
	// double as a way in, even if its entry also carries a matching hash
	// (say, a server switched from tunnel to direct by hand).
	if err := remote.Add(remote.Server{Name: "prod-2", URL: "https://10.0.0.2:4748", Token: "direct-token",
		Fingerprint: "sha256:" + strings.Repeat("ab", 32), TokenHash: tlsid.TokenHash("direct-token")}); err != nil {
		t.Fatal(err)
	}
	_, reply := dial(t, addr, fp, Hello{Name: "prod-2", Token: "direct-token"})
	if reply.OK || h.get("prod-2") != nil {
		t.Fatal("a direct server was accepted as a tunnel")
	}
}

func TestProbeChecksWithoutRegistering(t *testing.T) {
	h, addr, fp := testHub(t)
	token := invite(t, "prod-3")

	conn, reply := dial(t, addr, fp, Hello{Name: "prod-3", Token: token, Probe: true})
	if !reply.OK {
		t.Fatalf("probe with a valid token refused: %q", reply.Error)
	}
	if !closed(conn) {
		t.Fatal("hub kept a probe connection open")
	}
	if h.get("prod-3") != nil {
		t.Fatal("probe registered a tunnel")
	}
}

func TestWrongHubCertificateIsRefusedBeforeHello(t *testing.T) {
	h, addr, _ := testHub(t)
	invite(t, "prod-3")
	conn, err := tls.Dial("tcp", addr, tlsid.Pinned("sha256:"+strings.Repeat("cd", 32)))
	if err == nil {
		conn.Close()
		t.Fatal("agent connected to a hub with a different certificate")
	}
	if !errors.Is(err, tlsid.ErrMismatch) {
		t.Fatalf("want ErrMismatch, got %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if tunnelCount(h) != 0 {
		t.Fatal("tunnel registered without a handshake")
	}
}

func TestReconnectReplacesOldTunnel(t *testing.T) {
	h, addr, fp := testHub(t)
	token := invite(t, "prod-3")

	first, r1 := dial(t, addr, fp, Hello{Name: "prod-3", Token: token, Host: "first"})
	if !r1.OK {
		t.Fatal(r1.Error)
	}
	serveAgent(first, pingHandler("first"))
	waitFor(t, "first tunnel", func() bool { t1 := h.get("prod-3"); return t1 != nil && t1.host == "first" })

	second, r2 := dial(t, addr, fp, Hello{Name: "prod-3", Token: token, Host: "second"})
	if !r2.OK {
		t.Fatal(r2.Error)
	}
	serveAgent(second, pingHandler("second"))
	waitFor(t, "second tunnel", func() bool { t2 := h.get("prod-3"); return t2 != nil && t2.host == "second" })

	if _, body := get(t, h, "/servers/prod-3/v1/ping"); !strings.Contains(body, `"host":"second"`) {
		t.Fatalf("requests still go to the old connection: %s", body)
	}
	if _, err := first.Write([]byte("x")); err == nil {
		// Writes may be buffered; a read tells us for sure.
		if !closed(first) {
			t.Fatal("old connection was left open")
		}
	}
}

func TestRemovedOrReinvitedServerIsDisconnected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T)
	}{
		{"removed", func(t *testing.T) {
			if err := remote.Remove("prod-3"); err != nil {
				t.Fatal(err)
			}
		}},
		{"re-invited", func(t *testing.T) { invite(t, "prod-3") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, addr, fp := testHub(t)
			token := invite(t, "prod-3")
			conn, reply := dial(t, addr, fp, Hello{Name: "prod-3", Token: token})
			if !reply.OK {
				t.Fatal(reply.Error)
			}
			serveAgent(conn, pingHandler("box"))
			waitFor(t, "tunnel", func() bool { return h.get("prod-3") != nil })

			tc.change(t)
			h.reconcile()
			waitFor(t, "tunnel to be dropped", func() bool { return h.get("prod-3") == nil })

			// The old token no longer works.
			_, again := dial(t, addr, fp, Hello{Name: "prod-3", Token: token})
			if again.OK {
				t.Fatal("old token still accepted")
			}
		})
	}
}

func TestReconcileKeepsValidTunnels(t *testing.T) {
	h, addr, fp := testHub(t)
	token := invite(t, "prod-3")
	conn, reply := dial(t, addr, fp, Hello{Name: "prod-3", Token: token})
	if !reply.OK {
		t.Fatal(reply.Error)
	}
	serveAgent(conn, pingHandler("box"))
	waitFor(t, "tunnel", func() bool { return h.get("prod-3") != nil })

	h.reconcile()
	time.Sleep(300 * time.Millisecond)
	if code, _ := get(t, h, "/servers/prod-3/v1/ping"); code != http.StatusOK {
		t.Fatalf("reconcile dropped a valid tunnel (status %d)", code)
	}
}

func TestProxyToUnconnectedServer(t *testing.T) {
	h, _, _ := testHub(t)
	code, body := get(t, h, "/servers/ghost/v1/ping")
	if code != http.StatusBadGateway || !strings.Contains(body, "not connected") {
		t.Fatalf("got %d %s", code, body)
	}
}

// Log streams must reach the local client line by line while the agent is
// still writing, not when the agent's response ends. (httputil.ReverseProxy
// already flushes responses of unknown length right away; FlushInterval -1
// in proxy() is a second guarantee, so this test checks the behavior, not
// that setting.)
func TestProxyStreamsWithoutBuffering(t *testing.T) {
	for _, contentType := range []string{"text/event-stream", "application/x-ndjson"} {
		t.Run(contentType, func(t *testing.T) {
			h, addr, fp := testHub(t)
			token := invite(t, "prod-3")
			conn, reply := dial(t, addr, fp, Hello{Name: "prod-3", Token: token})
			if !reply.OK {
				t.Fatal(reply.Error)
			}
			release := make(chan struct{})
			mux := http.NewServeMux()
			mux.HandleFunc("GET /v1/stream", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", contentType)
				_, _ = io.WriteString(w, "first\n")
				w.(http.Flusher).Flush()
				<-release // hold the response open
				_, _ = io.WriteString(w, "second\n")
			})
			serveAgent(conn, mux)
			waitFor(t, "tunnel", func() bool { return h.get("prod-3") != nil })

			srv := httptest.NewServer(h.routes())
			defer srv.Close()
			defer close(release)
			resp, err := http.Get(srv.URL + "/servers/prod-3/v1/stream")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			got := make(chan string, 1)
			go func() {
				line, _ := bufio.NewReader(resp.Body).ReadString('\n')
				got <- line
			}()
			select {
			case line := <-got:
				if line != "first\n" {
					t.Fatalf("first line: %q", line)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("first line was buffered instead of streamed")
			}
		})
	}
}

func TestReadLineStopsAtNewline(t *testing.T) {
	r := bytes.NewReader([]byte("{\"name\":\"a\"}\nPRI * HTTP/2.0"))
	line, err := readLine(r)
	if err != nil || string(line) != `{"name":"a"}` {
		t.Fatalf("got %q, %v", line, err)
	}
	rest, _ := io.ReadAll(r)
	if string(rest) != "PRI * HTTP/2.0" {
		t.Fatalf("readLine consumed bytes after the newline: rest=%q", rest)
	}

	if _, err := readLine(bytes.NewReader(bytes.Repeat([]byte("a"), maxHello+10))); err == nil {
		t.Fatal("an oversized hello was accepted")
	}
	if _, err := readLine(bytes.NewReader([]byte("no newline"))); err == nil {
		t.Fatal("a hello without newline was accepted")
	}
}

func TestGarbageHelloIsDropped(t *testing.T) {
	h, addr, fp := testHub(t)
	conn, err := tls.Dial("tcp", addr, tlsid.Pinned(fp))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("GET / HTTP/1.1\n"))
	if !closed(conn) {
		t.Fatal("hub kept a connection with a non-JSON hello")
	}
	if tunnelCount(h) != 0 {
		t.Fatal("garbage registered a tunnel")
	}
}
