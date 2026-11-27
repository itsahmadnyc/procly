package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/itsahmadnyc/procly/internal/proc"
)

// withGroup is infos plus a group "queue" of three instances.
func withGroup(t *testing.T) []proc.Info {
	list := infos(t)
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		name := "queue-" + string(rune('1'+i))
		list = append(list, proc.Info{ID: 3 + i, Name: name, Status: proc.StatusOnline, PID: 200 + i, StartedAt: time.Now(),
			CPU: float64(10 * (i + 1)), Memory: 10 << 20,
			OutLog: filepath.Join(dir, name+"-out.log"), ErrLog: filepath.Join(dir, name+"-error.log"),
			Spec: proc.Spec{Name: name, Command: "node queue.js", Group: "queue", Index: i, Instances: 3}})
	}
	return list
}

func loadedWith(t *testing.T, list []proc.Info, w, h int) model {
	m, _ := send(newModel(nil), tea.WindowSizeMsg{Width: w, Height: h}, listMsg{infos: list})
	return m
}

func rowNames(m model) []string {
	var out []string
	for _, r := range m.rows {
		if r.v == nil {
			out = append(out, "["+r.group+"]")
		} else {
			out = append(out, r.v.Name)
		}
	}
	return out
}

func TestGroupsShowTogetherAndFold(t *testing.T) {
	m := loadedWith(t, withGroup(t), 100, 30)
	if got := strings.Join(rowNames(m), " "); got != "api web mailer [queue] queue-1 queue-2 queue-3" {
		t.Fatalf("rows: %s", got)
	}
	m, _ = send(m, key("G"))                      // queue-3
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyLeft}) // to its group's line
	if m.sel != (selection{"queue", true}) {
		t.Fatalf("left on a member should go to its group, got %+v", m.sel)
	}
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyLeft}) // fold
	if got := strings.Join(rowNames(m), " "); got != "api web mailer [queue]" {
		t.Fatalf("folded: %s", got)
	}
	if !strings.Contains(m.View(), "▸ queue ×3") {
		t.Fatal("a folded group should say so")
	}
	m, _ = send(m, key(" "))
	if len(m.rows) != 7 {
		t.Fatalf("space should unfold: %v", rowNames(m))
	}

	// Actions on the group's line go to the base name, which means all of them.
	m, cmd := send(m, key("r"))
	if cmd == nil || m.busy["queue"] != "restart" {
		t.Fatalf("restart on a group: %v", m.busy)
	}
	if !strings.Contains(m.View(), "◌ restarting") {
		t.Fatal("members of a busy group should show it")
	}
	m, _ = send(m, actionMsg{verb: "restart", name: "queue"})
	m, _ = send(m, key("d"))
	if m.confirm != "queue" || !strings.Contains(m.View(), "delete all 3 instances of queue?") {
		t.Fatalf("delete on a group should name its instances: %q", m.confirm)
	}
}

