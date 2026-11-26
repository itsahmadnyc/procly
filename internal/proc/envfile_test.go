package proc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// printEnv writes the variables it is asked about into out, one per line.
func printEnv(t *testing.T, out string, names ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("{ ")
	for _, n := range names {
		b.WriteString("echo " + n + "=\"$" + n + "\"; ")
	}
	b.WriteString("} > " + out + "; sleep 30")
	return b.String()
}

func readLines(t *testing.T, path string) map[string]string {
	t.Helper()
	var data []byte
	eventually(t, path+" to be written", func() bool {
		var err error
		data, err = os.ReadFile(path)
		return err == nil && strings.Count(string(data), "\n") > 0
	})
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		k, v, _ := strings.Cut(l, "=")
		m[k] = strings.Trim(v, `"`)
	}
	return m
}

func TestEnvFileLayersAndIsReadOnEveryStart(t *testing.T) {
	m, dir := newTestManager(t)
	a, b := filepath.Join(dir, "a.env"), filepath.Join(dir, "b.env")
	os.WriteFile(a, []byte("ONE=a\nTWO=a\nTHREE=a\n"), 0o600)
	os.WriteFile(b, []byte("TWO=b\nTHREE=b\n"), 0o600)
	out := filepath.Join(dir, "out.txt")

	s := spec(t, "app", printEnv(t, out, "ONE", "TWO", "THREE", "FOUR"))
	s.EnvFile = StringList{a, b}
	s.Env = map[string]string{"THREE": "spec", "FOUR": "spec"}
	s.AutoRestart = off()
	if _, err := m.Start(s); err != nil {
		t.Fatal(err)
	}
	got := readLines(t, out)
	if got["ONE"] != "a" || got["TWO"] != "b" || got["THREE"] != "spec" || got["FOUR"] != "spec" {
		t.Errorf("later files beat earlier ones and env beats the files: %v", got)
	}

	// Edit the file, restart: the new value is used without touching the spec.
	os.WriteFile(a, []byte("ONE=changed\n"), 0o600)
	os.Remove(out)
	if _, err := m.Restart("app"); err != nil {
		t.Fatal(err)
	}
	if got := readLines(t, out); got["ONE"] != "changed" {
		t.Errorf("the file is read again on restart: %v", got)
	}

	// Only names reach the views, never values.
	in, _ := m.Get("app")
	view, _ := json.Marshal(NewView(in))
	for _, v := range []string{"changed", `"b"`} {
		if strings.Contains(string(view), v) {
			t.Errorf("a value %s leaked into the view: %s", v, view)
		}
	}
	for _, name := range []string{"ONE", "TWO", "THREE", "FOUR"} {
		if !strings.Contains(string(view), `"`+name+`"`) {
			t.Errorf("name %s missing from the view: %s", name, view)
		}
	}
}

func TestEnvFileProblemsStopTheStartWithAMessage(t *testing.T) {
	m, dir := newTestManager(t)
	s := spec(t, "missing", "sleep 30")
	s.EnvFile = StringList{filepath.Join(dir, "nope.env")}
	if _, err := m.Start(s); err == nil || !strings.Contains(err.Error(), "env_file") || !strings.Contains(err.Error(), "nope.env") {
		t.Errorf("a missing file: %v", err)
	}

	bad := filepath.Join(dir, "bad.env")
	os.WriteFile(bad, []byte("OK=1\nthis line is wrong\n"), 0o600)
	s = spec(t, "badline", "sleep 30")
	s.EnvFile = StringList{bad}
	if _, err := m.Start(s); err == nil || !strings.Contains(err.Error(), "bad.env:2:") {
		t.Errorf("a bad line must name the file and the line: %v", err)
	}
	// The failure is written to the process's own stderr log too.
	in, err := m.Get("badline")
	if err == nil {
		data, _ := os.ReadFile(in.ErrLog)
		if !strings.Contains(string(data), "bad.env:2:") {
			t.Errorf("the error log should say why it did not start: %q", data)
		}
	}
}

func TestEnvFileMustBeAbsolute(t *testing.T) {
	s := spec(t, "x", "true")
	s.EnvFile = StringList{".env"}
	if err := s.Validate(); err == nil || !strings.Contains(err.Error(), "env_file") {
		t.Errorf("a relative path in a spec: %v", err)
	}
}
