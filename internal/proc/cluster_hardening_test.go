package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func needNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
}

func writeApp(t *testing.T, js string) string {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "app.js"), []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	return cwd
}

// A heavy app: it "syncs a database" for a while before it listens. With the
// workers started one at a time, the syncs never overlap.
func TestWorkersStartOneAtATime(t *testing.T) {
	needNode(t)
	m, _ := newTestManager(t)
	trace := filepath.Join(t.TempDir(), "trace.log")
	cwd := writeApp(t, `
const fs = require('fs'), http = require('http');
const trace = process.env.TRACE;
const id = process.env.NODE_APP_INSTANCE;
fs.appendFileSync(trace, 'begin ' + id + '\n');
setTimeout(() => {
  fs.appendFileSync(trace, 'end ' + id + '\n');
  http.createServer((q, r) => r.end(String(process.pid))).listen(Number(process.env.PORT));
}, 700);
`)
	port := freePort(t)
	if _, err := m.Start(Spec{Name: "heavy", Command: "node app.js", Cwd: cwd, ExecMode: ExecCluster, Instances: 3,
		Env: map[string]string{"PORT": strconv.Itoa(int(port)), "TRACE": trace}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "three workers answer", func() bool { return len(pidsAnswering(port, 60)) == 3 })
	b, _ := os.ReadFile(trace)
	var order []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		order = append(order, l)
	}
	want := []string{"begin 0", "end 0", "begin 1", "end 1", "begin 2", "end 2"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("the boots overlapped:\n got %v\nwant %v", order, want)
	}
	// the launcher reports each worker
	eventually(t, "the status lists three listening workers", func() bool {
		in, err := m.Get("heavy")
		if err != nil || in.Cluster == nil {
			return false
		}
		l, up := in.Cluster.live()
		return l == 3 && up == 3 && in.Cluster.Ready
	})
}

