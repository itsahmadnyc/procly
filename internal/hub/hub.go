// Package hub accepts connections from agents that dial in (servers behind
// NAT or without an open port) and serves them to local clients — the web
// panel and `procly --server` — over a unix socket.
//
// Each agent connection is TLS, with the hub's certificate pinned by the
// agent. The agent's first line proves who it is (name + token, checked
// against servers.json); after that the hub speaks HTTP/2 to it as a client,
// so many requests and log streams share that one connection.
package hub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/http2"

	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/remote"
	"github.com/itsahmadnyc/procly/internal/tlsid"
	"github.com/itsahmadnyc/procly/internal/version"
)

const (
	DefaultPort = "4749"
	DefaultAddr = "0.0.0.0:" + DefaultPort
	maxHello    = 4096
)

// Hello is the single line an agent sends right after the TLS handshake.
type Hello struct {
	Name    string `json:"name"`
	Token   string `json:"token"`
	Host    string `json:"host"`
	Version string `json:"version"`
	// Probe asks the hub to check the credentials and hang up, without
	// registering a tunnel. `procly agent join` uses it.
	Probe bool `json:"probe,omitempty"`
}

// Reply is the hub's single-line answer. After OK the connection carries HTTP/2.
type Reply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Identity loads (or creates) the hub's TLS certificate and returns its fingerprint.
func Identity() (tls.Certificate, string, error) {
	if err := paths.Ensure(); err != nil {
		return tls.Certificate{}, "", err
	}
	host, _ := os.Hostname()
	return tlsid.LoadOrCreate(filepath.Join(paths.Home(), "hub.json"), "procly hub "+host)
}

type tunnel struct {
	name      string
	tokenHash string
	cc        *http2.ClientConn
	conn      net.Conn
	host      string
	version   string
	remote    string
	since     time.Time
}

type hub struct {
	mu      sync.Mutex
	tunnels map[string]*tunnel
	logger  *log.Logger
	h2      *http2.Transport
}

func newHub(logger *log.Logger) *hub {
	return &hub{
		tunnels: map[string]*tunnel{},
		logger:  logger,
		// Pings find agents that vanished without closing the connection.
		h2: &http2.Transport{ReadIdleTimeout: 30 * time.Second, PingTimeout: 15 * time.Second},
	}
}

type Options struct {
	Addr string
	Out  io.Writer
}

