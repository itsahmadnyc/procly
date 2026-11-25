package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// binary is a real procly build, so these tests run the CLI exactly as a
// user does: separate processes, a background daemon, the unix socket.
var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "procly-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "procly")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/procly")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building procly for e2e tests:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type cli struct {
	t    *testing.T
	home string
	dir  string // working directory for commands
}

func newCLI(t *testing.T) *cli {
	t.Helper()
	c := &cli{t: t, home: t.TempDir(), dir: t.TempDir()}
	t.Cleanup(func() { c.run("kill") })
	return c
}

func (c *cli) run(args ...string) (string, int) {
	c.t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = c.dir
	cmd.Env = append(os.Environ(), "PROCLY_HOME="+c.home, "NO_COLOR=1", "PROCLY_SERVER=")
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		c.t.Fatalf("running procly %v: %v", args, err)
	}
	return string(out), code
}

// ok runs a command that must succeed and returns its output.
func (c *cli) ok(args ...string) string {
	c.t.Helper()
	out, code := c.run(args...)
	if code != 0 {
		c.t.Fatalf("procly %s: exit %d\n%s", strings.Join(args, " "), code, out)
	}
	return out
}

// fails runs a command that must fail and returns its output.
func (c *cli) fails(args ...string) string {
	c.t.Helper()
	out, code := c.run(args...)
	if code == 0 {
		c.t.Fatalf("procly %s should fail:\n%s", strings.Join(args, " "), out)
	}
	return out
}

func (c *cli) file(name, body string) {
	c.t.Helper()
	if err := os.WriteFile(filepath.Join(c.dir, name), []byte(body), 0o644); err != nil {
		c.t.Fatal(err)
	}
}

func contains(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("output is missing %q:\n%s", w, out)
		}
	}
}

// row returns the table line for a process (id, then name), so asserts
// don't match another row or a "started <name>" message.
func row(out, name string) string {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) > 1 && f[1] == name && strings.Trim(f[0], "0123456789") == "" {
			return l
		}
	}
	return ""
}

func TestCLILifecycle(t *testing.T) {
	c := newCLI(t)
	c.file("app.sh", "echo started with $FOO\nsleep 30\n")

	out := c.ok("start", "app.sh", "--name", "api", "-e", "FOO=bar")
	contains(t, out, "daemon started", "started api", "bash app.sh")
	if !strings.Contains(row(out, "api"), "online") {
		t.Fatalf("api not online after start:\n%s", out)
	}

	out = c.ok("describe", "api")
	contains(t, out, "command      bash app.sh", "FOO=bar", "autorestart  yes")

	time.Sleep(200 * time.Millisecond)
	contains(t, c.ok("logs", "api", "--no-follow", "--lines", "5"), "api | started with bar")

	out = c.ok("stop", "api")
	contains(t, out, "stopped api")
	if !strings.Contains(row(out, "api"), "stopped") {
		t.Fatalf("not stopped:\n%s", out)
	}

	// `start <name>` brings a stopped process back.
	out = c.ok("start", "api")
	contains(t, out, "started api")
	if !strings.Contains(row(out, "api"), "online") {
		t.Fatalf("not back online:\n%s", out)
	}
	contains(t, c.fails("start", "api"), "already running")

	out = c.ok("restart", "0") // by id
	contains(t, out, "restarted api")

	out = c.ok("delete", "api")
	contains(t, out, "deleted api", "No processes")
}

func TestCLICommandsConfigSaveAndResurrect(t *testing.T) {
	c := newCLI(t)
	c.file("procly.yml", "apps:\n  - name: web\n    command: sleep 30\n  - name: worker\n    command: sleep 31\n")
	c.ok("start", "sleep 32", "--name", "plain")
	out := c.ok("start", "procly.yml")
	contains(t, out, "started web", "started worker", "3 processes", "3 online")

	contains(t, c.ok("save"), "saved 3 processes")
	contains(t, c.ok("kill"), "stopped 3 processes and the daemon")
	contains(t, c.ok("ls"), "Daemon is not running")

	out = c.ok("resurrect")
	contains(t, out, "resurrected 3 processes")
	for _, n := range []string{"plain", "web", "worker"} {
		if !strings.Contains(row(out, n), "online") {
			t.Fatalf("%s not back after resurrect:\n%s", n, out)
		}
	}

	out = c.ok("stop", "all")
	contains(t, out, "stopped plain", "stopped web", "stopped worker", "3 stopped")
}