// A switch to cluster that "starts" but then crashes is undone.
func TestSwitchThatCrashesIsRolledBack(t *testing.T) {
	needNode(t)
	m, _ := newTestManager(t)
	port := freePort(t)
	// fine as one process; every cluster worker dies a second after it starts
	cwd := writeApp(t, `
const http = require('http');
if (process.env.NODE_APP_INSTANCE !== undefined) setTimeout(() => process.exit(1), 1000);
http.createServer((q, r) => r.end(String(process.pid))).listen(Number(process.env.PORT) + (process.env.NODE_APP_INSTANCE ? 0 : 0));
`)
	start, err := m.Start(Spec{Name: "web", Command: "node app.js", Cwd: cwd, Env: map[string]string{"PORT": strconv.Itoa(int(port))}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "it answers", func() bool { return len(pidsAnswering(port, 10)) == 1 })
	_, err = m.SetMode("web", ExecCluster, 2)
	if err == nil || !strings.Contains(err.Error(), "previous setup was put back") {
		t.Fatalf("a switch whose workers crash must fail and roll back: %v", err)
	}
	in, gerr := m.Get("web")
	if gerr != nil || in.Spec.isCluster() || in.Status != StatusOnline {
		t.Fatalf("the old setup must be running again: %+v %v (was id %d)", in.Spec.ExecMode, gerr, start.ID)
	}
	eventually(t, "the app answers again", func() bool { return len(pidsAnswering(port, 10)) == 1 })
}

func TestScaleWithoutRestart(t *testing.T) {
	needNode(t)
	m, _ := newTestManager(t)
	port := freePort(t)
	cwd := writeApp(t, `require('http').createServer((q, r) => r.end(String(process.pid))).listen(Number(process.env.PORT));`)
	in, err := m.Start(Spec{Name: "web", Command: "node app.js", Cwd: cwd, ExecMode: ExecCluster, Instances: 2, Env: map[string]string{"PORT": strconv.Itoa(int(port))}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "two workers", func() bool { return len(pidsAnswering(port, 40)) == 2 })
	if _, err := m.Scale("web", 1, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "three workers", func() bool { return len(pidsAnswering(port, 60)) == 3 })
	got, _ := m.Get("web")
	if got.PID != in.PID || got.Spec.Workers() != 3 {
		t.Fatalf("scaling must keep the same launcher process: pid %d -> %d, workers %d", in.PID, got.PID, got.Spec.Workers())
	}
	if _, err := m.Scale("web", -2, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "one worker", func() bool { return len(pidsAnswering(port, 60)) == 1 })
	if _, err := m.Scale("web", 1, false); err == nil {
		t.Fatal("scaling to what it already is should say so")
	}
	if _, err := m.Scale("web", 0, false); err == nil {
		t.Fatal("zero workers is not a scale, it is a stop")
	}
	fork, _ := m.Start(Spec{Name: "plain", Command: "sleep 30", Cwd: cwd})
	if _, err := m.Scale(fork.Name, 3, false); err == nil || !strings.Contains(err.Error(), "does not run in cluster mode") {
		t.Fatalf("scale is for cluster apps: %v", err)
	}
}

// max_memory is per worker in cluster mode: a worker over it is replaced, and
// an app that is over it right away makes procly give up instead of looping.
func TestClusterMemoryLimitIsPerWorker(t *testing.T) {
	needNode(t)
	m, _ := newTestManager(t)
	port := freePort(t)
	cwd := writeApp(t, `require('http').createServer((q, r) => r.end(String(process.pid))).listen(Number(process.env.PORT));`)
	// node itself is bigger than 1MB, so every worker is over a 1MB limit
	in, err := m.Start(Spec{Name: "big", Command: "node app.js", Cwd: cwd, ExecMode: ExecCluster, Instances: 1, MaxMemory: "1MB", Env: map[string]string{"PORT": strconv.Itoa(int(port))}})
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := launchCommand(in.Spec, t.TempDir())
	if err != nil || !strings.Contains(cmd, "--max-worker-memory 1048576") {
		t.Fatalf("the launcher gets the per-worker limit: %s %v", cmd, err)
	}
	// procly itself must not restart the whole group for it
	before := in.PID
	m.checkMemory()
	time.Sleep(300 * time.Millisecond)
	if got, _ := m.Get("big"); got.PID != before {
		t.Fatal("procly must leave cluster memory to the launcher")
	}
	// and the launcher does replace the worker that is over its limit
	eventually(t, "the launcher notices", func() bool {
		b, _ := os.ReadFile(in.OutLog)
		return strings.Contains(string(b), "over its limit")
	})
}

func TestPreflightFindsWhatBreaksInCluster(t *testing.T) {
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "server.js"), []byte(`
const { Sequelize } = require('sequelize');
sequelize.sync({ alter: true }).then(() => app.listen(3000));
const cron = require('node-cron'); cron.schedule('* * * * *', run);
const io = require('socket.io')(server);
`), 0o644)
	os.MkdirAll(filepath.Join(cwd, "node_modules", "x"), 0o755)
	os.WriteFile(filepath.Join(cwd, "node_modules", "x", "index.js"), []byte(`sequelize.sync({ alter: true })`), 0o644)
	ws := Preflight(Spec{Cwd: cwd}, ExecCluster, 2)
	kinds := map[string]bool{}
	for _, w := range ws {
		kinds[w.Kind] = true
		if w.Advice == "" || w.File != "server.js" {
			t.Errorf("a warning says where and what to do: %+v", w)
		}
	}
	for _, k := range []string{"startup", "jobs", "realtime"} {
		if !kinds[k] {
			t.Errorf("missing a %q warning: %+v", k, ws)
		}
	}
	if ws[0].Kind != "startup" {
		t.Errorf("the most dangerous comes first: %+v", ws)
	}
	if w := Preflight(Spec{Cwd: cwd}, ExecFork, 1); len(w) != 0 {
		t.Errorf("one plain process has nothing to warn about: %+v", w)
	}
	clean := t.TempDir()
	os.WriteFile(filepath.Join(clean, "server.js"), []byte(`require('http').createServer((q,r)=>r.end('ok')).listen(3000)`), 0o644)
	if w := Preflight(Spec{Cwd: clean}, ExecCluster, 2); len(w) != 0 {
		t.Errorf("a clean app gets no warnings: %+v", w)
	}
}

func TestForkCopiesNeedTheirOwnPort(t *testing.T) {
	cwd := writeApp(t, `require('http').createServer((q,r)=>r.end('x')).listen(Number(process.env.PORT))`)
	spec := Spec{Name: "api", Command: "node app.js", Cwd: cwd, Instances: 3, Env: map[string]string{"PORT": "4100"}}
	if _, err := Expand(spec); err == nil || !strings.Contains(err.Error(), "increment_var") {
		t.Fatalf("three copies on one fixed port must be refused: %v", err)
	}
	spec.IncrementVar = "PORT"
	out, err := Expand(spec)
	if err != nil || len(out) != 3 || out[0].Env["PORT"] != "4100" || out[1].Env["PORT"] != "4101" || out[2].Env["PORT"] != "4102" {
		t.Fatalf("increment_var gives each copy its own port: %+v %v", out, err)
	}
	if spec.Env["PORT"] != "4100" {
		t.Fatal("the spec the person wrote must not change")
	}
	// an app that reads its own instance number is trusted to pick a port
	aware := writeApp(t, `require('http').createServer((q,r)=>r.end('x')).listen(Number(process.env.PORT) + Number(process.env.PROCLY_INSTANCE))`)
	if _, err := Expand(Spec{Name: "api", Command: "node app.js", Cwd: aware, Instances: 2, Env: map[string]string{"PORT": "4200"}}); err != nil {
		t.Fatalf("an app that knows its instance is fine: %v", err)
	}
}