func TestGroupLogsMergeItsInstances(t *testing.T) {
	list := withGroup(t)
	for i, name := range []string{"queue-1", "queue-2", "queue-3"} {
		if err := os.WriteFile(list[3+i].OutLog, []byte(name+" a\n"+name+" b\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := loadedWith(t, list, 100, 30)
	m, _ = send(m, key("G"), tea.KeyMsg{Type: tea.KeyLeft}) // the group
	if got := lines(m); got != "queue-1 a|queue-2 a|queue-3 a|queue-1 b|queue-2 b|queue-3 b" {
		t.Fatalf("history of a group should interleave its instances: %s", got)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "queue-2 queue-2 b") {
		t.Fatalf("each line should name its instance:\n%s", view)
	}
}

func TestFilterProcesses(t *testing.T) {
	m := loadedWith(t, withGroup(t), 100, 30)
	m, _ = send(m, key("/"), key("q"), key("u"), key("e"), key("u"), key("e"), key("-"), key("2"))
	if got := strings.Join(rowNames(m), " "); got != "[queue] queue-2" {
		t.Fatalf("filter applies as you type: %s", got)
	}
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.input != inputNone || m.procFilter != "queue-2" {
		t.Fatal("enter keeps the filter")
	}
	if !strings.Contains(m.View(), "/queue-2") {
		t.Fatal("the table should show the filter")
	}
	m, _ = send(m, key("/"), key("x"), key("y"), key("z"))
	if len(m.rows) != 0 || !strings.Contains(m.View(), "no process matches") {
		t.Fatalf("no match: %v", rowNames(m))
	}
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.procFilter != "" || len(m.rows) != 7 {
		t.Fatal("esc while typing clears the filter")
	}
}

func TestSortCycles(t *testing.T) {
	list := withGroup(t)
	list[1].CPU = 90 // web
	m := loadedWith(t, list, 100, 30)
	m, _ = send(m, key("o"), key("o")) // name, then cpu
	if m.sortBy != sortCPU {
		t.Fatalf("sort: %v", m.sortBy)
	}
	// queue sums 10+20+30 = 60, web 90, api 1.5, mailer is not running.
	if got := strings.Join(rowNames(m), " "); !strings.HasPrefix(got, "web [queue] queue-1 queue-2 queue-3 api") {
		t.Fatalf("by cpu: %s", got)
	}
	if !strings.Contains(m.View(), "by cpu ↓") {
		t.Fatal("the table should say how it is sorted")
	}
}

func writeLog(t *testing.T, path string, ls ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range ls {
		_, _ = f.WriteString(l + "\n")
	}
}

func TestLogSearchStreamAndWrap(t *testing.T) {
	list := infos(t)
	writeLog(t, list[0].OutLog, "GET /health 200", "GET /users 500", "\x1b[31mcolored\x1b[0m\tline")
	writeLog(t, list[0].ErrLog, "error: users failed")
	m := loadedWith(t, list, 100, 30)
	if !strings.Contains(lines(m), "colored    line") {
		t.Fatalf("escape codes and tabs should be cleaned: %s", lines(m))
	}

	m, _ = send(m, tea.KeyMsg{Type: tea.KeyTab}, key("/"), key("u"), key("s"), key("e"), key("r"), key("s"), tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.visibleLogs()) != 2 || !strings.Contains(m.View(), "/users 2 matches") {
		t.Fatalf("search: %d lines", len(m.visibleLogs()))
	}
	m, _ = send(m, key("e")) // stderr only
	if v := m.visibleLogs(); len(v) != 1 || !v[0].stderr {
		t.Fatalf("stderr only: %v", v)
	}
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyEsc}) // clears the search
	if m.logFilter != "" || len(m.visibleLogs()) != 1 {
		t.Fatal("esc in the logs clears the search, the stream filter stays")
	}
	m, _ = send(m, key("e"), key("e")) // back to both
	if len(m.visibleLogs()) != 4 {
		t.Fatalf("both streams: %d", len(m.visibleLogs()))
	}

	if got := highlight("Users and USERS", "users"); ansi.Strip(got) != "Users and USERS" || got == "Users and USERS" && colorsOn() {
		t.Fatalf("highlight must keep the text: %q", got)
	}
}

// colorsOn reports whether lipgloss renders styles in this test run.
func colorsOn() bool { return sMatch.Render("x") != "x" }

func TestLogsPauseAndFollow(t *testing.T) {
	list := infos(t)
	for i := 0; i < 50; i++ {
		writeLog(t, list[0].OutLog, "line "+string(rune('A'+i%26)))
	}
	m := loadedWith(t, list, 100, 30)
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyPgUp})
	if m.logOff == 0 || !strings.Contains(m.View(), "paused") {
		t.Fatalf("page up should pause: off=%d", m.logOff)
	}
	off := m.logOff
	writeLog(t, list[0].OutLog, "new one", "new two")
	m, _ = send(m, logTickMsg{})
	if m.logOff != off+2 || m.unseen != 2 || !strings.Contains(m.View(), "2 new") {
		t.Fatalf("new lines while paused keep the view still: off %d->%d unseen %d", off, m.logOff, m.unseen)
	}
	m, _ = send(m, key("f"))
	if m.logOff != 0 || m.unseen != 0 || !strings.Contains(m.View(), "live") {
		t.Fatal("f follows again")
	}
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyTab}, key("g"))
	if want := len(m.logs) - (m.layout().logs.h - 2); m.logOff != want {
		t.Fatalf("g in the logs puts the oldest line at the top of a full pane: offset %d, want %d", m.logOff, want)
	}
	m, _ = send(m, key("G"))
	if m.logOff != 0 {
		t.Fatal("G in the logs follows")
	}
}

