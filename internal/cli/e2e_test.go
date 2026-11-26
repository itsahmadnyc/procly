package cli

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	out, _ := startBackgroundCmd(t, c, args...)
	return out
}

// startBackgroundCmd is startBackground that also returns the process, to send it signals.
func startBackgroundCmd(t *testing.T, c *cli, args ...string) (*syncBuffer, *exec.Cmd) {
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
	return out, cmd
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

func TestCLIReloadAndSignals(t *testing.T) {
	c := newCLI(t)
	c.file("server.sh", "trap 'echo reloaded >> signals.txt' HUP\ntrap 'echo stopped-by-int >> signals.txt; exit 0' INT\ntrap 'echo stopped-by-term >> signals.txt; exit 0' TERM\nwhile true; do sleep 0.1; done\n")
	c.file("procly.yml", "apps:\n  - name: web\n    command: bash server.sh\n    reload_signal: SIGHUP\n    stop_signal: SIGINT\n  - name: plain\n    command: sleep 30\n")
	c.ok("start", "procly.yml")
	time.Sleep(500 * time.Millisecond)
	seen := filepath.Join(c.dir, "signals.txt")
	fileHas := func(want ...string) bool {
		b, _ := os.ReadFile(seen)
		return strings.Join(strings.Fields(string(b)), ",") == strings.Join(want, ",")
	}

	out := c.ok("reload", "web")
	contains(t, out, "reloaded web")
	waitFor(t, "the reload to arrive", func() bool { return fileHas("reloaded") })

	// The process kept running, and nothing counted as a restart.
	out = c.ok("ls")
	if r := row(out, "web"); !strings.Contains(r, "online") || strings.Fields(r)[6] != "0" {
		t.Errorf("web after a reload: %q", r)
	}
	contains(t, c.ok("describe", "web"), "reload signal", "SIGHUP", "stop signal", "SIGINT")
	var v struct {
		Stop   string `json:"stop_signal"`
		Reload string `json:"reload_signal"`
	}
	if err := json.Unmarshal([]byte(c.ok("describe", "web", "--json")), &v); err != nil || v.Stop != "SIGINT" || v.Reload != "SIGHUP" {
		t.Errorf("describe --json: %+v (%v)", v, err)
	}

	// Without a reload_signal it says what to do; "all" skips the ones that cannot.
	contains(t, c.fails("reload", "plain"), "no reload_signal", "plain")
	contains(t, c.ok("reload", "all"), "reloaded web")
	waitFor(t, "the second reload", func() bool { return fileHas("reloaded", "reloaded") })

	// stop uses the configured signal.
	c.ok("stop", "web")
	waitFor(t, "stop_signal to arrive", func() bool { return fileHas("reloaded", "reloaded", "stopped-by-int") })

	// Managed processes get the default SIGHUP, not an ignored one (the daemon
	// catches it instead of ignoring it), and the daemon itself survives a hangup.
	b, err := os.ReadFile(filepath.Join(c.home, "daemon.pid"))
	if err != nil {
		t.Fatal(err)
	}
	dpid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if err := syscall.Kill(dpid, syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if !alive(dpid) {
		t.Error("the daemon must survive SIGHUP (a closing terminal)")
	}
	contains(t, c.ok("ls"), "plain")

	// A bad name is refused at start, naming the setting.
	c.file("bad.yml", "apps:\n  - name: bad\n    command: sleep 1\n    reload_signal: SIGNOPE\n")
	contains(t, c.fails("start", "bad.yml"), "reload_signal", "SIGNOPE")
	// And the flags work.
	c.ok("start", "sleep 31", "--name", "flagged", "--reload-signal", "usr1", "--stop-signal", "HUP")
	contains(t, c.ok("describe", "flagged"), "SIGUSR1", "SIGHUP")
}

func TestCLIScheduledRestartSettings(t *testing.T) {
	c := newCLI(t)
	// Mistakes are refused when the process starts, naming the setting.
	for _, bad := range []struct{ flag, value, want string }{
		{"--restart-every", "24", "with a unit"},
		{"--restart-every", "10s", "too short"},
		{"--restart-at", "25:00", "time of day"},
		{"--restart-at", "4pm", "time of day"},
	} {
		out := c.fails("start", "sleep 30", "--name", "bad", bad.flag, bad.value)
		contains(t, out, strings.ReplaceAll(strings.TrimPrefix(bad.flag, "--"), "-", "_"), bad.want)
	}
	c.file("procly.yml", "apps:\n  - name: nightly\n    command: sleep 30\n    restart_every: 24h\n    restart_at: \"04:00\"\n")
	c.ok("start", "procly.yml")
	out := c.ok("describe", "nightly")
	contains(t, out, "restart every", "24h", "restart at", "04:00", "server's local time")

	var v struct {
		Every string `json:"restart_every"`
		At    string `json:"restart_at"`
	}
	if err := json.Unmarshal([]byte(c.ok("describe", "nightly", "--json")), &v); err != nil || v.Every != "24h" || v.At != "04:00" {
		t.Errorf("describe --json: %+v (%v)", v, err)
	}
	// The flags work too, and the schedule is kept by save and resurrect.
	c.ok("start", "sleep 31", "--name", "byflag", "--restart-at", "03:30")
	contains(t, c.ok("describe", "byflag"), "restart at", "03:30")
	c.ok("save")
	c.ok("kill")
	c.ok("resurrect")
	contains(t, c.ok("describe", "nightly"), "24h", "04:00")
	contains(t, c.ok("describe", "byflag"), "03:30")
}

// loginStatus signs in to a panel at addr as a reverse proxy would, claiming
// the visitor is forwardedFor.
func loginStatus(t *testing.T, addr, password, forwardedFor string) int {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"password": password})
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/api/login", bytes.NewReader(body))
	req.Header.Set("X-Procly", "1")
	req.Header.Set("Content-Type", "application/json")
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func panelPassword(t *testing.T, out *syncBuffer) string {
	t.Helper()
	waitFor(t, "the panel to print its password", func() bool { return strings.Contains(out.String(), "password  ") })
	for _, l := range strings.Split(out.String(), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[0] == "password" {
			return f[1]
		}
	}
	t.Fatalf("no password in:\n%s", out.String())
	return ""
}

func TestCLIWebTrustedProxy(t *testing.T) {
	// Without the flag, every visitor behind a proxy shares the proxy's address.
	c := newCLI(t)
	addr := freeAddr(t)
	out := startBackground(t, c, "web", "--addr", addr)
	pw := panelPassword(t, out)
	for i := 0; i < 5; i++ {
		loginStatus(t, addr, "nope", "198.51.100.1")
	}
	if got := loginStatus(t, addr, pw, "198.51.100.2"); got != http.StatusTooManyRequests {
		t.Errorf("without --trusted-proxy one visitor's typos lock everyone out: %d", got)
	}

	// With the flag, the lockout follows the visitor the proxy reports.
	c2 := newCLI(t)
	addr2 := freeAddr(t)
	out2 := startBackground(t, c2, "web", "--addr", addr2, "--trusted-proxy", "127.0.0.1")
	pw2 := panelPassword(t, out2)
	contains(t, out2.String(), "Trusting X-Forwarded-For from 127.0.0.1/32")
	for i := 0; i < 5; i++ {
		loginStatus(t, addr2, "nope", "9.9.9.9, 198.51.100.1")
	}
	if got := loginStatus(t, addr2, pw2, "198.51.100.1"); got != http.StatusTooManyRequests {
		t.Errorf("the visitor who mistyped is locked out: %d", got)
	}
	if got := loginStatus(t, addr2, pw2, "198.51.100.2"); got != http.StatusOK {
		t.Errorf("another visitor is not: %d", got)
	}

	// Mistakes are refused at start, and trusting everything is called out.
	contains(t, c.fails("web", "--port", "1", "--trusted-proxy", "nonsense"), "--trusted-proxy", "nonsense")
	c3 := newCLI(t)
	out3 := startBackground(t, c3, "web", "--addr", freeAddr(t), "--trusted-proxy", "0.0.0.0/0")
	waitFor(t, "the warning", func() bool { return strings.Contains(out3.String(), "that is every address") })
}

// testCert writes a self-signed certificate for 127.0.0.1 with the given serial
// number to dir and returns the files and the certificate pool that trusts it.
func testCert(t *testing.T, dir string, serial int64) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "procly e2e"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return certFile, keyFile, pool
}

