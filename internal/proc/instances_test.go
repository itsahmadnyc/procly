package proc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExpand(t *testing.T) {
	off := false
	keep := 5
	s := Spec{Name: "queue", Command: "node w.js", Cwd: t.TempDir(), Instances: 3, Env: map[string]string{"A": "1"}, AutoRestart: &off, LogKeep: &keep}
	got, err := Expand(s)
	if err != nil || len(got) != 3 {
		t.Fatalf("Expand = %v, %v", got, err)
	}
	for i, e := range got {
		wantName := "queue-" + string(rune('1'+i))
		if e.Name != wantName || e.Group != "queue" || e.Index != i || e.Instances != 3 {
			t.Errorf("instance %d = %+v", i, e)
		}
		if err := e.Validate(); err != nil {
			t.Errorf("an expanded instance must validate: %v", err)
		}
	}
	// Copies are independent: changing one must not change the others.
	got[0].Env["A"] = "changed"
	*got[0].AutoRestart = true
	if got[1].Env["A"] != "1" || *got[1].AutoRestart || s.Env["A"] != "1" {
		t.Errorf("the instances share a map or a pointer: %+v %v", got[1].Env, *got[1].AutoRestart)
	}

	for _, n := range []int{0, 1} {
		s.Instances = n
		got, err := Expand(s)
		if err != nil || len(got) != 1 || got[0].Name != "queue" || got[0].Group != "" || got[0].Instances != 0 {
			t.Errorf("instances %d is a single plain process: %+v %v", n, got, err)
		}
	}
	for _, bad := range []int{-1, MaxInstances + 1, 1000} {
		s.Instances = bad
		if _, err := Expand(s); err == nil || !strings.Contains(err.Error(), "instances must be between") {
			t.Errorf("instances %d should be refused: %v", bad, err)
		}
	}
	// A name that is too long once the suffix is added is refused, not truncated.
	s.Instances, s.Name = 2, strings.Repeat("a", 64)
	if _, err := Expand(s); err == nil {
		t.Error("a 64 character name cannot take a suffix")
	}
	// Already expanded (a saved spec): left alone.
	again, err := Expand(Spec{Name: "queue-2", Command: "x", Cwd: "/", Instances: 3, Group: "queue", Index: 1})
	if err != nil || len(again) != 1 || again[0].Name != "queue-2" {
		t.Errorf("an expanded spec must not be expanded again: %+v %v", again, err)
	}
}

func startGroup(t *testing.T, m *Manager, name, command string, n int) []Info {
	t.Helper()
	specs, err := Expand(Spec{Name: name, Command: command, Cwd: t.TempDir(), Instances: n})
	if err != nil {
		t.Fatal(err)
	}
	var infos []Info
	for _, s := range specs {
		in, err := m.Start(s)
		if err != nil {
			t.Fatal(err)
		}
		infos = append(infos, in)
	}
	return infos
}

func TestInstancesGetTheirOwnEnvironmentAndLogs(t *testing.T) {
	m, dir := newTestManager(t)
	out := filepath.Join(dir, "seen")
	os.Mkdir(out, 0o755)
	cmd := `echo "$PROCLY_INSTANCE/$PROCLY_INSTANCES/$PROCLY_NAME" > ` + out + `/$PROCLY_NAME; sleep 30`
	infos := startGroup(t, m, "worker", cmd, 3)
	logs := map[string]bool{}
	for i, in := range infos {
		name := "worker-" + string(rune('1'+i))
		eventually(t, name+" to write its environment", func() bool {
			b, err := os.ReadFile(filepath.Join(out, name))
			return err == nil && strings.TrimSpace(string(b)) == string(rune('0'+i))+"/3/"+name
		})
		if logs[in.OutLog] {
			t.Errorf("instances share a log file: %s", in.OutLog)
		}
		logs[in.OutLog] = true
	}
	// A plain process gets no instance variables.
	m.Start(spec(t, "plain", `echo "[$PROCLY_INSTANCE][$PROCLY_INSTANCES]" > `+out+`/plain; sleep 30`))
	eventually(t, "plain to write", func() bool {
		b, _ := os.ReadFile(filepath.Join(out, "plain"))
		return strings.TrimSpace(string(b)) == "[][]"
	})
}

func TestGroupTargets(t *testing.T) {
	m, _ := newTestManager(t)
	startGroup(t, m, "queue", "sleep 30", 3)
	m.Start(spec(t, "api", "sleep 30"))

	// A name that is a group means all of its instances, in order.
	infos, err := m.Stop("queue")
	if err != nil || len(infos) != 3 {
		t.Fatalf("stop queue = %v, %v", infos, err)
	}
	for i, in := range infos {
		if in.Name != "queue-"+string(rune('1'+i)) || in.Status != StatusStopped {
			t.Errorf("stop queue: %d %+v", i, in)
		}
	}
	if in, _ := m.Get("api"); in.Status != StatusOnline {
		t.Errorf("api must not be touched: %+v", in)
	}
	// One instance can still be addressed by its own name or id.
	if _, err := m.Restart("queue-2"); err != nil {
		t.Fatal(err)
	}
	if in, _ := m.Get("queue-2"); in.Status != StatusOnline {
		t.Errorf("queue-2: %+v", in)
	}
	if in, _ := m.Get("queue-1"); in.Status != StatusStopped {
		t.Errorf("queue-1 must stay stopped: %+v", in)
	}
	// describe-style lookup of a group says what to do.
	if _, err := m.Get("queue"); err == nil || !strings.Contains(err.Error(), "group of 3 instances") || !strings.Contains(err.Error(), "queue-1") {
		t.Errorf("Get on a group: %v", err)
	}
	// Restart the group, then delete it.
	if infos, err := m.Restart("queue"); err != nil || len(infos) != 3 {
		t.Fatalf("restart queue = %v, %v", infos, err)
	}
	for _, in := range m.List() {
		if strings.HasPrefix(in.Name, "queue-") && in.Status != StatusOnline {
			t.Errorf("%s is %s after restarting the group", in.Name, in.Status)
		}
	}
	if infos, err := m.Delete("queue"); err != nil || len(infos) != 3 {
		t.Fatalf("delete queue = %v, %v", infos, err)
	}
	var names []string
	for _, in := range m.List() {
		names = append(names, in.Name)
	}
	if strings.Join(names, ",") != "api" {
		t.Errorf("after deleting the group: %v", names)
	}
	if _, err := m.Stop("queue"); err == nil {
		t.Error("the group is gone")
	}
}

