package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadYAML(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "api"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := writeFile(t, dir, "procly.yml", `
apps:
  - name: api
    command: node server.js
    cwd: ./api
    env:
      PORT: 3000
      DEBUG: true
    max_restarts: 5
  - name: worker
    command: python3 worker.py
    autorestart: false
  - name: abs
    command: "true"
    cwd: /tmp
`)
	apps, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 3 {
		t.Fatalf("apps: %+v", apps)
	}
	api, worker, abs := apps[0], apps[1], apps[2]
	if api.Cwd != filepath.Join(dir, "api") {
		t.Errorf("relative cwd should resolve from the config file: %s", api.Cwd)
	}
	if api.Env["PORT"] != "3000" || api.Env["DEBUG"] != "true" || api.MaxRestarts != 5 {
		t.Errorf("api: %+v (numbers and booleans in env become strings)", api)
	}
	if worker.Cwd != dir || worker.ShouldAutoRestart() {
		t.Errorf("worker: cwd %s, autorestart %v", worker.Cwd, worker.ShouldAutoRestart())
	}
	if abs.Cwd != "/tmp" {
		t.Errorf("absolute cwd changed: %s", abs.Cwd)
	}
}

func TestLoadJSON(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "procly.json", `{"apps":[{"name":"api","command":"node x.js","env":{"A":"1"}}]}`)
	apps, err := Load(p)
	if err != nil || len(apps) != 1 || apps[0].Env["A"] != "1" || apps[0].Cwd != dir {
		t.Fatalf("json: %+v %v", apps, err)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct{ body, want string }{
		"no apps":   {"apps: []\n", "no apps"},
		"no name":   {"apps:\n  - command: x\n", "no name"},
		"duplicate": {"apps:\n  - {name: a, command: x}\n  - {name: a, command: y}\n", "duplicate"},
		"bad yaml":  {"apps: [\n", ""},
	}
	for name, c := range cases {
		p := writeFile(t, dir, name+".yml", c.body)
		_, err := Load(p)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error mentioning %q", name, err, c.want)
		}
	}
	if _, err := Load(filepath.Join(dir, "missing.yml")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestIsConfig(t *testing.T) {
	for path, want := range map[string]bool{
		"procly.yml": true, "x.YAML": true, "apps.json": true,
		"server.js": false, "app.py": false, "yml": false,
	} {
		if IsConfig(path) != want {
			t.Errorf("IsConfig(%q) = %v", path, !want)
		}
	}
}

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "procly.yml", `
apps:
  - name: one
    command: node a.js
    env_file: .env
  - name: two
    command: node b.js
    cwd: ./sub
    env_file: [.env, /etc/shared.env, config/prod.env]
  - name: three
    command: node c.js
`)
	apps, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string(apps[0].EnvFile); len(got) != 1 || got[0] != filepath.Join(dir, ".env") {
		t.Errorf("a single path resolves from the config's folder: %v", got)
	}
	want := []string{filepath.Join(dir, ".env"), "/etc/shared.env", filepath.Join(dir, "config/prod.env")}
	got := []string(apps[1].EnvFile)
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("a list resolves from the config's folder, not from cwd: %v, want %v", got, want)
	}
	if len(apps[2].EnvFile) != 0 {
		t.Errorf("no env_file, no files: %v", apps[2].EnvFile)
	}

	bad := writeFile(t, dir, "bad.yml", "apps:\n  - name: x\n    command: y\n    env_file: {a: b}\n")
	if _, err := Load(bad); err == nil || !strings.Contains(err.Error(), "path or a list of paths") {
		t.Errorf("a map is not a path: %v", err)
	}
}
