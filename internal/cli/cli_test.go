package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/itsahmadnyc/procly/internal/proc"
)

var startFlags = flagSet{
	valued: set("name", "cwd", "env", "max-restarts"),
	bools:  set("no-autorestart"),
	alias:  map[string]string{"n": "name", "e": "env"},
}

func TestParse(t *testing.T) {
	a, err := parse([]string{"app.js", "--name", "api", "-e", "A=1", "--env=B=2", "--no-autorestart", "--", "--port", "3000"}, startFlags)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(a.pos, ",") != "app.js" || a.get("name") != "api" || !a.bools["no-autorestart"] {
		t.Fatalf("parsed %+v", a)
	}
	if strings.Join(a.values["env"], ",") != "A=1,B=2" {
		t.Fatalf("repeatable flag: %v", a.values["env"])
	}
	if strings.Join(a.rest, " ") != "--port 3000" {
		t.Fatalf("args after --: %v", a.rest)
	}

	for _, bad := range [][]string{{"app.js", "--port", "3000"}, {"--name"}} {
		if _, err := parse(bad, startFlags); err == nil {
			t.Errorf("%v should fail", bad)
		}
	}
	if _, err := parse([]string{"app.js", "--port"}, startFlags); err == nil || !strings.Contains(err.Error(), "after --") {
		t.Errorf("unknown flag should point to --: %v", err)
	}
}

func specFor(t *testing.T, cwd string, argv ...string) proc.Spec {
	t.Helper()
	// --cwd goes first: anything after "--" belongs to the script.
	a, err := parse(append([]string{"--cwd", cwd}, argv...), startFlags)
	if err != nil {
		t.Fatal(err)
	}
	s, err := buildSpec(a)
	if err != nil {
		t.Fatalf("buildSpec(%v): %v", argv, err)
	}
	return s
}

func TestBuildSpecFromFiles(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"server.js", "worker.py", "job.sh", "my app.js", "app.mjs", "app.cjs", "UPPER.MJS", "page.TS"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "bin-tool"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "tool"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		argv          []string
		command, name string
	}{
		{[]string{"server.js"}, "node server.js", "server"},
		{[]string{"worker.py"}, "python3 worker.py", "worker"},
		{[]string{"job.sh"}, "bash job.sh", "job"},
		{[]string{"bin-tool"}, "./bin-tool", "bin-tool"},
		{[]string{"my app.js"}, "node 'my app.js'", "my-app"},
		{[]string{"app.mjs"}, "node app.mjs", "app"},
		{[]string{"app.cjs"}, "node app.cjs", "app"},
		{[]string{"UPPER.MJS"}, "node UPPER.MJS", "UPPER"}, // extensions are matched in lower case
		{[]string{"page.TS"}, "npx tsx page.TS", "page"},
		{[]string{"sub/tool"}, "sub/tool", "tool"}, // an executable in a folder is run as it is, not found on PATH
		{[]string{"server.js", "--name", "api", "--", "--port", "3000", "it's"}, `node server.js --port 3000 'it'\''s'`, "api"},
	}
	for _, c := range cases {
		s := specFor(t, dir, c.argv...)
		if s.Command != c.command || s.Name != c.name || s.Cwd != dir {
			t.Errorf("%v: got command %q name %q cwd %q", c.argv, s.Command, s.Name, s.Cwd)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A file that cannot be run says what to type, using its own name and, for a
	// few types, the right tool.
	for file, want := range map[string]string{
		"notes.txt": `procly start "<command> notes.txt"`,
		"main.go":   `procly start "go run main.go"`,
		"app.jar":   `procly start "java -jar app.jar"`,
	} {
		if err := os.WriteFile(filepath.Join(dir, file), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		a, _ := parse([]string{file, "--cwd", dir}, startFlags)
		if _, err := buildSpec(a); err == nil || !strings.Contains(err.Error(), "don't know how to run "+file) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error %v, want it to contain %q", file, err, want)
		}
	}
}

func TestBuildSpecFromCommands(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		argv          []string
		command, name string
	}{
		{[]string{"npm run start"}, "npm run start", "start"},
		{[]string{"python3 -m http.server"}, "python3 -m http.server", "python3"},
		{[]string{"/usr/local/bin/caddy run"}, "/usr/local/bin/caddy run", "caddy"},
		{[]string{"redis-server", "--", "--port", "6380"}, "redis-server --port 6380", "redis-server"},
	}
	for _, c := range cases {
		s := specFor(t, dir, c.argv...)
		if s.Command != c.command || s.Name != c.name {
			t.Errorf("%v: got %q / %q", c.argv, s.Command, s.Name)
		}
	}
}

