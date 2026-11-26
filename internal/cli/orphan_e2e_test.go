package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pidOf reads a process's pid from a `procly ls` table.
func pidOf(t *testing.T, out, name string) int {
	t.Helper()
	f := strings.Fields(row(out, name)) // id, name, "●", status, pid, ...
	if len(f) < 5 {
		t.Fatalf("no table row for %s:\n%s", name, out)
	}
	pid, err := strconv.Atoi(f[4])
	if err != nil || pid <= 0 {
		t.Fatalf("no pid for %s in %q", name, row(out, name))
	}
	return pid
}

// alive is false for a process that has exited, including one that is only a
// zombie waiting to be reaped (kill -0 still succeeds for those).
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// copies counts running processes whose command line contains pattern.
func copies(pattern string) int {
	out, _ := exec.Command("pgrep", "-f", pattern).Output()
	return len(strings.Fields(string(out)))
}

// signalDaemon sends sig to this test's daemon and waits for it to be gone.
func (c *cli) signalDaemon(sig syscall.Signal) {
	c.t.Helper()
	b, err := os.ReadFile(filepath.Join(c.home, "daemon.pid"))
	if err != nil {
		c.t.Fatal("no daemon pid file: ", err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if err := syscall.Kill(pid, sig); err != nil {
		c.t.Fatal(err)
	}
	waitFor(c.t, "the daemon to exit", func() bool { return !alive(pid) })
}

// twoApps starts web and worker (shell loops), saves the list, and makes sure
// the processes are killed even if the test fails half way.
func twoApps(t *testing.T, c *cli) (web, worker int) {
	t.Helper()
	c.file("orphan-e2e-web.sh", "while true; do echo web; sleep 1; done\n")
	c.file("orphan-e2e-worker.sh", "while true; do echo worker; sleep 1; done\n")
	c.ok("start", "orphan-e2e-web.sh", "--name", "web")
	out := c.ok("start", "orphan-e2e-worker.sh", "--name", "worker")
	c.ok("save")
	web, worker = pidOf(t, out, "web"), pidOf(t, out, "worker")
	t.Cleanup(func() {
		for _, p := range []int{web, worker} {
			syscall.Kill(-p, syscall.SIGKILL)
		}
	})
	return web, worker
}

// The scenario from the daemon study (issue #8): the daemon is killed with
// apps running. They must be seen, never started twice, and stoppable.
func TestCLIOrphansAfterTheDaemonDies(t *testing.T) {
	c := newCLI(t)
	web, worker := twoApps(t, c)

	c.signalDaemon(syscall.SIGKILL)
	if !alive(web) || !alive(worker) {
		t.Fatal("the apps should outlive a daemon that was killed")
	}

	// ls finds them, instead of saying the daemon is not running and showing nothing.
	out := c.ok("ls")
	contains(t, out, "still running from a daemon that died", "left over from a daemon that died", "2 orphaned")
	for name, pid := range map[string]int{"web": web, "worker": worker} {
		r := row(out, name)
		if !strings.Contains(r, "orphaned") || !strings.Contains(r, strconv.Itoa(pid)) {
			t.Fatalf("%s should be listed as orphaned with pid %d: %q", name, pid, r)
		}
	}

	// resurrect must not start second copies.
	contains(t, c.ok("resurrect"), "resurrected 0 processes")
	if n := copies("orphan-e2e-web.sh"); n != 1 {
		t.Fatalf("%d copies of web after resurrect, want 1", n)
	}
	if n := copies("orphan-e2e-worker.sh"); n != 1 {
		t.Fatalf("%d copies of worker after resurrect, want 1", n)
	}

	// Neither start nor restart may touch an orphan.
	contains(t, c.fails("start", "orphan-e2e-web.sh", "--name", "web"), "orphaned", "procly stop web")
	contains(t, c.fails("restart", "web"), "orphaned")
	if n := copies("orphan-e2e-web.sh"); n != 1 {
		t.Fatalf("%d copies of web after the refused start, want 1", n)
	}

	// Stopping an orphan ends the process and forgets it; the other stays.
	out = c.ok("stop", "web")
	contains(t, out, "stopped web")
	waitFor(t, "web to be gone", func() bool { return !alive(web) })
	if row(out, "web") != "" || !alive(worker) {
		t.Fatalf("stopping web must leave worker alone and drop web from the list:\n%s", out)
	}

	// Now resurrect brings web back from the saved list, and still leaves the orphan worker alone.
	out = c.ok("resurrect")
	contains(t, out, "resurrected 1 process")
	if !strings.Contains(row(out, "web"), "online") || !strings.Contains(row(out, "worker"), "orphaned") {
		t.Fatalf("web should be online again and worker still orphaned:\n%s", out)
	}
	if n := copies("orphan-e2e-worker.sh"); n != 1 {
		t.Fatalf("%d copies of worker, want 1", n)
	}

	// kill ends everything, orphans included, and leaves no list behind.
	c.ok("kill")
	waitFor(t, "every process to be gone", func() bool { return copies("orphan-e2e-web.sh") == 0 && !alive(worker) })
	if _, err := os.Stat(filepath.Join(c.home, "running.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("nothing is running, so running.json should be gone: %v", err)
	}
	contains(t, c.ok("ls"), "Daemon is not running")
}

// A daemon that is asked to stop (SIGTERM, systemctl stop) stops its apps,
// and leaves nothing behind that a later daemon could mistake for an orphan.
func TestCLIAGracefulStopLeavesNoOrphans(t *testing.T) {
	c := newCLI(t)
	web, worker := twoApps(t, c)
	waitFor(t, "running.json", func() bool { _, err := os.Stat(filepath.Join(c.home, "running.json")); return err == nil })

	c.signalDaemon(syscall.SIGTERM)
	waitFor(t, "both apps to be gone", func() bool { return !alive(web) && !alive(worker) })
	if _, err := os.Stat(filepath.Join(c.home, "running.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a clean shutdown must remove running.json: %v", err)
	}
	contains(t, c.ok("ls"), "Daemon is not running")
}

// What systemd does after a crash: start `daemon --resurrect` while the old
// apps are still running. The saved list must not duplicate them.
func TestCLIBootResurrectSkipsSurvivors(t *testing.T) {
	c := newCLI(t)
	web, worker := twoApps(t, c)
	c.signalDaemon(syscall.SIGKILL)

	// Started the way the CLI starts a daemon: its log goes to daemon.log.
	logf, err := os.OpenFile(filepath.Join(c.home, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	d := exec.Command(binary, "daemon", "--resurrect")
	d.Dir = c.dir
	d.Env = append(os.Environ(), "PROCLY_HOME="+c.home, "NO_COLOR=1", "PROCLY_SERVER=")
	d.Stdout, d.Stderr = logf, logf
	if err := d.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Process.Kill(); d.Wait() })
	// The first daemon logged "listening" once; ours is up when there are two lines.
	// (The socket path is not a good signal: a long PROCLY_HOME moves it to a hashed temp file.)
	waitFor(t, "the new daemon to listen", func() bool {
		b, _ := os.ReadFile(filepath.Join(c.home, "daemon.log"))
		return strings.Count(string(b), "daemon listening") >= 2
	})

	out := c.ok("ls")
	for name, pid := range map[string]int{"web": web, "worker": worker} {
		r := row(out, name)
		if !strings.Contains(r, "orphaned") || !strings.Contains(r, strconv.Itoa(pid)) {
			t.Fatalf("after `daemon --resurrect`, %s should be the old orphan with pid %d: %q\n%s", name, pid, r, out)
		}
	}
	if copies("orphan-e2e-web.sh") != 1 || copies("orphan-e2e-worker.sh") != 1 {
		t.Fatalf("the boot-time resurrect started second copies: web=%d worker=%d", copies("orphan-e2e-web.sh"), copies("orphan-e2e-worker.sh"))
	}
}
