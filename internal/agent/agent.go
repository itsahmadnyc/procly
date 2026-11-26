// Package agent exposes this server's processes to a procly hub over TLS,
// so one web panel can manage many servers.
package agent

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/itsahmadnyc/procly/internal/client"
	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/sse"
	"github.com/itsahmadnyc/procly/internal/sysinfo"
	"github.com/itsahmadnyc/procly/internal/version"
)

const (
	DefaultPort = "4748"
	DefaultAddr = "0.0.0.0:" + DefaultPort
)

type Options struct {
	Addr       string
	ResetToken bool
	Out        io.Writer
}

type agent struct {
	c         *client.Client
	tokenHash []byte
	host      string
	logger    *log.Logger
}

// Run serves the agent API until SIGINT/SIGTERM.
func Run(opts Options) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	id, err := loadIdentity(filepath.Join(paths.Home(), "agent.json"), opts.ResetToken)
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	a := &agent{c: client.New(), tokenHash: id.tokenHash, host: host, logger: log.New(os.Stderr, "", log.LstdFlags)}

	ln, err := tls.Listen("tcp", opts.Addr, &tls.Config{
		Certificates: []tls.Certificate{id.cert},
		MinVersion:   tls.VersionTLS13,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &http.Server{
		Handler:           a.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          log.New(io.Discard, "", 0), // TLS handshake noise from scanners
	}
	printBanner(opts.Out, opts.Addr, id)

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

func printBanner(w io.Writer, addr string, id *identity) {
	if w == nil {
		w = os.Stdout
	}
	host, port, _ := net.SplitHostPort(addr)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "<this-server-address>"
	}
	fmt.Fprintf(w, "\n  procly agent listening on %s (TLS)\n", addr)
	fmt.Fprintf(w, "  fingerprint  %s\n", id.fingerprint)
	if id.newToken != "" {
		fmt.Fprintln(w, "\n  On the server that runs the panel, add this one with:")
		fmt.Fprintf(w, "\n  procly server add <name> https://%s:%s \\\n    --token %s \\\n    --fingerprint %s\n", host, port, id.newToken, id.fingerprint)
		fmt.Fprintln(w, "\n  The token is shown once. Only its hash is kept on disk.")
	} else {
		fmt.Fprintln(w, "  The token was shown when the agent first started. New one: procly agent --reset-token")
	}
	fmt.Fprintf(w, "  Allow port %s only from your panel server in the firewall.\n\n", port)
}

// routes is the API for hubs that dial this agent: the shared handlers
// behind a bearer-token check.
func (a *agent) routes() http.Handler { return a.authenticate(a.handlers()) }

// handlers is the agent API without authentication. Tunnel connections use
// it directly, because the hub checked this agent's token at connect time
// and only the hub can send requests down that connection.
func (a *agent) handlers() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{"version": version.Version, "host": a.host, "daemon": false}
		if _, err := a.c.Ping(); err == nil {
			resp["daemon"] = true
		}
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		infos, err := a.c.List()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "procly daemon is not running on " + a.host})
			return
		}
		writeJSON(w, http.StatusOK, proc.NewViews(infos))
	})
	mux.HandleFunc("GET /v1/system", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, sysinfo.Read())
	})
	mux.HandleFunc("POST /v1/processes/{name}/{action}", a.action)
	mux.HandleFunc("POST /v1/save", func(w http.ResponseWriter, r *http.Request) {
		n, _, err := a.c.Save()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"saved": n})
	})
	mux.HandleFunc("GET /v1/processes/{name}/logs", func(w http.ResponseWriter, r *http.Request) {
		info, err := a.c.Get(r.PathValue("name"))
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "process not found"})
			return
		}
		send, ok := sse.Writer(w)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
			return
		}
		logtail.Stream(r.Context(), info.OutLog, info.ErrLog, 300, send)
	})
	return mux
}

// authenticate checks the bearer token against the stored hash in constant time.
func (a *agent) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(token))
		if !ok || subtle.ConstantTimeCompare(sum[:], a.tokenHash) != 1 {
			a.logger.Printf("rejected %s %s from %s: bad token", r.Method, r.URL.Path, r.RemoteAddr)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *agent) action(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "all" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "act on one process at a time"})
		return
	}
	var err error
	switch r.PathValue("action") {
	case "restart", "start":
		_, err = a.c.Restart(name)
	case "stop":
		_, err = a.c.Stop(name)
	case "reload":
		_, err = a.c.Reload(name)
	case "delete":
		_, err = a.c.Delete(name)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown action"})
		return
	}
	if err != nil {
		code := http.StatusServiceUnavailable
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			code = apiErr.Status
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
