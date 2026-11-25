package daemon

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/itsahmadnyc/procly/internal/client"
	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/proc"
)

// startDaemon runs the real daemon on its unix socket in this process and
// returns a client for it plus a channel that yields Run's result.
func startDaemon(t *testing.T, resurrect bool) (*client.Client, chan error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- Run(resurrect) }()
	c := client.New()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := c.Ping(); err == nil {
			t.Cleanup(func() {
				if _, err := c.Ping(); err == nil {
					_, _ = c.Kill()
					<-done
				}
			})
			return c, done
		}
		select {
		case err := <-done:
			t.Fatalf("daemon exited early: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("daemon did not come up")
	return nil, nil
}

func stopDaemon(t *testing.T, c *client.Client, done chan error) []proc.Info {
	t.Helper()
	infos, err := c.Kill()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop after kill")
	}
	return infos
}

func TestDaemonOverItsSocket(t *testing.T) {
	t.Setenv("PROCLY_HOME", t.TempDir())
	c, done := startDaemon(t, false)

	fi, err := os.Stat(paths.Socket())
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket must be owner-only: %v %v", fi.Mode().Perm(), err)
	}
	if pid, _ := os.ReadFile(paths.PidFile()); strings.TrimSpace(string(pid)) == "" {
		t.Fatal("pid file not written")
	}

	cwd := t.TempDir()
	if _, err := c.Start(proc.Spec{Name: "api", Command: "sleep 30", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(proc.Spec{Name: "web", Command: "sleep 30", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	list, err := c.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	if in, err := c.Get("1"); err != nil || in.Name != "web" {
		t.Fatalf("get by id: %+v %v", in, err)
	}
	if infos, err := c.Restart("api"); err != nil || infos[0].Restarts != 1 {
		t.Fatalf("restart: %+v %v", infos, err)
	}
	if infos, err := c.Stop("web"); err != nil || infos[0].Status != proc.StatusStopped {
		t.Fatalf("stop: %+v %v", infos, err)
	}

	_, err = c.Get("ghost")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("unknown process should be a 404 APIError, got %v", err)
	}
	_, err = c.Start(proc.Spec{Name: "api", Command: "sleep 30", Cwd: cwd})
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.Message, "already running") {
		t.Fatalf("duplicate start: %v", err)
	}

	// A second daemon must refuse to start next to the first.
	if err := Run(false); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second daemon: %v", err)
	}

	if n, path, err := c.Save(); err != nil || n != 2 || path != paths.DumpFile() {
		t.Fatalf("save: %d %s %v", n, path, err)
	}
	if _, err := c.Delete("web"); err != nil {
		t.Fatal(err)
	}

	infos := stopDaemon(t, c, done)
	if len(infos) != 1 || infos[0].Status != proc.StatusStopped {
		t.Fatalf("kill should stop what's left: %+v", infos)
	}
	if _, err := os.Stat(paths.Socket()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("socket left behind after shutdown")
	}
	if _, err := os.Stat(paths.PidFile()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pid file left behind after shutdown")
	}
	if _, err := c.Ping(); err == nil {
		t.Fatal("daemon still answering after kill")
	}
}

func TestDaemonResurrectsOnStart(t *testing.T) {
	t.Setenv("PROCLY_HOME", t.TempDir())
	c, done := startDaemon(t, false)
	if _, err := c.Start(proc.Spec{Name: "keep", Command: "sleep 30", Cwd: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Save(); err != nil {
		t.Fatal(err)
	}
	stopDaemon(t, c, done)

	c, _ = startDaemon(t, true)
	in, err := c.Get("keep")
	if err != nil || in.Status != proc.StatusOnline {
		t.Fatalf("after --resurrect: %+v %v", in, err)
	}
}

func TestDaemonReplacesStaleSocket(t *testing.T) {
	t.Setenv("PROCLY_HOME", t.TempDir())
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	// Left over from a crashed daemon: the file exists but nobody listens.
	if err := os.WriteFile(paths.Socket(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ := startDaemon(t, false)
	if _, err := c.Ping(); err != nil {
		t.Fatal(err)
	}
}

func TestClientWithoutDaemon(t *testing.T) {
	t.Setenv("PROCLY_HOME", t.TempDir())
	start := time.Now()
	if _, err := client.New().Ping(); err == nil {
		t.Fatal("ping succeeded with no daemon")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("ping without a daemon should fail fast")
	}
}