func servedSerial(addr string) (int64, error) {
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].SerialNumber.Int64(), nil
}

func TestCLIWebTLS(t *testing.T) {
	c := newCLI(t)
	certFile, keyFile, pool := testCert(t, c.dir, 1)
	addr := freeAddr(t)
	out, cmd := startBackgroundCmd(t, c, "web", "--addr", addr, "--tls-cert", certFile, "--tls-key", keyFile)
	pw := panelPassword(t, out)
	contains(t, out.String(), "https://"+addr)

	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	res, err := client.Get("https://" + addr + "/")
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("GET over TLS: %v %v", res, err)
	}
	res.Body.Close()
	if r, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/"); err == nil && r.StatusCode == http.StatusOK {
		t.Error("plain HTTP must not be served on a TLS port")
	}

	body, _ := json.Marshal(map[string]string{"password": pw})
	req, _ := http.NewRequest(http.MethodPost, "https://"+addr+"/api/login", bytes.NewReader(body))
	req.Header.Set("X-Procly", "1")
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	secure := false
	for _, ck := range res.Cookies() {
		secure = secure || ck.Secure
	}
	if res.StatusCode != http.StatusOK || !secure {
		t.Errorf("login over TLS: %d, Secure cookie: %v", res.StatusCode, secure)
	}

	// A renewed certificate is used after SIGHUP, without restarting the panel.
	if got, _ := servedSerial(addr); got != 1 {
		t.Fatalf("serial = %d", got)
	}
	testCert(t, c.dir, 2)
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the new certificate after SIGHUP", func() bool { got, _ := servedSerial(addr); return got == 2 })
	contains(t, out.String(), "loaded the certificate again (SIGHUP)")

	// A broken certificate is refused; the good one stays.
	os.WriteFile(certFile, []byte("broken"), 0o600)
	cmd.Process.Signal(syscall.SIGHUP)
	waitFor(t, "the refusal", func() bool { return strings.Contains(out.String(), "still serving the previous certificate") })
	if got, _ := servedSerial(addr); got != 2 {
		t.Errorf("the last good certificate must stay: %d", got)
	}

	// Mistakes stop the start with a message.
	contains(t, c.fails("web", "--port", "1", "--tls-cert", certFile), "--tls-cert and --tls-key go together")
	contains(t, c.fails("web", "--port", "1", "--tls-cert", filepath.Join(c.dir, "no.pem"), "--tls-key", keyFile), "no.pem")
}