// Run serves agents on opts.Addr and local clients on the hub socket until
// SIGINT/SIGTERM.
func Run(opts Options) error {
	cert, fp, err := Identity()
	if err != nil {
		return err
	}
	sock := paths.HubSocket()
	if c, err := net.DialTimeout("unix", sock, 300*time.Millisecond); err == nil {
		c.Close()
		return errors.New("procly hub is already running")
	}
	_ = os.Remove(sock)
	local, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(sock, 0o600)
	agents, err := tls.Listen("tcp", opts.Addr, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
	if err != nil {
		local.Close()
		return err
	}

	h := newHub(log.New(os.Stderr, "", log.LstdFlags))
	printBanner(opts.Out, opts.Addr, fp)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &http.Server{Handler: h.routes(), ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}

	go h.acceptAgents(agents)
	go h.reconcileLoop(ctx)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
		agents.Close()
		h.closeAll()
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.Serve(local); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func printBanner(w io.Writer, addr, fp string) {
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprintf(w, "\n  procly hub listening for agents on %s (TLS)\n", addr)
	fmt.Fprintf(w, "  fingerprint  %s\n\n", fp)
	fmt.Fprintln(w, "  Invite a server:  procly server invite <name> --hub-address <this-server>:"+portOf(addr))
	fmt.Fprintln(w, "  Connected servers show up in 'procly web' and work with --server.")
	fmt.Fprintln(w)
}

func portOf(addr string) string {
	if _, p, err := net.SplitHostPort(addr); err == nil {
		return p
	}
	return DefaultPort
}

func (h *hub) acceptAgents(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			h.logger.Printf("accept: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go h.handshake(conn.(*tls.Conn))
	}
}

// readLine reads up to '\n' one byte at a time, so nothing after the line
// (the start of HTTP/2) is swallowed into a buffer.
func readLine(r io.Reader) ([]byte, error) {
	var line []byte
	b := make([]byte, 1)
	for len(line) < maxHello {
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		if b[0] == '\n' {
			return line, nil
		}
		line = append(line, b[0])
	}
	return nil, errors.New("hello line too long")
}

func writeLine(w io.Writer, v any) error {
	data, _ := json.Marshal(v)
	_, err := w.Write(append(data, '\n'))
	return err
}

func (h *hub) handshake(conn *tls.Conn) {
	addr := conn.RemoteAddr().String()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := conn.Handshake(); err != nil {
		conn.Close() // scanners and wrong ports; not worth logging
		return
	}
	line, err := readLine(conn)
	if err != nil {
		conn.Close()
		return
	}
	var hello Hello
	if err := json.Unmarshal(line, &hello); err != nil {
		conn.Close()
		return
	}
	srv, ok, err := remote.Get(hello.Name)
	if err != nil || !ok || srv.Mode != remote.ModeTunnel || !tlsid.TokenMatches(hello.Token, srv.TokenHash) {
		h.logger.Printf("rejected agent %q from %s: unknown server or bad token", hello.Name, addr)
		// Same answer for both cases, so names can't be probed.
		_ = writeLine(conn, Reply{Error: "unknown server or bad token; run 'procly server invite' on the hub again"})
		conn.Close()
		return
	}
	if err := writeLine(conn, Reply{OK: true}); err != nil {
		conn.Close()
		return
	}
	if hello.Probe {
		h.logger.Printf("%s checked its join settings from %s", hello.Name, addr)
		conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})

	cc, err := h.h2.NewClientConn(conn)
	if err != nil {
		h.logger.Printf("%s: http/2 setup failed: %v", hello.Name, err)
		conn.Close()
		return
	}
	t := &tunnel{name: hello.Name, tokenHash: srv.TokenHash, cc: cc, conn: conn, host: hello.Host,
		version: hello.Version, remote: addr, since: time.Now()}

	h.mu.Lock()
	old := h.tunnels[t.name]
	h.tunnels[t.name] = t
	h.mu.Unlock()
	if old != nil {
		old.conn.Close() // the agent reconnected; drop the stale connection
	}
	h.logger.Printf("%s connected from %s (%s, procly %s)", t.name, addr, t.host, t.version)
	go h.watch(t)
}

// watch removes a tunnel once its connection closes.
func (h *hub) watch(t *tunnel) {
	for {
		time.Sleep(2 * time.Second)
		if st := t.cc.State(); st.Closed || st.Closing {
			break
		}
	}
	t.conn.Close()
	h.mu.Lock()
	if h.tunnels[t.name] == t {
		delete(h.tunnels, t.name)
		h.logger.Printf("%s disconnected", t.name)
	}
	h.mu.Unlock()
}

// reconcileLoop drops tunnels whose server was removed or re-invited.
func (h *hub) reconcileLoop(ctx context.Context) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		h.reconcile()
	}
}

// reconcile closes every tunnel whose server is gone from servers.json or
// has a different token hash than when it connected.
func (h *hub) reconcile() {
	list, err := remote.Load()
	if err != nil {
		return
	}
	valid := map[string]string{}
	for _, s := range list {
		if s.Mode == remote.ModeTunnel {
			valid[s.Name] = s.TokenHash
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for name, t := range h.tunnels {
		if hash, ok := valid[name]; !ok || hash != t.tokenHash {
			h.logger.Printf("%s: removed or re-invited, closing its connection", name)
			t.conn.Close()
		}
	}
}

func (h *hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range h.tunnels {
		t.conn.Close()
	}
}

func (h *hub) get(name string) *tunnel {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tunnels[name]
}

// TunnelInfo is what GET /servers on the hub socket returns.
type TunnelInfo struct {
	Name    string    `json:"name"`
	Host    string    `json:"host"`
	Version string    `json:"version"`
	Remote  string    `json:"remote"`
	Since   time.Time `json:"since"`
}

func (h *hub) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /servers", func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		out := make([]TunnelInfo, 0, len(h.tunnels))
		for _, t := range h.tunnels {
			out = append(out, TunnelInfo{t.name, t.host, t.version, t.remote, t.since})
		}
		h.mu.Unlock()
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": version.Version})
	})
	mux.HandleFunc("/servers/{name}/{rest...}", h.proxy)
	return mux
}

// proxy forwards a local request to the named agent over its tunnel,
// streaming the response (log streams included) as it arrives.
func (h *hub) proxy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	t := h.get(name)
	if t == nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": name + " is not connected to the hub; is 'procly agent connect' running there?"})
		return
	}
	rest := "/" + r.PathValue("rest")
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "https"
			pr.Out.URL.Host = name
			pr.Out.URL.Path = rest
			pr.Out.URL.RawPath = ""
			pr.Out.Host = name
		},
		Transport:     t.cc,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return // client went away
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": name + ": tunnel error: " + err.Error()})
		},
	}
	rp.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