func TestBuildSpecOptions(t *testing.T) {
	dir := t.TempDir()
	s := specFor(t, dir, "sleep 1", "-e", "A=1", "-e", "B=x=y", "--no-autorestart", "--max-restarts", "4")
	if s.Env["A"] != "1" || s.Env["B"] != "x=y" || s.ShouldAutoRestart() || s.MaxRestarts != 4 {
		t.Fatalf("options: %+v", s)
	}
	for _, bad := range [][]string{{"sleep 1", "-e", "NOEQUALS"}, {"sleep 1", "-e", "=v"}, {"sleep 1", "--max-restarts", "-2"}, {"sleep 1", "--max-restarts", "x"}} {
		a, _ := parse(append(bad, "--cwd", dir), startFlags)
		if _, err := buildSpec(a); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestInheritPath(t *testing.T) {
	t.Setenv("PATH", "/custom/bin:/usr/bin")
	s := proc.Spec{}
	inheritPath(&s)
	if s.Env["PATH"] != "/custom/bin:/usr/bin" {
		t.Fatalf("PATH not captured: %v", s.Env)
	}
	own := proc.Spec{Env: map[string]string{"PATH": "/mine"}}
	inheritPath(&own)
	if own.Env["PATH"] != "/mine" {
		t.Fatal("an explicit PATH was overwritten")
	}
}

func TestSanitizeAndQuote(t *testing.T) {
	for in, want := range map[string]string{"my app": "my-app", "__x__": "x", "all": "app", "": "app", "a/b\\c": "a-b-c", strings.Repeat("n", 80): strings.Repeat("n", 64)} {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"plain": "plain", "a b": "'a b'", "it's": `'it'\''s'`, "$HOME": "'$HOME'", "": "''"} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatting(t *testing.T) {
	for d, want := range map[time.Duration]string{
		5 * time.Second: "5s", 3 * time.Minute: "3m", 90 * time.Minute: "1h 30m", 50 * time.Hour: "2d 2h",
	} {
		if got := fmtDuration(d); got != want {
			t.Errorf("fmtDuration(%s) = %q, want %q", d, got, want)
		}
	}
	for b, want := range map[int64]string{512: "512 B", 2048: "2.0 KB", 47 << 20: "47.0 MB", 3 << 30: "3.00 GB"} {
		if got := fmtBytes(b); got != want {
			t.Errorf("fmtBytes(%d) = %q, want %q", b, got, want)
		}
	}
}

func TestRenderTableAligns(t *testing.T) {
	var buf bytes.Buffer
	renderTable(&buf, []string{"name", "cpu"}, [][]string{{"api", "1.0%"}, {"worker", "12.5%"}}, map[int]bool{1: true},
		func(_, _ int, s string) string { return s })
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: %q", lines)
	}
	if !strings.HasSuffix(lines[1], " 1.0%") || !strings.HasSuffix(lines[2], "12.5%") || len(lines[1]) != len(lines[2]) {
		t.Fatalf("right-aligned numeric column is off:\n%s", buf.String())
	}
	if !strings.HasPrefix(lines[2], "  worker   ") {
		t.Fatalf("left column: %q", lines[2])
	}
}

func TestBuildSpecHealthAndRotation(t *testing.T) {
	dir := t.TempDir()
	startFlags := flagSet{
		valued: set("name", "cwd", "env", "max-restarts", "health", "log-max-size", "log-keep"),
		bools:  set("no-autorestart"),
		alias:  map[string]string{"n": "name", "e": "env"},
	}
	a, err := parse([]string{"--cwd", dir, "sleep 30", "--health", "http://127.0.0.1:3000/health", "--log-max-size", "50MB", "--log-keep", "0"}, startFlags)
	if err != nil {
		t.Fatal(err)
	}
	s, err := buildSpec(a)
	if err != nil {
		t.Fatal(err)
	}
	if s.Health != "http://127.0.0.1:3000/health" || s.LogMaxSize != "50MB" || s.LogKeep == nil || *s.LogKeep != 0 {
		t.Fatalf("spec: %+v", s)
	}
	for _, bad := range [][]string{
		{"sleep 1", "--health", "localhost:3000"},
		{"sleep 1", "--log-max-size", "huge"},
		{"sleep 1", "--log-keep", "-1"},
	} {
		a, _ := parse(append([]string{"--cwd", dir}, bad...), startFlags)
		if _, err := buildSpec(a); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