func TestCLIScriptArgumentsAfterDoubleDash(t *testing.T) {
	c := newCLI(t)
	c.file("echoargs.sh", "echo \"args: $*\"\nsleep 30\n")
	c.ok("start", "echoargs.sh", "--name", "a", "--", "--port", "3000", "two words")
	time.Sleep(200 * time.Millisecond)
	contains(t, c.ok("logs", "a", "--no-follow"), "args: --port 3000 two words")
}

func TestCLIEnvPrefixCommand(t *testing.T) {
	c := newCLI(t)
	c.ok("start", "GREETING=hi sh -c 'echo $GREETING; sleep 30'", "--name", "greet")
	time.Sleep(300 * time.Millisecond)
	if !strings.Contains(row(c.ok("ls"), "greet"), "online") {
		t.Fatal("a VAR=value command should run, not crash")
	}
	contains(t, c.ok("logs", "greet", "--no-follow"), "greet | hi")
}

func TestCLIErrors(t *testing.T) {
	c := newCLI(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"frobnicate"}, `unknown command "frobnicate"`},
		{[]string{"start"}, "usage: procly start"},
		{[]string{"start", "sleep 1", "--port", "3"}, "script arguments go after --"},
		{[]string{"stop", "api"}, "daemon is not running"},
		{[]string{"describe"}, "usage: procly describe"},
		{[]string{"start", "x.js", "--server", "prod-2"}, "start runs on this machine only"},
		{[]string{"ls", "--server", "prod-2"}, `no server named "prod-2"`},
		{[]string{"server", "add", "p", "http://1.2.3.4:4748", "--token", "t", "--fingerprint", strings.Repeat("a", 64)}, "url must look like https://"},
		{[]string{"server", "invite", "local"}, "invalid server name"},
		{[]string{"agent", "connect"}, "hasn't joined a hub yet"},
		{[]string{"web", "--port", "99999"}, "--port must be between"},
	}
	for _, tc := range cases {
		out := c.fails(tc.args...)
		if !strings.Contains(out, tc.want) {
			t.Errorf("procly %s: want %q in\n%s", strings.Join(tc.args, " "), tc.want, out)
		}
	}
}

func TestCLIHelpVersionAndEmptyStates(t *testing.T) {
	c := newCLI(t)
	contains(t, c.ok("help"), "Usage", "procly start", "server invite", "--server")
	contains(t, c.ok("version"), "procly ")
	contains(t, c.ok("ls"), "Daemon is not running")
	contains(t, c.ok("server", "ls"), "No servers yet")
	contains(t, c.ok("kill"), "daemon is not running")
}

func TestCLIServerInviteStoresOnlyAHash(t *testing.T) {
	c := newCLI(t)
	out := c.ok("server", "invite", "office-1", "--hub-address", "hub.example.com:4749")
	contains(t, out, "invited office-1", "procly agent join hub.example.com:4749 --name office-1", "--fingerprint sha256:", "--token ")
	var token string
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "--token" {
			token = f[1]
		}
	}
	if len(token) < 40 {
		t.Fatalf("no token in invite output:\n%s", out)
	}
	data, err := os.ReadFile(filepath.Join(c.home, "servers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) {
		t.Fatal("servers.json contains the raw invite token")
	}
	contains(t, c.ok("server", "ls"), "office-1", "hub not running", "tunnel (dials in)")
	contains(t, c.ok("server", "invite", "office-1"), "re-invited office-1")
	contains(t, c.ok("server", "rm", "office-1"), "removed office-1")
}
