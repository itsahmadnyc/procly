// Package web serves the procly web panel: a small JSON API over the daemon
// plus a static frontend embedded in the binary.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/itsahmadnyc/procly/internal/client"
	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/remote"
	"github.com/itsahmadnyc/procly/internal/sse"
	"github.com/itsahmadnyc/procly/internal/version"
)

var errDaemonDown = errors.New("the procly daemon is not running")

//go:embed assets
var assets embed.FS

const DefaultAddr = "127.0.0.1:4747"

type Options struct {
	Addr          string
	ResetPassword bool
	// TrustedProxies are addresses or ranges allowed to set X-Forwarded-For;
	// empty trusts nobody. See ParseTrustedProxies.
	TrustedProxies []string
	// TLSCert and TLSKey, both or neither, make the panel serve HTTPS itself. The
	// files are read again when they change and on SIGHUP.
	TLSCert, TLSKey string
	Out             io.Writer // where the startup banner goes
}

type server struct {
	servers *registry
	auth    *auth
	host    string
	proxies trustedProxies // who may say who the visitor is, see clientIP
}

// Run serves the panel until SIGINT/SIGTERM.
// certWatchEvery is how often the certificate files are looked at.
const certWatchEvery = 10 * time.Second

func Run(opts Options) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	a, newPassword, err := loadAuth(filepath.Join(paths.Home(), "web.json"), opts.ResetPassword)
	if err != nil {
		return err
	}
	proxies, err := ParseTrustedProxies(opts.TrustedProxies)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	s := &server{servers: newRegistry(localBackend{client.New()}), auth: a, host: host, proxies: proxies}

	if (opts.TLSCert == "") != (opts.TLSKey == "") {
		return errors.New("--tls-cert and --tls-key go together: give both or neither")
	}
	var certs *certReloader
	if opts.TLSCert != "" {
		if certs, err = newCertReloader(opts.TLSCert, opts.TLSKey); err != nil {
			return err
		}
	}

	ln, err := net.Listen("tcp", opts.Addr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx }, // ends log streams on shutdown
	}

	scheme := "http"
	if certs != nil {
		scheme = "https"
	}
	printBanner(opts.Out, scheme, ln.Addr().String(), newPassword)
	if len(proxies) > 0 {
		out := opts.Out
		if out == nil {
			out = os.Stdout
		}
		fmt.Fprintf(out, "\n  Trusting X-Forwarded-For from %s for the login lockout.\n", proxies)
		if proxies.broad() {
			fmt.Fprintln(out, "  Warning: that is every address, so anyone can choose which address gets locked out.")
		}
	}

	if certs != nil {
		srv.TLSConfig = certs.config()
		logf := func(format string, args ...any) {
			out := opts.Out
			if out == nil {
				out = os.Stdout
			}
			fmt.Fprintf(out, "  "+format+"\n", args...)
		}
		go certs.watch(ctx, certWatchEvery, logf)
		// SIGHUP loads the certificate again at once, e.g. from a renewal hook
		// (procly reload panel, with reload_signal: SIGHUP).
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-hup:
					if err := certs.reload(); err != nil {
						logf("%v; still serving the previous certificate", err)
					} else {
						logf("tls: loaded the certificate again (SIGHUP)")
					}
				}
			}
		}()
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = srv.Shutdown(sctx)
	}()

	serve := srv.Serve
	if certs != nil {
		serve = func(l net.Listener) error { return srv.ServeTLS(l, "", "") }
	}
	if err := serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func printBanner(w io.Writer, scheme, addr, newPassword string) {
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprintf(w, "\n  procly web panel  %s://%s\n", scheme, addr)
	if newPassword != "" {
		fmt.Fprintf(w, "\n  password  %s\n", newPassword)
		fmt.Fprintln(w, "  Shown once. Save it now; only a hash is kept on disk.")
	} else {
		fmt.Fprintln(w, "  Sign in with the password shown when the panel was first started.")
		fmt.Fprintln(w, "  Forgot it? Run: procly web --reset-password")
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		if ip := net.ParseIP(h); (ip == nil || !ip.IsLoopback()) && scheme == "http" {
			fmt.Fprintln(w, "\n  Warning: listening beyond localhost over plain HTTP.")
			fmt.Fprintln(w, "  Serve HTTPS with --tls-cert and --tls-key, put it behind a proxy (Caddy, nginx), or reach it with: ssh -L 4747:localhost:4747 <server>")
		}
	}
	fmt.Fprintln(w)
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(assets, "assets")
	files := http.FileServerFS(static)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", files))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, static, "index.html")
	})

	mux.HandleFunc("POST /api/login", s.csrf(s.login))
	mux.HandleFunc("POST /api/logout", s.csrf(s.logout))
	mux.HandleFunc("GET /api/info", s.private(s.info))
	mux.HandleFunc("GET /api/servers", s.private(s.serverList))
	mux.HandleFunc("GET /api/servers/{server}/processes", s.private(s.withBackend(s.list)))
	mux.HandleFunc("GET /api/servers/{server}/system", s.private(s.withBackend(s.system)))
	mux.HandleFunc("GET /api/servers/{server}/domains", s.private(s.withBackend(s.domains)))
	mux.HandleFunc("POST /api/servers/{server}/processes/{name}/{action}", s.private(s.csrf(s.withBackend(s.action))))
	mux.HandleFunc("GET /api/servers/{server}/processes/{name}/logs", s.private(s.withBackend(s.logs)))
	mux.HandleFunc("POST /api/servers/{server}/save", s.private(s.csrf(s.withBackend(s.save))))
	mux.HandleFunc("POST /api/servers/{server}/processes", s.private(s.csrf(s.withBackend(s.start))))

	return secureHeaders(mux)
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// private rejects requests without a valid session.
func (s *server) private(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil || !s.auth.validSession(c.Value) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in required"})
			return
		}
		h(w, r)
	}
}