func TestCLIInstances(t *testing.T) {
	c := newCLI(t)
	// Each instance writes what it was told and derives a "port" the way the docs show.
	c.file("worker.sh", "echo \"$PROCLY_INSTANCE of $PROCLY_INSTANCES\" > \"seen-$PROCLY_INSTANCE.txt\"\nsleep 30\n")
	out := c.ok("start", "worker.sh", "--name", "queue", "--instances", "3")
	contains(t, out, "started queue-1", "started queue-2", "started queue-3")

	for i := 0; i < 3; i++ {
		f := filepath.Join(c.dir, "seen-"+strconv.Itoa(i)+".txt")
		want := strconv.Itoa(i) + " of 3"
		waitFor(t, f, func() bool { b, err := os.ReadFile(f); return err == nil && strings.TrimSpace(string(b)) == want })
	}
	c.ok("start", "sh -c 'echo $((3000 + PROCLY_INSTANCE)) > port-$PROCLY_INSTANCE.txt; sleep 30'", "--name", "calc", "--instances", "2")
	for i, want := range []string{"3000", "3001"} {
		f := filepath.Join(c.dir, "port-"+strconv.Itoa(i)+".txt")
		waitFor(t, f, func() bool { b, err := os.ReadFile(f); return err == nil && strings.TrimSpace(string(b)) == want })
	}

	// ls shows them grouped: a summary line, and the instances under it.
	list := c.ok("ls")
	contains(t, list, "queue ×3", "3 online", "queue-1", "queue-2", "queue-3", "calc ×2")
	li := strings.Index(list, "queue ×3")
	if strings.Index(list, "queue-1") < li || strings.Index(list, "queue-3") < strings.Index(list, "queue-1") {
		t.Errorf("the instances should follow their group, in order:\n%s", list)
	}

	// JSON stays flat, with the group fields for whoever wants to group them.
	var views []struct {
		Name      string `json:"name"`
		Group     string `json:"group"`
		Index     int    `json:"index"`
		Instances int    `json:"instances"`
	}
	if err := json.Unmarshal([]byte(c.ok("ls", "--json")), &views); err != nil || len(views) != 5 {
		t.Fatalf("ls --json: %d views, %v", len(views), err)
	}
	if v := views[1]; v.Name != "queue-2" || v.Group != "queue" || v.Index != 1 || v.Instances != 3 {
		t.Errorf("queue-2 in JSON: %+v", v)
	}

	// describe: an instance says where it belongs; the group name says what to do.
	contains(t, c.ok("describe", "queue-2"), "group", "queue, instance 2 of 3", "PROCLY_INSTANCE=1")
	contains(t, c.fails("describe", "queue"), "group of 3 instances", "queue-1")

	// Names that are taken are refused up front, without starting part of the group.
	contains(t, c.fails("start", "worker.sh", "--name", "queue", "--instances", "2"), "queue-1 already exists")
	c.ok("start", "sleep 31", "--name", "solo")
	contains(t, c.fails("start", "sleep 32", "--name", "solo", "--instances", "2"), "solo is already a process")

	// stop, restart and delete with the base name act on all of them; the others are untouched.
	out = c.ok("stop", "queue")
	contains(t, out, "stopped queue-1", "stopped queue-2", "stopped queue-3")
	contains(t, c.ok("ls"), "3 stopped", "2 online")
	contains(t, c.fails("start", "queue", "--instances", "9"), "queue-1 already exists") // a group that exists is not started a second time
	out = c.ok("start", "queue")                                                         // starts the stopped group again
	contains(t, out, "started queue-1", "started queue-2", "started queue-3")
	contains(t, c.fails("start", "queue"), "already running")
	out = c.ok("restart", "queue")
	contains(t, out, "restarted queue-1", "restarted queue-3")

	// save and resurrect keep the group.
	c.ok("save")
	c.ok("kill")
	c.ok("resurrect")
	list = c.ok("ls")
	contains(t, list, "queue ×3", "calc ×2", "solo")
	contains(t, c.ok("delete", "queue"), "deleted queue-1", "deleted queue-3")
	after := c.ok("ls")
	if strings.Contains(after, "queue") || !strings.Contains(after, "calc ×2") {
		t.Errorf("deleting the group removes only the group:\n%s", after)
	}
}

func TestCLIInstancesInAConfigFile(t *testing.T) {
	c := newCLI(t)
	c.file("procly.yml", "apps:\n  - name: api\n    command: sleep 30\n  - name: queue\n    command: sleep 31\n    instances: 2\n")
	out := c.ok("start", "procly.yml")
	contains(t, out, "started api", "started queue-1", "started queue-2")
	contains(t, c.ok("ls"), "queue ×2")
	c.file("bad.yml", "apps:\n  - name: q\n    command: sleep 1\n    instances: 1000\n")
	contains(t, c.fails("start", "bad.yml"), "instances must be between")
	contains(t, c.fails("start", "sleep 1", "--name", "x", "--instances", "0"), "--instances must be a number")
	contains(t, c.fails("start", "sleep 1", "--name", "x", "--instances", "abc"), "--instances must be a number")
}