func TestGroupRestartIsRolling(t *testing.T) {
	m, dir := newTestManager(t)
	log := filepath.Join(dir, "order.txt")
	// A stop (SIGTERM) is logged with a timestamp and then takes 0.3 s, so a
	// restart that went through the instances at once would show three stops
	// within a few milliseconds, and one that goes one by one shows them 0.3 s apart.
	cmd := `trap 'echo "stop $PROCLY_NAME $(date +%s%N)" >> ` + log + `; sleep 0.3; exit 0' TERM; echo "start $PROCLY_NAME" >> ` + log + `; while true; do sleep 0.1; done`
	startGroup(t, m, "web", cmd, 3)
	eventually(t, "all three to start", func() bool { b, _ := os.ReadFile(log); return strings.Count(string(b), "start ") == 3 })
	os.Remove(log)

	began := time.Now()
	if _, err := m.Restart("web"); err != nil {
		t.Fatal(err)
	}
	took := time.Since(began)
	eventually(t, "the restarted instances", func() bool { b, _ := os.ReadFile(log); return strings.Count(string(b), "start ") == 3 })

	var stops []int64
	var names []string
	b, _ := os.ReadFile(log)
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if f := strings.Fields(l); len(f) == 3 && f[0] == "stop" {
			ns, _ := strconv.ParseInt(f[2], 10, 64)
			stops, names = append(stops, ns), append(names, f[1])
		}
	}
	if strings.Join(names, ",") != "web-1,web-2,web-3" {
		t.Fatalf("instances are stopped in order, each once: %v", names)
	}
	for i := 1; i < len(stops); i++ {
		if gap := time.Duration(stops[i] - stops[i-1]); gap < 250*time.Millisecond {
			t.Errorf("%s was stopped only %v after %s: a group restart goes one at a time", names[i], gap, names[i-1])
		}
	}
	if took < 800*time.Millisecond {
		t.Errorf("three slow stops one after another take about a second, this took %v", took)
	}
}

func TestNamesCannotClashWithAGroup(t *testing.T) {
	m, _ := newTestManager(t)
	startGroup(t, m, "queue", "sleep 30", 2)
	if _, err := m.Start(spec(t, "queue", "sleep 30")); err == nil || !strings.Contains(err.Error(), "group of 2 instances") {
		t.Errorf("a plain process named like a group: %v", err)
	}
	m.Start(spec(t, "cache", "sleep 30"))
	specs, _ := Expand(Spec{Name: "cache", Command: "sleep 30", Cwd: t.TempDir(), Instances: 2})
	if _, err := m.Start(specs[0]); err == nil || !strings.Contains(err.Error(), "already a process") {
		t.Errorf("a group named like a process: %v", err)
	}
}

func TestGroupSurvivesSaveAndResurrect(t *testing.T) {
	a, dir := newTestManager(t)
	startGroup(t, a, "queue", "sleep 30", 3)
	dump := filepath.Join(dir, "dump.json")
	if n, err := a.Save(dump); err != nil || n != 3 {
		t.Fatalf("save: %d %v", n, err)
	}
	a.StopAll()

	b, _ := newTestManager(t)
	infos, err := b.Resurrect(dump)
	if err != nil || len(infos) != 3 {
		t.Fatalf("resurrect: %v %v", infos, err)
	}
	for i, in := range infos {
		if in.Spec.Group != "queue" || in.Spec.Index != i || in.Spec.Instances != 3 || in.Status != StatusOnline {
			t.Errorf("instance %d after resurrect: %+v", i, in.Spec)
		}
	}
	// It is still a group: its base name still addresses all three.
	if got, err := b.Stop("queue"); err != nil || len(got) != 3 {
		t.Errorf("stop queue after resurrect = %v, %v", got, err)
	}
}

func TestViewKnowsTheGroup(t *testing.T) {
	v := NewView(Info{Name: "queue-2", Spec: Spec{Name: "queue-2", Command: "x", Cwd: "/", Group: "queue", Index: 1, Instances: 3}})
	if v.Group != "queue" || v.Index != 1 || v.Instances != 3 {
		t.Errorf("view: %+v", v)
	}
	if p := NewView(Info{Name: "api", Spec: Spec{Name: "api", Command: "x", Cwd: "/"}}); p.Group != "" || p.Instances != 0 {
		t.Errorf("a plain process has no group: %+v", p)
	}
}

func TestInstanceSpecValidation(t *testing.T) {
	s := Spec{Name: "q-1", Command: "x", Cwd: "/", Group: "q", Index: 0, Instances: 1}
	if err := s.Validate(); err == nil {
		t.Error("an instance of a group of one is not valid")
	}
	s.Instances, s.Index = 3, 3
	if err := s.Validate(); err == nil {
		t.Error("an index past the end is not valid")
	}
	s.Index = 2
	if err := s.Validate(); err != nil {
		t.Errorf("a good instance: %v", err)
	}
}
