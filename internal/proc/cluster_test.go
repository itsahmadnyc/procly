package proc

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestParseClusterCommand(t *testing.T) {
	for in, want := range map[string]clusterCmd{
		"node server.js":                        {node: "node", script: "server.js"},
		"node server.js --port 3000 -v":         {node: "node", script: "server.js", args: []string{"--port", "3000", "-v"}},
		"/usr/bin/node dist/main.js":            {node: "/usr/bin/node", script: "dist/main.js"},
		"node --max-old-space-size=512 app.js":  {node: "node", execArgv: []string{"--max-old-space-size=512"}, script: "app.js"},
		"node -r ts-node/register src/index.ts": {node: "node", execArgv: []string{"-r", "ts-node/register"}, script: "src/index.ts"},
		`node "my app.js" 'a b'`:                {node: "node", script: "my app.js", args: []string{"a b"}},
		"server.mjs":                            {node: "node", script: "server.mjs"},
		"./bin/www.js 8080":                     {node: "node", script: "./bin/www.js", args: []string{"8080"}},
	} {
		got, err := parseClusterCommand(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parseClusterCommand(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for in, why := range map[string]string{
		"npm run start":        "starts another program",
		"next start":           "starts another program",
		"python app.py":        "starts another program",
		"node":                 "needs a script",
		"node -e 'x'":          "script file",
		"node a.js && echo hi": "plain command",
		"PORT=3000 node a.js":  "environment variables",
		"node a.js | tee log":  "plain command",
		`node "unclosed.js`:    "unclosed quote",
		"":                     "command is required",
	} {
		if _, err := parseClusterCommand(in); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("parseClusterCommand(%q) should fail with %q, got %v", in, why, err)
		}
	}
}

func TestClusterSpecRules(t *testing.T) {
	dir := t.TempDir()
	base := Spec{Name: "api", Command: "node server.js", Cwd: dir, ExecMode: ExecCluster, Instances: 3}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	specs, err := Expand(base)
	if err != nil || len(specs) != 1 || specs[0].Name != "api" || specs[0].Instances != 3 || specs[0].Group != "" {
		t.Fatalf("a cluster app stays one process with its workers as instances: %+v %v", specs, err)
	}
	one := base
	one.Instances = 1
	if specs, _ := Expand(one); specs[0].Instances != 1 || specs[0].Workers() != 1 {
		t.Fatalf("one worker stays one worker: %+v", specs[0])
	}
	if w := (Spec{ExecMode: ExecCluster}).Workers(); w < 1 || w > MaxInstances {
		t.Fatalf("no instances means one worker per core: %d", w)
	}
	for name, mutate := range map[string]func(*Spec){
		"a group":          func(s *Spec) { s.Group, s.Index = "api", 0; s.Instances = 2 },
		"npm":              func(s *Spec) { s.Command = "npm start" },
		"a wrong mode":     func(s *Spec) { s.ExecMode = "turbo" },
		"too many workers": func(s *Spec) { s.Instances = MaxInstances + 1 },
	} {
		bad := base
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	if sig, ok := base.reloadSignal(); !ok || sig != syscall.SIGUSR2 {
		t.Errorf("a cluster app reloads on SIGUSR2 by default: %v %v", sig, ok)
	}
	if _, ok := (Spec{Command: "node a.js"}).reloadSignal(); ok {
		t.Error("a fork app has no reload signal unless set")
	}
	v := NewView(Info{Spec: base})
	if v.ExecMode != ExecCluster || v.Workers != 3 || v.ReloadSignal != "SIGUSR2" {
		t.Errorf("view: %+v", v)
	}
	if v := NewView(Info{Spec: Spec{Name: "x", Command: "node a.js", Cwd: dir}}); v.ExecMode != "" || v.Workers != 0 {
		t.Errorf("a fork view has no workers: %+v", v)
	}
}

func TestLauncherCommand(t *testing.T) {
	dir := t.TempDir()
	cmd, err := launchCommand(Spec{Command: "node --max-old-space-size=256 server.js --port 3000", ExecMode: ExecCluster, Instances: 4}, dir)
	if err != nil {
		t.Fatal(err)
	}
	path, _ := ensureLauncher(dir)
	want := "exec node " + path + " --workers 4 --exec-argv --max-old-space-size=256 -- server.js --port 3000"
	if cmd != want {
		t.Fatalf("\n got %s\nwant %s", cmd, want)
	}
	if b, _ := os.ReadFile(path); string(b) != string(launcherJS) {
		t.Fatal("the launcher is written out as embedded")
	}
	if got, _ := launchCommand(Spec{Command: "node a.js"}, dir); got != shellCommand("node a.js") {
		t.Fatalf("a fork app runs its command as written: %q", got)
	}
	quoted, _ := launchCommand(Spec{Command: `node "my app.js" 'it''s'`, ExecMode: ExecCluster, Instances: 1}, dir)
	if !strings.Contains(quoted, `'my app.js'`) {
		t.Fatalf("words with spaces stay one word: %s", quoted)
	}
}

// ---- end to end: needs node ----

const clusterApp = `
const http = require('http');
http.createServer((q, r) => r.end(String(process.pid))).listen(Number(process.env.PORT));
`

// workersOf are the pids of the processes in the group of pgid, except the leader.
func groupMembers(t *testing.T, pgid int) []int {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,pgid=").Output()
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[1] == strconv.Itoa(pgid) {
			n, _ := strconv.Atoi(f[0])
			pids = append(pids, n)
		}
	}
	return pids
}

func get(port uint16) (int, error) {
	// a new connection each time: the cluster hands out connections, not requests
	c := http.Client{Timeout: 700 * time.Millisecond, Transport: &http.Transport{DisableKeepAlives: true}}
	r, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return 0, err
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return strconv.Atoi(string(b))
}

func pidsAnswering(port uint16, tries int) map[int]bool {
	seen := map[int]bool{}
	for i := 0; i < tries; i++ {
		if pid, err := get(port); err == nil {
			seen[pid] = true
		}
	}
	return seen
}

func TestClusterEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	m, logs := newTestManager(t)
	t.Cleanup(func() {
		if t.Failed() {
			for _, n := range []string{"web-out.log", "web-error.log"} {
				b, _ := os.ReadFile(filepath.Join(logs, n))
				t.Logf("%s:\n%s", n, b)
			}
		}
	})
	port := freePort(t)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "app.js"), []byte(clusterApp), 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := m.Start(Spec{Name: "web", Command: "node app.js", Cwd: cwd, ExecMode: ExecCluster, Instances: 2, Env: map[string]string{"PORT": strconv.Itoa(int(port))}})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "two workers answering", func() bool { return len(pidsAnswering(port, 30)) >= 2 })
	before := pidsAnswering(port, 40)
	if len(before) != 2 {
		t.Fatalf("the two workers share the port: %v", before)
	}
	if members := groupMembers(t, in.PID); len(members) != 3 { // the launcher and two workers
		t.Fatalf("a launcher and two workers in one group: %v", members)
	}
	if _, ok := before[in.PID]; ok {
		t.Fatal("the launcher itself does not serve")
	}
	info, _ := m.Get("web")
	if info.Memory < 40<<20 {
		t.Fatalf("memory is the sum over the group, got %d", info.Memory)
	}

	// reload: every worker is replaced, and the port never goes quiet
	var mu sync.Mutex
	failures := 0
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := get(port); err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
			}
			time.Sleep(15 * time.Millisecond)
		}
	}()
	if _, err := m.Reload("web"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "new workers only", func() bool {
		now := pidsAnswering(port, 40)
		if len(now) != 2 {
			return false
		}
		for p := range now {
			if before[p] {
				return false
			}
		}
		return true
	})
	close(stop)
	<-done
	mu.Lock()
	if failures > 0 {
		t.Errorf("%d requests failed during the reload", failures)
	}
	mu.Unlock()

	// a worker that dies is replaced
	var victim int
	for p := range pidsAnswering(port, 20) {
		victim = p
	}
	_ = syscall.Kill(victim, syscall.SIGKILL)
	eventually(t, "the worker to come back", func() bool {
		seen := pidsAnswering(port, 40)
		return len(seen) == 2 && !seen[victim]
	})

	// stop ends everything
	m.Stop("web")
	eventually(t, "the group to be gone", func() bool { return len(groupMembers(t, in.PID)) == 0 })
	if _, err := get(port); err == nil {
		t.Fatal("nothing should answer after stop")
	}
}

