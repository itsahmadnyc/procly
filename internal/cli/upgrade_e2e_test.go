package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itsahmadnyc/procly/internal/client"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/upgrade"
)

// buildVersion builds the CLI as a release of the given version, so a test can
// upgrade from one real build to another.
func buildVersion(t *testing.T, version string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "procly-"+version)
	cmd := exec.Command("go", "build", "-ldflags", "-X github.com/itsahmadnyc/procly/internal/version.Version="+version, "-o", out, "../../cmd/procly")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building procly %s: %v\n%s", version, err, b)
	}
	return out
}

func copyExe(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// releaseMirror serves binary as the release of version, the way GitHub does:
// the archive, checksums.txt, and a "latest" page that redirects to the tag.
// badChecksum makes checksums.txt lie.
func releaseMirror(t *testing.T, bin, version string, badChecksum bool) (base, latest string) {
	t.Helper()
	platform, err := upgrade.Platform()
	if err != nil {
		t.Skip(err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "procly_" + platform + "/procly", Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg})
	tw.Write(data)
	tw.Close()
	gz.Close()
	sum := sha256.Sum256(buf.Bytes())
	hexsum := hex.EncodeToString(sum[:])
	if badChecksum {
		hexsum = strings.Repeat("0", 64)
	}
	asset := "procly_" + platform + ".tar.gz"
	mux := http.NewServeMux()
	mux.HandleFunc("/"+asset, func(w http.ResponseWriter, _ *http.Request) { w.Write(buf.Bytes()) })
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintf(w, "%s  %s\n", hexsum, asset) })
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/itsahmadnyc/procly/releases/tag/v"+version, http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, srv.URL + "/releases/latest"
}

// runExe runs a specific procly binary in c's home, with extra environment.
func (c *cli) runExe(bin string, env []string, args ...string) (string, int) {
	c.t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = c.dir
	cmd.Env = append(append(os.Environ(), "PROCLY_HOME="+c.home, "NO_COLOR=1", "PROCLY_SERVER="), env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		c.t.Fatalf("running %s %v: %v", bin, args, err)
	}
	return string(out), code
}

func (c *cli) okExe(bin string, env []string, args ...string) string {
	c.t.Helper()
	out, code := c.runExe(bin, env, args...)
	if code != 0 {
		c.t.Fatalf("%s %s: exit %d\n%s", filepath.Base(bin), strings.Join(args, " "), code, out)
	}
	return out
}

// viewsOf reads `ls --json` with the given binary.
func (c *cli) viewsOf(bin string) map[string]proc.View {
	c.t.Helper()
	var views []proc.View
	if err := json.Unmarshal([]byte(c.okExe(bin, nil, "ls", "--json")), &views); err != nil {
		c.t.Fatal(err)
	}
	m := map[string]proc.View{}
	for _, v := range views {
		m[v.Name] = v
	}
	return m
}

// daemonVersion asks the daemon of c's home which version it is.
func (c *cli) daemonVersionOf() string {
	c.t.Helper()
	c.t.Setenv("PROCLY_HOME", c.home)
	p, err := client.New().Ping()
	if err != nil {
		return "(no daemon)"
	}
	return p.Version
}

// eventsApp logs when it starts and stops (a stop takes 0.3 s), into events.log.
const eventsApp = `trap 'echo "stop $PROCLY_NAME $(date +%s%N)" >> events.log; sleep 0.3; exit 0' TERM
echo "start $PROCLY_NAME $(date +%s%N)" >> events.log
while true; do sleep 0.1; done
`

func eventLines(c *cli) [][]string {
	b, _ := os.ReadFile(filepath.Join(c.dir, "events.log"))
	var out [][]string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if f := strings.Fields(l); len(f) == 3 {
			out = append(out, f)
		}
	}
	return out
}

