package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

// runWith is run with extra environment variables and standard input.
func (c *cli) runWith(env []string, stdin string, args ...string) (string, int) {
	c.t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = c.dir
	cmd.Env = append(append(os.Environ(), "PROCLY_HOME="+c.home, "NO_COLOR=1", "PROCLY_SERVER="), env...)
	cmd.Stdin = strings.NewReader(stdin)
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

func TestCLIHealthCheckRestartsAndShows(t *testing.T) {
	c := newCLI(t)
	// Port 1 never accepts connections, so every check fails.
	c.file("procly.yml", `apps:
  - name: api
    command: sleep 30
    health: tcp://127.0.0.1:1
    health_grace: 0.2
    health_interval: 0.2
    health_retries: 2
    log_max_size: 5MB
    log_keep: 2
`)
	c.ok("start", "procly.yml")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := c.ok("describe", "api")
		if strings.Contains(out, "restarts     ") && !strings.Contains(out, "restarts     0") {
			contains(t, out, "tcp://127.0.0.1:1", "log rotation at 5.0 MB, keep 2")
			contains(t, c.ok("ls"), "health")
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("a failing health check never restarted the process:\n%s", c.ok("describe", "api"))
}

func TestCLIAlertsCommand(t *testing.T) {
	c := newCLI(t)
	contains(t, c.ok("alerts"), "No alerts set up", "procly alerts telegram")
	contains(t, c.fails("alerts", "telegram"), "--token", "@BotFather")
	contains(t, c.fails("alerts", "slack", "--webhook", "http://insecure.example.com/x"), "https://hooks.slack.com")
	contains(t, c.fails("alerts", "test"), "no alerts set up")
	// A token that can't send a test message is not saved.
	out := c.fails("alerts", "slack", "--webhook", "https://127.0.0.1:1/hook")
	contains(t, out, "Nothing was saved")
	contains(t, c.ok("alerts"), "No alerts set up")
	contains(t, c.ok("alerts", "off"), "turned off all alerts")
}

func TestCLIEnvFile(t *testing.T) {
	c := newCLI(t)
	c.file(".env", "# settings\nGREETING=hello from the file\nexport SECRET_TOKEN=\"s3cr3t value\"\nSHARED=file\n")
	c.file("show.sh", "echo \"$GREETING|$SECRET_TOKEN|$SHARED\" > seen.txt\nsleep 30\n")
	c.ok("start", "show.sh", "--name", "show", "--env-file", ".env", "-e", "SHARED=flag")

	seen := filepath.Join(c.dir, "seen.txt")
	waitFor(t, "seen.txt", func() bool { b, err := os.ReadFile(seen); return err == nil && strings.Contains(string(b), "|") })
	if b, _ := os.ReadFile(seen); strings.TrimSpace(string(b)) != "hello from the file|s3cr3t value|flag" {
		t.Fatalf("the process saw %q: file values, with -e winning over the file", b)
	}

	// describe shows the file and the names, never the values.
	out := c.ok("describe", "show")
	contains(t, out, "env_file", ".env", "GREETING", "SECRET_TOKEN", "values not shown")
	if strings.Contains(out, "s3cr3t value") || strings.Contains(out, "hello from the file") {
		t.Errorf("describe must not print the file's values:\n%s", out)
	}

	// Edit the file and restart: the change applies.
	c.file(".env", "GREETING=changed\n")
	os.Remove(seen)
	c.ok("restart", "show")
	waitFor(t, "seen.txt again", func() bool { b, err := os.ReadFile(seen); return err == nil && strings.Contains(string(b), "|") })
	if b, _ := os.ReadFile(seen); strings.TrimSpace(string(b)) != "changed||flag" {
		t.Errorf("after editing the file and restarting the process saw %q", b)
	}

	// Problems are reported at start, with the file and the line.
	c.file("bad.env", "OK=1\nnot valid\n")
	contains(t, c.fails("start", "show.sh", "--name", "broken", "--env-file", "bad.env"), "bad.env:2:")
	contains(t, c.fails("start", "show.sh", "--name", "gone", "--env-file", "nope.env"), "env_file", "nope.env")
}

func TestCLIEnvFileInConfig(t *testing.T) {
	c := newCLI(t)
	if err := os.MkdirAll(filepath.Join(c.dir, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	c.file("conf/app.env", "WHO=config\n")
	c.file("conf/run.sh", "echo \"$WHO\" > ../who.txt\nsleep 30\n")
	c.file("conf/procly.yml", "apps:\n  - name: viaconfig\n    command: sh run.sh\n    cwd: .\n    env_file: app.env\n")
	c.ok("start", "conf/procly.yml")
	who := filepath.Join(c.dir, "who.txt")
	waitFor(t, "who.txt", func() bool { b, err := os.ReadFile(who); return err == nil && strings.Contains(string(b), "config") })
}

func TestCLIJSONOutput(t *testing.T) {
	c := newCLI(t)

	// Without a daemon there are no processes: an empty array, exit 0, nothing else on stdout.
	out := c.ok("ls", "--json")
	var none []map[string]any
	if err := json.Unmarshal([]byte(out), &none); err != nil || len(none) != 0 {
		t.Fatalf("ls --json with no daemon should print []: %q (%v)", out, err)
	}

	c.file(".env", "FROM_FILE=filevalue\n")
	c.ok("start", "sleep 30", "--name", "alpha", "-e", "SECRET=topsecret", "--env-file", ".env")
	c.ok("start", "sleep 31", "--name", "beta", "--no-autorestart")

	out = c.ok("ls", "--json")
	if strings.Contains(out, "\x1b[") {
		t.Errorf("no colors in JSON: %q", out)
	}
	var views []struct {
		ID          int      `json:"id"`
		Name        string   `json:"name"`
		Status      string   `json:"status"`
		PID         int      `json:"pid"`
		Command     string   `json:"command"`
		AutoRestart bool     `json:"autorestart"`
		EnvKeys     []string `json:"env_keys"`
	}
	if err := json.Unmarshal([]byte(out), &views); err != nil {
		t.Fatalf("ls --json is not one JSON array: %v\n%s", err, out)
	}
	if len(views) != 2 || views[0].Name != "alpha" || views[1].Name != "beta" {
		t.Fatalf("views = %+v", views)
	}
	if views[0].Status != "online" || views[0].PID <= 0 || views[0].Command != "sleep 30" || !views[0].AutoRestart || views[1].AutoRestart {
		t.Errorf("fields: %+v", views)
	}
	if got := strings.Join(views[0].EnvKeys, ","); got != "FROM_FILE,SECRET" {
		t.Errorf("env names = %q", got)
	}
	if strings.Contains(out, "topsecret") || strings.Contains(out, "filevalue") {
		t.Errorf("values must never be in the JSON:\n%s", out)
	}

	// describe: one object, by name or id, and a clear failure for an unknown one.
	var one struct {
		Name string `json:"name"`
		ID   int    `json:"id"`
	}
	if err := json.Unmarshal([]byte(c.ok("describe", "beta", "--json")), &one); err != nil || one.Name != "beta" {
		t.Errorf("describe --json by name: %+v (%v)", one, err)
	}
	if err := json.Unmarshal([]byte(c.ok("describe", fmt.Sprint(views[0].ID), "--json")), &one); err != nil || one.Name != "alpha" {
		t.Errorf("describe --json by id: %+v (%v)", one, err)
	}
	c.fails("describe", "nope", "--json")

	// The table output is unchanged without the flag.
	contains(t, c.ok("ls"), "alpha", "beta", "online")
}

// syncBuffer lets a test read what a background process has printed so far.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestCLIJSONFromAnotherServer(t *testing.T) {
	far, here := newCLI(t), newCLI(t)
	far.ok("start", "sleep 30", "--name", "far-app")

	addr, token, fp := startAgent(t, far)
	here.ok("server", "add", "far", "https://"+addr, "--token", token, "--fingerprint", fp)

	var views []struct{ Name, Status string }
	if err := json.Unmarshal([]byte(here.ok("ls", "--json", "--server", "far")), &views); err != nil || len(views) != 1 || views[0].Name != "far-app" || views[0].Status != "online" {
		t.Errorf("ls --json --server: %+v (%v)", views, err)
	}
	var one struct{ Name string }
	if err := json.Unmarshal([]byte(here.ok("describe", "far-app", "--json", "-S", "far")), &one); err != nil || one.Name != "far-app" {
		t.Errorf("describe --json -S: %+v (%v)", one, err)
	}
	here.fails("describe", "nope", "--json", "-S", "far")
}

// freeAddr returns a loopback address that is free right now.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// startBackground runs procly with args in c's home until the test ends and
// returns what it prints.
func startBackground(t *testing.T, c *cli, args ...string) *syncBuffer {
	t.Helper()
	out := &syncBuffer{}
	cmd := exec.Command(binary, args...)
	cmd.Dir = c.dir
	cmd.Env = append(os.Environ(), "PROCLY_HOME="+c.home, "NO_COLOR=1", "PROCLY_SERVER=")
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	return out
}

// tokenAndFingerprint reads them from what an agent or invite printed: the
// token on the "--token <t>" line, the fingerprint as sha256:<64 hex>.
func tokenAndFingerprint(t *testing.T, text string) (token, fp string) {
	t.Helper()
	for _, l := range strings.Split(text, "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "--token" {
			token = f[1]
		}
	}
	fp = regexp.MustCompile(`sha256:[0-9a-f]{64}`).FindString(text)
	if token == "" || fp == "" {
		t.Fatalf("no token or fingerprint in:\n%s", text)
	}
	return token, fp
}

// startAgent runs `procly agent` for c and returns its address, token and fingerprint.
func startAgent(t *testing.T, c *cli) (addr, token, fp string) {
	t.Helper()
	addr = freeAddr(t)
	out := startBackground(t, c, "agent", "--addr", addr)
	waitFor(t, "the agent to print its token", func() bool { return strings.Contains(out.String(), "--token") })
	token, fp = tokenAndFingerprint(t, out.String())
	return addr, token, fp
}

func TestCLISecretsWithoutTheCommandLine(t *testing.T) {
	// server add: token from the environment, then from standard input.
	far := newCLI(t)
	far.ok("start", "sleep 30", "--name", "far-app")
	addr, token, fp := startAgent(t, far)

	here := newCLI(t)
	out, code := here.runWith([]string{"PROCLY_TOKEN=" + token}, "", "server", "add", "viaenv", "https://"+addr, "--fingerprint", fp)
	if code != 0 || !strings.Contains(out, "added") {
		t.Fatalf("server add with PROCLY_TOKEN: exit %d\n%s", code, out)
	}
	out, code = here.runWith(nil, token+"\n", "server", "add", "viastdin", "https://"+addr, "--token", "-", "--fingerprint", fp)
	if code != 0 || !strings.Contains(out, "added") {
		t.Fatalf("server add with --token -: exit %d\n%s", code, out)
	}
	contains(t, here.ok("server", "ls"), "viaenv", "viastdin")
	// A wrong secret from the environment is rejected like a wrong flag.
	out, code = here.runWith([]string{"PROCLY_TOKEN=wrongwrongwrongwrongwrongwrongwrong1"}, "", "server", "add", "bad", "https://"+addr, "--fingerprint", fp)
	if code == 0 || !strings.Contains(out, "rejected the token") {
		t.Errorf("a wrong token from the environment: exit %d\n%s", code, out)
	}

	// agent join: the same two ways, against a hub.
	hub := newCLI(t)
	hubAddr := freeAddr(t)
	startBackground(t, hub, "hub", "--addr", hubAddr)
	waitFor(t, "the hub to listen", func() bool {
		c, err := net.DialTimeout("tcp", hubAddr, 200*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	for _, how := range []string{"env", "stdin"} {
		invite := hub.ok("server", "invite", "from-"+how, "--hub-address", hubAddr)
		tok, hfp := tokenAndFingerprint(t, invite)
		edge := newCLI(t)
		var env []string
		var stdin string
		argv := []string{"agent", "join", hubAddr, "--name", "from-" + how, "--fingerprint", hfp}
		if how == "env" {
			env = []string{"PROCLY_TOKEN=" + tok}
		} else {
			stdin = tok + "\n"
			argv = append(argv, "--token", "-")
		}
		out, code := edge.runWith(env, stdin, argv...)
		if code != 0 || !strings.Contains(out, "joined") {
			t.Errorf("agent join via %s: exit %d\n%s", how, code, out)
		}
	}

	// alerts: nothing given says where the secret can come from (no network is used).
	c := newCLI(t)
	contains(t, c.fails("alerts", "telegram", "--chat", "1"), "PROCLY_TELEGRAM_TOKEN", "--token -")
	contains(t, c.fails("alerts", "slack"), "PROCLY_SLACK_WEBHOOK", "--webhook -")
	out, code = c.runWith(nil, "", "alerts", "telegram", "--token", "-", "--chat", "1")
	if code == 0 || !strings.Contains(out, "nothing on standard input") {
		t.Errorf("--token - with an empty stdin: exit %d\n%s", code, out)
	}
	out, code = c.runWith(nil, "http://not-https.example\n", "alerts", "slack", "--webhook", "-")
	if code == 0 || !strings.Contains(out, "https") {
		t.Errorf("a webhook read from stdin is still checked: exit %d\n%s", code, out)
	}
}
