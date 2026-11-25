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
	"sort"
	"syscall"
	"time"

	"github.com/itsahmadnyc/procly/internal/client"
	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/version"
)

//go:embed assets
var assets embed.FS

const DefaultAddr = "127.0.0.1:4747"

type Options struct {
	Addr          string
	ResetPassword bool
	Out           io.Writer // where the startup banner goes
}

type server struct {
	c    *client.Client
	auth *auth
	host string
}

// Run serves the panel until SIGINT/SIGTERM.
func Run(opts Options) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	a, newPassword, err := loadAuth(filepath.Join(paths.Home(), "web.json"), opts.ResetPassword)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	s := &server{c: client.New(), auth: a, host: host}

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

	printBanner(opts.Out, ln.Addr().String(), newPassword)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = srv.Shutdown(sctx)
	}()

	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func printBanner(w io.Writer, addr, newPassword string) {
	if w == nil {
		w = os.Stdout
	}
	fmt.Fprintf(w, "\n  procly web panel  http://%s\n", addr)
	if newPassword != "" {
		fmt.Fprintf(w, "\n  password  %s\n", newPassword)
		fmt.Fprintln(w, "  Shown once. Save it now; only a hash is kept on disk.")
	} else {
		fmt.Fprintln(w, "  Sign in with the password shown when the panel was first started.")
		fmt.Fprintln(w, "  Forgot it? Run: procly web --reset-password")
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		if ip := net.ParseIP(h); ip == nil || !ip.IsLoopback() {
			fmt.Fprintln(w, "\n  Warning: listening beyond localhost over plain HTTP.")
			fmt.Fprintln(w, "  Put it behind HTTPS (Caddy, nginx) or reach it with: ssh -L 4747:localhost:4747 <server>")
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
	mux.HandleFunc("GET /api/processes", s.private(s.list))
	mux.HandleFunc("POST /api/processes/{name}/{action}", s.private(s.csrf(s.action)))
	mux.HandleFunc("GET /api/processes/{name}/logs", s.private(s.logs))
	mux.HandleFunc("POST /api/save", s.private(s.csrf(s.save)))

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

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
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
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.auth.endSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) info(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"host": s.host, "version": version.Version}
	if p, err := s.c.Ping(); err == nil {
		resp["daemon"] = p
	}
	writeJSON(w, http.StatusOK, resp)
}

// procView is what the browser sees. Environment values stay on the server;
// only the variable names are sent.
type procView struct {
	ID          int         `json:"id"`
	Name        string      `json:"name"`
	Status      proc.Status `json:"status"`
	PID         int         `json:"pid"`
	Restarts    int         `json:"restarts"`
	UptimeMs    int64       `json:"uptime_ms"`
	ExitCode    int         `json:"exit_code"`
	CPU         float64     `json:"cpu"`
	Memory      int64       `json:"memory"`
	Command     string      `json:"command"`
	Cwd         string      `json:"cwd"`
	AutoRestart bool        `json:"autorestart"`
	EnvKeys     []string    `json:"env_keys"`
}

func view(in proc.Info) procView {
	v := procView{
		ID: in.ID, Name: in.Name, Status: in.Status, PID: in.PID, Restarts: in.Restarts,
		ExitCode: in.ExitCode, CPU: in.CPU, Memory: in.Memory,
		Command: in.Spec.Command, Cwd: in.Spec.Cwd, AutoRestart: in.Spec.ShouldAutoRestart(),
		EnvKeys: []string{},
	}
	if in.PID > 0 && !in.StartedAt.IsZero() {
		v.UptimeMs = time.Since(in.StartedAt).Milliseconds()
	}
	for k := range in.Spec.Env {
		if k != "PATH" {
			v.EnvKeys = append(v.EnvKeys, k)
		}
	}
	sort.Strings(v.EnvKeys)
	return v
}

func (s *server) list(w http.ResponseWriter, r *http.Request) {
	infos, err := s.c.List()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "daemon is not running"})
		return
	}
	out := make([]procView, len(infos))
	for i, in := range infos {
		out[i] = view(in)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) action(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "all" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "act on one process at a time"})
		return
	}
	var err error
	switch r.PathValue("action") {
	case "restart", "start":
		_, err = s.c.Restart(name)
	case "stop":
		_, err = s.c.Stop(name)
	case "delete":
		_, err = s.c.Delete(name)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown action"})
		return
	}
	if err != nil {
		code := http.StatusServiceUnavailable // daemon unreachable
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			code = apiErr.Status
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) save(w http.ResponseWriter, r *http.Request) {
	n, _, err := s.c.Save()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"saved": n})
}

type logEvent struct {
	Stream string `json:"s"` // "o" stdout, "e" stderr
	Line   string `json:"l"`
}

// logs streams a process's output as server-sent events: recent history
// first, then new lines as they are written.
func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	info, err := s.c.Get(r.PathValue("name"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "process not found"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")

	send := func(batch []logEvent) bool {
		if len(batch) == 0 {
			return true
		}
		data, _ := json.Marshal(batch)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Paths come from the daemon, never from the request.
	outTail, outSize := logtail.Tail(info.OutLog, 300)
	errTail, errSize := logtail.Tail(info.ErrLog, 300)
	var history []logEvent
	for _, l := range outTail {
		history = append(history, logEvent{"o", l})
	}
	for _, l := range errTail {
		history = append(history, logEvent{"e", l})
	}
	fmt.Fprint(w, ": history\n\n")
	if !send(history) {
		return
	}
	outF := logtail.NewFollower(info.OutLog, outSize)
	errF := logtail.NewFollower(info.ErrLog, errSize)

	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-tick.C:
			var batch []logEvent
			for _, l := range outF.Read() {
				batch = append(batch, logEvent{"o", l})
			}
			for _, l := range errF.Read() {
				batch = append(batch, logEvent{"e", l})
			}
			if !send(batch) {
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