func TestUpgradeBetweenTwoBuilds(t *testing.T) {
	oldBin, newBin := buildVersion(t, "1.0.0"), buildVersion(t, "2.0.0")
	base, latest := releaseMirror(t, newBin, "2.0.0", false)
	mirror := []string{"PROCLY_DOWNLOAD_BASE=" + base, "PROCLY_LATEST_URL=" + latest}

	installed := filepath.Join(t.TempDir(), "bin", "procly")
	copyExe(t, oldBin, installed)

	c := newCLI(t)
	c.file("app.sh", eventsApp)
	c.okExe(installed, nil, "start", "app.sh", "--name", "web", "--instances", "2")
	c.okExe(installed, nil, "start", "app.sh", "--name", "api")
	c.okExe(installed, nil, "start", "app.sh", "--name", "idle")
	c.okExe(installed, nil, "stop", "idle")
	c.okExe(installed, nil, "save")
	dumpBefore, _ := os.ReadFile(filepath.Join(c.home, "dump.json"))
	if got := c.daemonVersionOf(); got != "1.0.0" {
		t.Fatalf("the daemon should be 1.0.0, it is %s", got)
	}
	before := c.viewsOf(installed)
	waitFor(t, "the apps to log their start", func() bool { return len(eventLines(c)) >= 4 })

	// --check says there is a newer version and changes nothing.
	out := c.okExe(installed, mirror, "upgrade", "--check")
	contains(t, out, "procly 2.0.0 is available; you have 1.0.0")
	if v, _ := upgrade.Version(installed); v != "1.0.0" {
		t.Fatalf("--check must not install anything: %s", v)
	}

	// --dry-run shows what would restart and changes nothing.
	out = c.okExe(installed, mirror, "upgrade", "--dry-run")
	contains(t, out, "1.0.0 -> 2.0.0", "3 running apps", "one at a time", "Stopped apps stay stopped", "--dry-run: nothing was changed")
	if v, _ := upgrade.Version(installed); v != "1.0.0" {
		t.Fatalf("--dry-run must not install anything: %s", v)
	}

	// A release whose checksum does not match is refused, and nothing is touched.
	badBase, badLatest := releaseMirror(t, newBin, "2.0.0", true)
	out, code := c.runExe(installed, []string{"PROCLY_DOWNLOAD_BASE=" + badBase, "PROCLY_LATEST_URL=" + badLatest}, "upgrade")
	if code == 0 || !strings.Contains(out, "does not match its checksum") {
		t.Fatalf("a bad checksum: exit %d\n%s", code, out)
	}
	if v, _ := upgrade.Version(installed); v != "1.0.0" || c.daemonVersionOf() != "1.0.0" {
		t.Fatalf("a refused upgrade changes nothing: binary %s", v)
	}
	for name, v := range c.viewsOf(installed) {
		if before[name].PID != v.PID {
			t.Errorf("%s was restarted by a refused upgrade", name)
		}
	}

	// The upgrade itself.
	os.Remove(filepath.Join(c.dir, "events.log"))
	out = c.okExe(installed, mirror, "upgrade")
	contains(t, out, "installed procly 2.0.0", "restarted web-1", "restarted web-2", "restarted api", "kept stopped idle")
	if i, j := strings.Index(out, "restarted web-1"), strings.Index(out, "restarted web-2"); i < 0 || j < i {
		t.Errorf("the instances restart in order:\n%s", out)
	}

	if v, _ := upgrade.Version(installed); v != "2.0.0" {
		t.Errorf("the binary is %s", v)
	}
	if got := c.daemonVersionOf(); got != "2.0.0" {
		t.Errorf("the daemon is %s, want 2.0.0", got)
	}
	if _, err := os.Stat(installed + ".old"); !os.IsNotExist(err) {
		t.Errorf("the backup should be removed after a good handover: %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.home, "upgrade.json")); !os.IsNotExist(err) {
		t.Errorf("upgrade.json should be removed when done: %v", err)
	}
	after := c.viewsOf(installed)
	for _, name := range []string{"web-1", "web-2", "api"} {
		if after[name].Status != proc.StatusOnline || after[name].PID == before[name].PID || after[name].PID == 0 {
			t.Errorf("%s: %s pid %d, was %d (restarted once, online)", name, after[name].Status, after[name].PID, before[name].PID)
		}
	}
	if after["idle"].Status != proc.StatusStopped || after["idle"].PID != 0 {
		t.Errorf("a stopped app stays stopped, and stays in the list: %+v", after["idle"])
	}
	if after["web-1"].Group != "web" || after["web-2"].Instances != 2 {
		t.Errorf("the group survives: %+v %+v", after["web-1"], after["web-2"])
	}
	if dumpAfter, _ := os.ReadFile(filepath.Join(c.home, "dump.json")); string(dumpAfter) != string(dumpBefore) {
		t.Errorf("the saved list must not be touched by an upgrade")
	}

	// One at a time: each app stopped only after the previous one had been
	// stopped and started again (a stop takes 0.3 s, so the gaps show it).
	var stops []int64
	var names []string
	for _, f := range eventLines(c) {
		if f[0] == "stop" {
			ns, _ := strconv.ParseInt(f[2], 10, 64)
			stops, names = append(stops, ns), append(names, f[1])
		}
	}
	if strings.Join(names, ",") != "web-1,web-2,api" {
		t.Fatalf("stops in order: %v", names)
	}
	for i := 1; i < len(stops); i++ {
		if gap := time.Duration(stops[i] - stops[i-1]); gap < 250*time.Millisecond {
			t.Errorf("%s was stopped only %v after %s: the apps must be restarted one at a time", names[i], gap, names[i-1])
		}
	}

	// Running it again has nothing to do.
	contains(t, c.okExe(installed, mirror, "upgrade"), "procly 2.0.0 is up to date")
	contains(t, c.okExe(installed, mirror, "upgrade", "--check"), "procly 2.0.0 is the latest version")
}

func TestUpgradeNoRestartThenHandOver(t *testing.T) {
	oldBin, newBin := buildVersion(t, "1.0.0"), buildVersion(t, "2.0.0")
	base, latest := releaseMirror(t, newBin, "2.0.0", false)
	mirror := []string{"PROCLY_DOWNLOAD_BASE=" + base, "PROCLY_LATEST_URL=" + latest}
	installed := filepath.Join(t.TempDir(), "procly")
	copyExe(t, oldBin, installed)

	c := newCLI(t)
	c.okExe(installed, nil, "start", "sleep 60", "--name", "one")
	before := c.viewsOf(installed)["one"].PID

	out := c.okExe(installed, mirror, "upgrade", "--no-restart")
	contains(t, out, "installed procly 2.0.0", "keep running the old version", "Run 'procly upgrade' again")
	if v, _ := upgrade.Version(installed); v != "2.0.0" {
		t.Fatalf("binary %s", v)
	}
	if got := c.daemonVersionOf(); got != "1.0.0" || c.viewsOf(installed)["one"].PID != before {
		t.Fatalf("--no-restart leaves the daemon and the apps alone: daemon %s", got)
	}
	// check says the daemon is behind.
	contains(t, c.okExe(installed, mirror, "upgrade", "--check"), "The daemon still runs 1.0.0")

	// The second run only has the daemon to hand over: the binary is already new.
	out = c.okExe(installed, mirror, "upgrade")
	contains(t, out, "The binary is procly 2.0.0, but the daemon still runs 1.0.0", "restarted one")
	if got := c.daemonVersionOf(); got != "2.0.0" || c.viewsOf(installed)["one"].PID == before {
		t.Errorf("handed over: daemon %s, pid %d (was %d)", got, c.viewsOf(installed)["one"].PID, before)
	}
}

func TestUpgradeUnderSystemdOnlyReplacesTheBinary(t *testing.T) {
	oldBin, newBin := buildVersion(t, "1.0.0"), buildVersion(t, "2.0.0")
	base, latest := releaseMirror(t, newBin, "2.0.0", false)
	mirror := []string{"PROCLY_DOWNLOAD_BASE=" + base, "PROCLY_LATEST_URL=" + latest}
	installed := filepath.Join(t.TempDir(), "procly")
	copyExe(t, oldBin, installed)

	c := newCLI(t)
	// systemd puts INVOCATION_ID in the environment of the daemon it starts.
	c.okExe(installed, []string{"INVOCATION_ID=0123456789abcdef"}, "start", "sleep 60", "--name", "one")
	before := c.viewsOf(installed)["one"].PID

	out := c.okExe(installed, mirror, "upgrade")
	contains(t, out, "installed procly 2.0.0", "run by systemd", "stops the apps with it", "sudo systemctl restart procly")
	if v, _ := upgrade.Version(installed); v != "2.0.0" {
		t.Errorf("the binary is replaced: %s", v)
	}
	if got := c.daemonVersionOf(); got != "1.0.0" || c.viewsOf(installed)["one"].PID != before {
		t.Errorf("the daemon and the apps are left alone: daemon %s", got)
	}
	// The daemon itself refuses a handover when asked directly.
	c.t.Setenv("PROCLY_HOME", c.home)
	if _, err := client.New().Handover(); err == nil || !strings.Contains(err.Error(), "systemd") {
		t.Errorf("a systemd daemon must refuse a handover: %v", err)
	}
	if got := c.daemonVersionOf(); got != "1.0.0" {
		t.Errorf("a refused handover leaves the daemon running: %s", got)
	}
}

func TestUpgradeLeavesBinariesThatOthersOwn(t *testing.T) {
	oldBin := buildVersion(t, "1.0.0")
	brew := filepath.Join(t.TempDir(), "opt", "homebrew", "Cellar", "procly", "1.0.0", "bin", "procly")
	copyExe(t, oldBin, brew)
	c := newCLI(t)
	out, code := c.runExe(brew, nil, "upgrade")
	if code == 0 || !strings.Contains(out, "installed by homebrew") || !strings.Contains(out, "brew upgrade procly") {
		t.Errorf("a Homebrew binary: exit %d\n%s", code, out)
	}
	if v, _ := upgrade.Version(brew); v != "1.0.0" {
		t.Errorf("nothing may be replaced: %s", v)
	}
}

// An upgrade that was interrupted after the old daemon left is finished by running it again.
func TestUpgradeFinishesAnInterruptedHandover(t *testing.T) {
	bin := buildVersion(t, "1.0.0")
	c := newCLI(t)
	c.okExe(bin, nil, "start", "sleep 60", "--name", "one")
	c.okExe(bin, nil, "start", "sleep 61", "--name", "two")
	c.okExe(bin, nil, "start", "sleep 62", "--name", "off")
	c.okExe(bin, nil, "stop", "off")
	v := c.viewsOf(bin)
	oneBefore, twoBefore := v["one"].PID, v["two"].PID

	// What the old daemon hands over, written to disk; then the daemon is gone
	// while the apps keep running, as when the command is killed half way.
	var entries []proc.HandoverEntry
	for _, name := range []string{"one", "two", "off"} {
		entries = append(entries, proc.HandoverEntry{Running: name != "off", Spec: proc.Spec{Name: name, Command: map[string]string{"one": "sleep 60", "two": "sleep 61", "off": "sleep 62"}[name], Cwd: c.dir}})
	}
	data, _ := json.Marshal(entries)
	os.WriteFile(filepath.Join(c.home, "upgrade.json"), data, 0o600)
	b, _ := os.ReadFile(filepath.Join(c.home, "daemon.pid"))
	dpid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	exec.Command("kill", "-9", strconv.Itoa(dpid)).Run()
	waitFor(t, "the daemon to be gone", func() bool { return !alive(dpid) })
	if !alive(oneBefore) || !alive(twoBefore) {
		t.Fatal("the apps outlive the daemon")
	}

	out := c.okExe(bin, nil, "upgrade")
	contains(t, out, "An earlier upgrade was interrupted", "restarted one", "restarted two", "kept stopped off")
	after := c.viewsOf(bin)
	if after["one"].Status != proc.StatusOnline || after["one"].PID == oneBefore || after["two"].PID == twoBefore {
		t.Errorf("the apps are restarted under the new daemon: %+v %+v", after["one"], after["two"])
	}
	if after["off"].Status != proc.StatusStopped {
		t.Errorf("the stopped app is kept stopped: %+v", after["off"])
	}
	if _, err := os.Stat(filepath.Join(c.home, "upgrade.json")); !os.IsNotExist(err) {
		t.Errorf("the file is removed when done: %v", err)
	}
	// Running it again finds nothing to finish.
	if out := c.okExe(bin, nil, "upgrade", "--check"); strings.Contains(out, "interrupted") {
		t.Errorf("nothing left to finish:\n%s", out)
	}
}