// csrf requires a custom header that cross-site forms and simple requests
// cannot set, on top of the SameSite=Strict cookie.
func (s *server) csrf(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Procly") != "1" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "missing X-Procly header"})
			return
		}
		h(w, r)
	}
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if ok, wait := s.auth.allowLogin(ip); !ok {
		w.Header().Set("Retry-After", fmt.Sprint(int(wait.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{
			"error": fmt.Sprintf("too many attempts, try again in %d minutes", int(wait.Minutes())+1),
		})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if !s.auth.checkPassword(body.Password) {
		s.auth.recordFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong password"})
		return
	}
	s.auth.clearFailures(ip)
	token, err := s.auth.newSession()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not start a session"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// secureRequest reports whether the browser reached us over HTTPS, directly or
// through a reverse proxy that says so. The session cookie is Secure exactly
// then, and so is the cookie that ends it.
func secureRequest(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.auth.endSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) info(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	writeJSON(w, http.StatusOK, map[string]any{"host": s.host, "version": version.Version, "home": home})
}

type startRequest struct {
	Name        string            `json:"name"`
	Command     string            `json:"command"`
	Cwd         string            `json:"cwd"`
	Env         map[string]string `json:"env"`
	AutoRestart *bool             `json:"autorestart"`
	Health      string            `json:"health"`
}

// start runs a new command on this machine. The panel is an admin tool, so
// a signed-in user may start anything the panel's own user could run.
func (s *server) start(w http.ResponseWriter, r *http.Request, b backend, server string) {
	st, ok := b.(starter)
	if !ok || server != remote.LocalName {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "new processes can only be started on this server; agents don't start new commands. Run procly start on " + server + ".",
		})
		return
	}
	var req startRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	spec := proc.Spec{
		Name: strings.TrimSpace(req.Name), Command: strings.TrimSpace(req.Command), Cwd: strings.TrimSpace(req.Cwd),
		Env: req.Env, AutoRestart: req.AutoRestart, Health: strings.TrimSpace(req.Health),
	}
	if spec.Env == nil {
		spec.Env = map[string]string{}
	}
	// Like the CLI: give the process the PATH procly itself was started with.
	if _, set := spec.Env["PATH"]; !set {
		spec.Env["PATH"] = os.Getenv("PATH")
	}
	if err := spec.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := st.Start(r.Context(), spec); err != nil {
		writeError(w, server, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": spec.Name})
}

type backendHandler func(w http.ResponseWriter, r *http.Request, b backend, server string)

func (s *server) withBackend(h backendHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("server")
		b, ok := s.servers.get(name)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown server " + name})
			return
		}
		h(w, r, b, name)
	}
}

type serverStatus struct {
	Name     string         `json:"name"`
	Label    string         `json:"label"`
	Local    bool           `json:"local"`
	URL      string         `json:"url,omitempty"`
	OK       bool           `json:"ok"`
	Error    string         `json:"error,omitempty"`
	Total    int            `json:"total"`
	ByStatus map[string]int `json:"by_status"`
}

// serverList checks every server in parallel, each with a short timeout,
// so one unreachable machine can't stall the panel.
func (s *server) serverList(w http.ResponseWriter, r *http.Request) {
	entries, regErr := s.servers.all()
	out := make([]serverStatus, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		out[i] = serverStatus{Name: e.name, Label: e.name, Local: e.name == remote.LocalName, URL: e.url, ByStatus: map[string]int{}}
		if out[i].Local {
			out[i].Label = s.host
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
			defer cancel()
			views, err := e.backend.List(ctx)
			if err != nil {
				out[i].Error = friendly(err)
				return
			}
			out[i].OK = true
			out[i].Total = len(views)
			for _, v := range views {
				out[i].ByStatus[string(v.Status)]++
			}
		}()
	}
	wg.Wait()
	resp := map[string]any{"servers": out}
	if regErr != nil {
		resp["error"] = regErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) list(w http.ResponseWriter, r *http.Request, b backend, server string) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	views, err := b.List(ctx)
	if err != nil {
		writeError(w, server, err)
		return
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *server) action(w http.ResponseWriter, r *http.Request, b backend, server string) {
	name, verb := r.PathValue("name"), r.PathValue("action")
	if name == "all" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "act on one process at a time"})
		return
	}
	switch verb {
	case "restart", "start", "stop", "reload", "delete":
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown action"})
		return
	}
	if err := b.Action(r.Context(), name, verb); err != nil {
		writeError(w, server, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) save(w http.ResponseWriter, r *http.Request, b backend, server string) {
	n, err := b.Save(r.Context())
	if err != nil {
		writeError(w, server, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"saved": n})
}

func (s *server) system(w http.ResponseWriter, r *http.Request, b backend, server string) {
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	sys, err := b.System(ctx)
	if err != nil {
		writeError(w, server, err)
		return
	}
	writeJSON(w, http.StatusOK, sys)
}

// domains says which names reach each process on a server, as found in that
// machine's proxy configuration.
func (s *server) domains(w http.ResponseWriter, r *http.Request, b backend, server string) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	rep, err := b.Domains(ctx, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeError(w, server, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// logs relays a process's output as server-sent events, whether it comes
// from local files or from an agent's stream.
func (s *server) logs(w http.ResponseWriter, r *http.Request, b backend, server string) {
	send, ok := sse.Writer(w)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	started := false
	err := b.Logs(r.Context(), r.PathValue("name"), func(batch []logtail.Event) bool {
		started = true
		return send(batch)
	})
	if err != nil && !started {
		// Nothing streamed yet, so a normal error response still fits.
		w.Header().Set("Content-Type", "application/json")
		writeError(w, server, err)
	}
}

// writeError maps backend errors to HTTP codes the frontend understands.
func writeError(w http.ResponseWriter, server string, err error) {
	code := http.StatusBadGateway
	var apiErr *client.APIError
	var remoteErr *remote.StatusError
	switch {
	case errors.Is(err, errDaemonDown):
		code = http.StatusServiceUnavailable
	case errors.As(err, &apiErr):
		code = apiErr.Status
	case errors.As(err, &remoteErr):
		code = remoteErr.Code
		if code == http.StatusUnauthorized {
			code = http.StatusBadGateway // the agent rejected our token, not the browser's session
		}
	}
	msg := friendly(err)
	if server != remote.LocalName {
		msg = server + ": " + msg
	}
	writeJSON(w, code, map[string]string{"error": msg})
}

func friendly(err error) string {
	var remoteErr *remote.StatusError
	switch {
	case errors.Is(err, remote.ErrFingerprint):
		return "certificate fingerprint does not match the one saved with 'procly server add'"
	case errors.Is(err, remote.ErrHubDown):
		return err.Error()
	case errors.As(err, &remoteErr) && remoteErr.Code == http.StatusUnauthorized:
		return "the agent rejected the token"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return "unreachable (" + netErr.Error() + ")"
	}
	return err.Error()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
