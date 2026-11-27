package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/itsahmadnyc/procly/internal/proc"
)

func richViews() []proc.View {
	v := func(id int, name string, st proc.Status, pid int, cpu float64, mem int64) proc.View {
		return proc.View{ID: id, Name: name, Status: st, PID: pid, CPU: cpu, Memory: mem, UptimeMs: 90_000, AutoRestart: true}
	}
	var out []proc.View
	for i := 0; i < 3; i++ {
		m := v(i, "api-"+string(rune('1'+i)), proc.StatusOnline, 100+i, 12.5, 50<<20)
		m.Group, m.Index, m.Instances, m.MaxMemory = "api", i, 3, 200<<20
		out = append(out, m)
	}
	gw := v(3, "gateway", proc.StatusOnline, 200, 3, 120<<20)
	gw.HealthCheck, gw.Health = "http://127.0.0.1:3000/", proc.HealthHealthy
	bad := v(4, "billing", proc.StatusErrored, 0, 0, 0)
	bad.Restarts, bad.ExitCode = 15, 1
	sick := v(5, "worker-with-a-rather-long-name", proc.StatusOnline, 300, 1, 40<<20)
	sick.HealthCheck, sick.Health = "tcp://127.0.0.1:9", proc.HealthUnhealthy
	idle := v(6, "cron", proc.StatusStopped, 0, 0, 0)
	return append(out, gw, bad, sick, idle)
}

func richOut(infos []proc.View, server string, cols int) string {
	var b bytes.Buffer
	printRichList(&b, infos, server, cols)
	return b.String()
}

func TestRichListFitsEveryWidthAndTheBoxIsSquare(t *testing.T) {
	for cols := 36; cols <= 170; cols += 7 {
		out := richOut(richViews(), "", cols)
		var box []int
		for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if w := width(line); w > cols {
				t.Fatalf("cols=%d: line %d is %d wide: %q\n%s", cols, i, w, line, out)
			}
			if strings.HasPrefix(line, "  ╭") || strings.HasPrefix(line, "  │") || strings.HasPrefix(line, "  ╰") {
				box = append(box, width(line))
			}
		}
		for _, w := range box {
			if w != box[0] {
				t.Fatalf("cols=%d: the frame is not square (%v):\n%s", cols, box, out)
			}
		}
		if len(box) < 5 {
			t.Fatalf("cols=%d: no frame:\n%s", cols, out)
		}
	}
}

func TestRichListShowsGroupsAsATreeAndSaysWhatIsWrong(t *testing.T) {
	out := richOut(richViews(), "", 130)
	for _, want := range []string{"╭─ procly ", "▾ api ×3", "├ api-1", "├ api-2", "└ api-3", "✓ healthy", "✗ unhealthy",
		"● 3 online", "● errored", "● stopped", "7 processes", "5 online",
		"✗ billing gave up after 15 restarts (exit code 1)  procly logs billing",
		"! worker-with-a-rather-long-name is failing its health check"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestRichListDropsColumnsInOrderAndKeepsTheEssentials(t *testing.T) {
	wide := richOut(richViews(), "", 160)
	for _, col := range []string{"pid", "uptime", "restarts", "health", "memory", "cpu", "id"} {
		if !strings.Contains(wide, col) {
			t.Errorf("a wide terminal shows %q:\n%s", col, wide)
		}
	}
	narrow := richOut(richViews(), "", 50)
	header := strings.Split(narrow, "\n")[2]
	for _, gone := range []string{"pid", "uptime", "health"} {
		if strings.Contains(header, gone) {
			t.Errorf("a narrow terminal drops %q:\n%s", gone, narrow)
		}
	}
	for _, kept := range []string{"name", "status", "cpu", "memory"} {
		if !strings.Contains(header, kept) {
			t.Errorf("a narrow terminal keeps %q:\n%s", kept, narrow)
		}
	}
}

func TestRichListMentionsTheServerAndOrphans(t *testing.T) {
	orphan := []proc.View{{ID: 0, Name: "old", Status: proc.StatusOrphaned, PID: 5, AutoRestart: true}}
	out := richOut(orphan, "prod-1", 100)
	for _, want := range []string{"procly ", " on prod-1 ", "● orphaned", "! 1 process is left over from a daemon that died"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestPlainListIsUnchangedOffATerminal(t *testing.T) {
	var b bytes.Buffer
	printList(&b, richViews(), "")
	out := b.String()
	if strings.ContainsAny(out, "╭╰│") {
		t.Fatalf("a pipe or a file gets the plain table:\n%s", out)
	}
	for _, want := range []string{"id", "name", "status", "restarts", "api ×3", "api-1", "gateway"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain list is missing %q:\n%s", want, out)
		}
	}
}