func TestReloadNeedsASignal(t *testing.T) {
	list := infos(t)
	list[1].Spec.ReloadSignal = "SIGHUP"
	m := loadedWith(t, list, 100, 30)
	m, cmd := send(m, key("R"))
	if cmd != nil || !m.flashErr || !strings.Contains(m.flash, "no reload_signal") {
		t.Fatalf("R without reload_signal: %q", m.flash)
	}
	m, cmd = send(m, key("j"), key("R"))
	if cmd == nil || m.busy["web"] != "reload" {
		t.Fatalf("R with a signal: %v", m.busy)
	}
	m, _ = send(m, actionMsg{verb: "reload", name: "web"})
	if m.flash != "reloaded web" {
		t.Fatalf("flash: %q", m.flash)
	}
}

func TestMouse(t *testing.T) {
	m := loadedWith(t, withGroup(t), 100, 30)
	l := m.layout()
	click := tea.MouseMsg{X: 5, Y: l.table.y + 2 + 2, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	m, _ = send(m, click)
	if m.sel.name != "mailer" {
		t.Fatalf("click on the third row: %+v", m.sel)
	}
	m, _ = send(m, tea.MouseMsg{X: 5, Y: l.table.y + 2 + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	m, _ = send(m, tea.MouseMsg{X: 5, Y: l.table.y + 2 + 3, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if !m.collapsed["queue"] {
		t.Fatal("a second click on a group's line folds it")
	}
	if !l.logs.has(5, l.logs.y+1) {
		t.Fatalf("layout has no logs: %+v", l)
	}
	m, _ = send(m, tea.MouseMsg{X: 5, Y: l.logs.y + 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.focus != paneLogs {
		t.Fatal("a click in the logs focuses them")
	}
}

func TestLayoutAdapts(t *testing.T) {
	wide := loadedWith(t, withGroup(t), 160, 45).layout()
	if wide.detail.x == 0 || wide.detail.y != wide.table.y {
		t.Fatalf("wide terminals put the details beside the table: %+v", wide)
	}
	narrow := loadedWith(t, withGroup(t), 80, 30).layout()
	if narrow.detail.x != 0 || narrow.detail.y <= narrow.table.y {
		t.Fatalf("narrow terminals stack them: %+v", narrow)
	}
	tiny := loadedWith(t, withGroup(t), 40, 10).layout()
	if tiny.detail.h != 0 {
		t.Fatalf("tiny terminals drop the details: %+v", tiny)
	}
	// Columns give way on narrow terminals, the name and status stay.
	view := ansi.Strip(loadedWith(t, withGroup(t), 44, 20).View())
	if !strings.Contains(view, "queue-1") || !strings.Contains(view, "online") || strings.Contains(view, "pid") {
		t.Fatalf("narrow table:\n%s", view)
	}
}

func TestInterleave(t *testing.T) {
	a := []logLine{{text: "a1"}, {text: "a2"}, {text: "a3"}, {text: "a4"}}
	b := []logLine{{text: "b1"}, {text: "b2"}}
	var got []string
	for _, l := range interleave([][]logLine{a, b}) {
		got = append(got, l.text)
	}
	if strings.Join(got, " ") != "a1 a2 b1 a3 a4 b2" {
		t.Fatalf("interleave: %v", got)
	}
}

func TestHostLine(t *testing.T) {
	h := hostStats{cpu: 42, memUsed: 3 << 30, memTotal: 8 << 30, load1: 0.5, cores: 4}
	if got := ansi.Strip(h.line(100)); got != "cpu 42%  mem 3.0G/8.0G  load 0.50" {
		t.Fatalf("line: %q", got)
	}
	if got := ansi.Strip(h.line(22)); got != "cpu 42%  mem 3.0G/8.0G" {
		t.Fatalf("short: %q", got)
	}
	if newHostStats().line(100) != "" {
		t.Fatal("unknown stats show nothing")
	}
	_ = readHost() // must not panic on this machine
}

func TestKeysThatArriveTogether(t *testing.T) {
	m := loadedWith(t, withGroup(t), 100, 30)
	m, _ = send(m, key("jjj"))
	if m.sel != (selection{"queue", true}) {
		t.Fatalf("three j's in one message should move three rows, at %+v", m.sel)
	}
	m, cmd := send(m, key("jq"))
	if cmd == nil {
		t.Fatal("a q among other keys should still quit")
	}
}

func TestScrollSpeedsUpTheLongerItGoesOn(t *testing.T) {
	clock := time.Unix(1000, 0)
	now = func() time.Time { return clock }
	defer func() { now = time.Now }()

	var m model
	if got := m.stepOf(3); got != 3 {
		t.Fatalf("a single wheel step is 3 lines, got %d", got)
	}
	var steps []int
	for i := 0; i < 60; i++ {
		clock = clock.Add(20 * time.Millisecond) // a trackpad flick: 50 events a second
		steps = append(steps, m.stepOf(1))
	}
	for i := 1; i < len(steps); i++ {
		if steps[i] < steps[i-1] {
			t.Fatalf("a long scroll never slows down: %v", steps)
		}
	}
	if steps[0] != 1 || steps[len(steps)-1] != scrollMax {
		t.Fatalf("starts at one line and ends at %d times that: %v", scrollMax, steps)
	}
	clock = clock.Add(2 * scrollGap) // the user stops, then scrolls again
	if got := m.stepOf(1); got != 1 {
		t.Fatalf("a pause starts the scroll over, got %d", got)
	}
	clock = clock.Add(scrollGap + 1)
	if got := m.stepOf(1); got != 1 {
		t.Fatalf("slow single steps stay single steps, got %d", got)
	}
}

func TestHeldArrowKeyReachesTheTopInFewerPresses(t *testing.T) {
	clock := time.Unix(1000, 0)
	now = func() time.Time { return clock }
	defer func() { now = time.Now }()

	list := infos(t)
	for i := 0; i < 400; i++ {
		writeLog(t, list[0].OutLog, "line "+string(rune('A'+i%26)))
	}
	m := loadedWith(t, list, 100, 30)
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyTab})
	presses := 0
	for m.logOff < m.maxLogOff() && presses < 1000 {
		clock = clock.Add(30 * time.Millisecond) // key repeat
		m, _ = send(m, tea.KeyMsg{Type: tea.KeyUp})
		presses++
	}
	if presses >= m.maxLogOff() {
		t.Fatalf("holding the key should be faster than one line a press: %d presses for %d lines", presses, m.maxLogOff())
	}
	if m.logOff != m.maxLogOff() {
		t.Fatalf("it still stops exactly at the oldest line: %d of %d", m.logOff, m.maxLogOff())
	}
}

func TestFrameRate(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "")
	t.Setenv("SSH_TTY", "")
	t.Setenv("PROCLY_FPS", "")
	if got := frameRate(); got != 30 {
		t.Errorf("locally: %d", got)
	}
	t.Setenv("SSH_CONNECTION", "10.0.0.1 5000 10.0.0.2 22")
	if got := frameRate(); got != 8 {
		t.Errorf("over ssh the screen is sent less often: %d", got)
	}
	t.Setenv("PROCLY_FPS", "45")
	if got := frameRate(); got != 45 {
		t.Errorf("PROCLY_FPS wins: %d", got)
	}
	for _, bad := range []string{"0", "100", "fast", "-3"} {
		t.Setenv("PROCLY_FPS", bad)
		if got := frameRate(); got != 8 {
			t.Errorf("PROCLY_FPS=%q is ignored, got %d", bad, got)
		}
	}
}