// An app for the mode switch: as a fork instance it listens on PORT plus its
// instance number; as a cluster worker it listens on PORT, shared.
const switchApp = `
const cluster = require('cluster');
const http = require('http');
const offset = cluster.isWorker ? 0 : Number(process.env.PROCLY_INSTANCE || 0);
http.createServer((q, r) => r.end(String(process.pid))).listen(Number(process.env.PORT) + offset);
`

func TestSetModeBetweenForkAndCluster(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	m, _ := newTestManager(t)
	base := freePort(t)
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "app.js"), []byte(switchApp), 0o644); err != nil {
		t.Fatal(err)
	}
	start, err := m.Start(Spec{Name: "web", Command: "node app.js", Cwd: cwd, Env: map[string]string{"PORT": strconv.Itoa(int(base))}})
	if err != nil {
		t.Fatal(err)
	}
	answers := func(port uint16, want int) func() bool {
		return func() bool { return len(pidsAnswering(port, 40)) == want }
	}
	eventually(t, "the fork app", answers(base, 1))

	// fork -> cluster: the same process, now with two workers sharing the port
	infos, err := m.SetMode("web", ExecCluster, 2)
	if err != nil || len(infos) != 1 || infos[0].ID != start.ID || !infos[0].Spec.isCluster() || infos[0].Spec.Instances != 2 {
		t.Fatalf("fork to cluster: %+v %v", infos, err)
	}
	eventually(t, "two workers on one port", answers(base, 2))
	if _, err := m.SetMode("web", ExecCluster, 2); err == nil || !strings.Contains(err.Error(), "already runs that way") {
		t.Fatalf("asking for what is already so: %v", err)
	}

	// cluster -> fork with three instances: a group, each on its own port
	infos, err = m.SetMode("web", ExecFork, 3)
	if err != nil || len(infos) != 3 || infos[0].Spec.Group != "web" || infos[2].Spec.Name != "web-3" || infos[0].Spec.isCluster() {
		t.Fatalf("cluster to fork: %+v %v", infos, err)
	}
	for i := uint16(0); i < 3; i++ {
		eventually(t, "instance "+strconv.Itoa(int(i)), answers(base+i, 1))
	}
	if _, err := m.SetMode("web-2", ExecCluster, 2); err == nil || !strings.Contains(err.Error(), "one of 3 instances") {
		t.Fatalf("one instance of a group cannot change alone: %v", err)
	}

	// group -> cluster: the instances become the workers
	infos, err = m.SetMode("web", ExecCluster, 0)
	if err != nil || len(infos) != 1 || infos[0].Spec.Name != "web" || infos[0].Spec.Workers() != 3 || infos[0].Spec.Group != "" {
		t.Fatalf("group to cluster: %+v %v", infos, err)
	}
	eventually(t, "three workers on one port", answers(base, 3))
	if list := m.List(); len(list) != 1 || list[0].Name != "web" {
		t.Fatalf("only the one process is left: %+v", list)
	}

	// cluster -> fork with no count is the app as it was: ONE process, never
	// N copies that fight over the same port (this once left an app crashing)
	infos, err = m.SetMode("web", ExecFork, 0)
	if err != nil || len(infos) != 1 || infos[0].Spec.Name != "web" || infos[0].Spec.isCluster() || infos[0].Spec.Group != "" || infos[0].Spec.Instances > 1 {
		t.Fatalf("cluster to fork: %+v %v", infos, err)
	}
	eventually(t, "one process on the port", answers(base, 1))
	if list := m.List(); len(list) != 1 {
		t.Fatalf("exactly one process after going back to fork: %+v", list)
	}
	if _, err := m.SetMode("web", ExecCluster, 3); err != nil {
		t.Fatal(err)
	}
	eventually(t, "three workers again", answers(base, 3))

	// a refusal changes nothing
	other, _ := m.Start(Spec{Name: "tool", Command: "sleep 30", Cwd: cwd})
	if _, err := m.SetMode("tool", ExecCluster, 2); err == nil || !strings.Contains(err.Error(), "another program") {
		t.Fatalf("a command that is not a node script cannot be clustered: %v", err)
	}
	if in, _ := m.Get("tool"); in.PID != other.PID || in.Status != StatusOnline {
		t.Fatalf("a refused change must leave the app running: %+v", in)
	}
	if _, err := m.SetMode("all", ExecCluster, 2); err == nil {
		t.Fatal("not all at once")
	}
	if _, err := m.SetMode("nope", ExecCluster, 2); err == nil {
		t.Fatal("an unknown app")
	}

	// cluster -> fork with one: a single plain process again
	infos, err = m.SetMode("web", ExecFork, 1)
	if err != nil || len(infos) != 1 || infos[0].Spec.isCluster() || infos[0].Spec.Instances != 0 {
		t.Fatalf("cluster to one fork process: %+v %v", infos, err)
	}
	eventually(t, "the single process", answers(base, 1))
}

func TestSetModeKeepsAStoppedAppStopped(t *testing.T) {
	m, _ := newTestManager(t)
	cwd := t.TempDir()
	if _, err := m.Start(Spec{Name: "idle", Command: "node app.js", Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
	m.Stop("idle")
	infos, err := m.SetMode("idle", ExecCluster, 2)
	if err != nil || len(infos) != 1 || infos[0].Status != StatusStopped || !infos[0].Spec.isCluster() {
		t.Fatalf("a stopped app stays stopped: %+v %v", infos, err)
	}
	infos, err = m.SetMode("idle", ExecFork, 2)
	if err != nil || len(infos) != 2 || infos[0].Status != StatusStopped {
		t.Fatalf("and as a group too: %+v %v", infos, err)
	}
}
