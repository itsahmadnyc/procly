// Package daemon runs the long-lived procly supervisor and its local API.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/itsahmadnyc/procly/internal/alerts"
	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/version"
)

// Run starts the daemon in the foreground and blocks until it is told to stop.
func Run(resurrect bool) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	sock := paths.Socket()

	if conn, err := net.DialTimeout("unix", sock, 300*time.Millisecond); err == nil {
		conn.Close()
		return errors.New("procly daemon is already running")
	}
	_ = os.Remove(sock) // stale socket from a crashed daemon
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	_ = os.Chmod(sock, 0o600)
	_ = os.WriteFile(paths.PidFile(), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	defer os.Remove(paths.PidFile())

	mgr := proc.NewManager(paths.LogDir(), logger)
	mgr.TrackRunning(paths.RunFile())
	// Processes a daemon that died left running: list them, never start a second copy.
	if orphans := mgr.RecognizeOrphans(); len(orphans) > 0 {
		logger.Printf("%d %s still running from a daemon that died", len(orphans), plural(len(orphans), "process is", "processes are"))
	}
	host, _ := os.Hostname()
	mgr.OnEvent(alerts.NewNotifier(host, logger).Handle)
	quit := make(chan struct{})
	var once sync.Once
	shutdown := func() { once.Do(func() { close(quit) }) }
	srv := &http.Server{Handler: newHandler(mgr, shutdown), ReadHeaderTimeout: 10 * time.Second}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	signal.Ignore(syscall.SIGHUP)

	done := make(chan struct{})
	go rotateLoop(quit, mgr, logger)
	go func() {
		defer close(done)
		select {
		case s := <-sigs:
			logger.Printf("received %s", s)
		case <-quit:
		}
		logger.Printf("shutting down")
		mgr.StopAll()
		mgr.WaitIdle()     // a late background write must not undo the next line
		mgr.FlushRunning() // nothing is left running, so this removes the file
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	logger.Printf("procly %s daemon listening on %s pid=%d", version.Version, sock, os.Getpid())
	if resurrect {
		infos, err := mgr.Resurrect(paths.DumpFile())
		if err != nil {
			logger.Printf("resurrect: %v", err)
		}
		logger.Printf("resurrected %d processes", len(infos))
	}

	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	} else {
		shutdown()
	}
	<-done
	logger.Printf("stopped")
	return err
}

// rotateLoop keeps process logs and the daemon's own log under their size limits.
func rotateLoop(quit <-chan struct{}, m *proc.Manager, logger *log.Logger) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-quit:
			return
		case <-tick.C:
		}
		m.RotateLogs()
		if rotated, err := proc.RotateFile(paths.DaemonLog(), 10<<20, 3); err != nil {
			logger.Printf("rotating daemon log: %v", err)
		} else if rotated {
			logger.Printf("rotated %s", paths.DaemonLog())
		}
	}
}

func newHandler(m *proc.Manager, shutdown func()) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"version": version.Version, "pid": os.Getpid()})
	})
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.List())
	})
	mux.HandleFunc("GET /v1/processes/{name}", func(w http.ResponseWriter, r *http.Request) {
		info, err := m.Get(r.PathValue("name"))
		respond(w, info, err)
	})
	mux.HandleFunc("POST /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		var spec proc.Spec
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&spec); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}
		info, err := m.Start(spec)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, info)
	})
	mux.HandleFunc("POST /v1/processes/{name}/stop", func(w http.ResponseWriter, r *http.Request) {
		infos, err := m.Stop(r.PathValue("name"))
		respond(w, infos, err)
	})
	mux.HandleFunc("POST /v1/processes/{name}/restart", func(w http.ResponseWriter, r *http.Request) {
		infos, err := m.Restart(r.PathValue("name"))
		respond(w, infos, err)
	})
	mux.HandleFunc("DELETE /v1/processes/{name}", func(w http.ResponseWriter, r *http.Request) {
		infos, err := m.Delete(r.PathValue("name"))
		respond(w, infos, err)
	})
	mux.HandleFunc("POST /v1/save", func(w http.ResponseWriter, r *http.Request) {
		n, err := m.Save(paths.DumpFile())
		respond(w, map[string]any{"saved": n, "path": paths.DumpFile()}, err)
	})
	mux.HandleFunc("POST /v1/resurrect", func(w http.ResponseWriter, r *http.Request) {
		infos, err := m.Resurrect(paths.DumpFile())
		respond(w, infos, err)
	})
	mux.HandleFunc("POST /v1/kill", func(w http.ResponseWriter, r *http.Request) {
		infos := m.StopAll()
		writeJSON(w, http.StatusOK, infos)
		shutdown()
	})
	return mux
}

func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func writeError(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	if errors.Is(err, proc.ErrNotFound) {
		code = http.StatusNotFound
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
