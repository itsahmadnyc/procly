package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/itsahmadnyc/procly/internal/proc"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func send(m model, msgs ...tea.Msg) (model, tea.Cmd) {
	var cmd tea.Cmd
	for _, msg := range msgs {
		var next tea.Model
		next, cmd = m.Update(msg)
		m = next.(model)
	}
	return m, cmd
}

func infos(t *testing.T) []proc.Info {
	dir := t.TempDir()
	mk := func(id int, name string, st proc.Status, pid int) proc.Info {
		return proc.Info{ID: id, Name: name, Status: st, PID: pid, StartedAt: time.Now().Add(-time.Minute),
			CPU: 1.5, Memory: 40 << 20, OutLog: filepath.Join(dir, name+"-out.log"), ErrLog: filepath.Join(dir, name+"-error.log"),
			Spec: proc.Spec{Command: "node " + name + ".js", Cwd: "/srv/" + name}}
	}
	return []proc.Info{mk(0, "api", proc.StatusOnline, 100), mk(1, "web", proc.StatusOnline, 101), mk(2, "mailer", proc.StatusErrored, 0)}
}

func loaded(t *testing.T) model {
	m, _ := send(newModel(nil), tea.WindowSizeMsg{Width: 100, Height: 30}, listMsg{infos: infos(t)})
	return m
}

func TestSelectionKeys(t *testing.T) {
	m := loaded(t)
	if m.sel.name != "api" {
		t.Fatalf("first process should be selected, got %q", m.sel.name)
	}
	steps := []struct{ key, want string }{
		{"j", "web"}, {"down", "mailer"}, {"j", "mailer"}, // stops at the end
		{"k", "web"}, {"g", "api"}, {"G", "mailer"},
	}
	for _, s := range steps {
		m, _ = send(m, key(s.key))
		if m.sel.name != s.want {
			t.Fatalf("after %q: selected %q, want %q", s.key, m.sel.name, s.want)
		}
	}
}

func TestSelectionFollowsTheProcessNotThePosition(t *testing.T) {
	m := loaded(t)
	m, _ = send(m, key("j")) // web
	reordered := infos(t)
	reordered[0], reordered[1] = reordered[1], reordered[0]
	m, _ = send(m, listMsg{infos: reordered})
	if m.sel.name != "web" {
		t.Fatalf("selection jumped to %q when the list changed order", m.sel.name)
	}
	m, _ = send(m, listMsg{infos: infos(t)[2:]}) // web is gone
	if m.sel.name != "mailer" {
		t.Fatalf("after the selected process vanished: %q", m.sel.name)
	}
	m, _ = send(m, listMsg{infos: nil})
	if m.sel.name != "" || !strings.Contains(m.View(), "No processes yet") {
		t.Fatalf("empty list: sel %q\n%s", m.sel.name, m.View())
	}
}

func TestDeleteNeedsConfirmation(t *testing.T) {
	m := loaded(t)
	m, cmd := send(m, key("d"))
	if m.confirm != "api" || cmd != nil || !strings.Contains(m.View(), "delete api?") {
		t.Fatalf("d should ask first: confirm=%q", m.confirm)
	}
	m, cmd = send(m, key("n"))
	if m.confirm != "" || cmd != nil || m.flash != "delete cancelled" {
		t.Fatalf("any other key cancels: confirm=%q flash=%q", m.confirm, m.flash)
	}
	m, _ = send(m, key("x"))
	m, cmd = send(m, key("y"))
	if cmd == nil || m.busy["api"] != "delete" {
		t.Fatalf("y should delete: busy=%v", m.busy)
	}
	if !strings.Contains(m.View(), "deleting") {
		t.Fatal("a busy process should say what is happening")
	}
}

func TestActionsAndResults(t *testing.T) {
	m := loaded(t)
	m, none := send(m, key("r"))
	if none != nil || len(m.busy) != 0 || m.confirmVerb != "restart" {
		t.Fatalf("r should only ask: busy=%v", m.busy)
	}
	m, cmd := send(m, key("n")) // anything but y cancels
	if cmd != nil || len(m.busy) != 0 || m.confirm != "" {
		t.Fatal("n should cancel")
	}
	m, _ = send(m, key("r"))
	m, cmd = send(m, key("y"))
	if cmd == nil || m.busy["api"] != "restart" {
		t.Fatalf("r then y: busy=%v", m.busy)
	}
	m, _ = send(m, key("r"))
	m, again := send(m, key("y"))
	if again != nil {
		t.Fatal("a second action while one is running should be ignored")
	}
	m, _ = send(m, actionMsg{verb: "restart", name: "api"})
	if len(m.busy) != 0 || m.flash != "restarted api" || m.flashErr {
		t.Fatalf("after success: busy=%v flash=%q", m.busy, m.flash)
	}
	m, _ = send(m, actionMsg{verb: "stop", name: "api", err: errors.New("boom")})
	if m.flash != "boom" || !m.flashErr {
		t.Fatalf("after failure: flash=%q err=%v", m.flash, m.flashErr)
	}

	// s stops a running process and starts a stopped one.
	m, _ = send(m, key("s"), key("y"))
	if m.busy["api"] != "stop" {
		t.Fatalf("s on online: %v", m.busy)
	}
	m, _ = send(m, key("G"), key("s"))
	if m.busy["mailer"] != "start" {
		t.Fatalf("s on errored: %v", m.busy)
	}
}

func TestHelpZoomAndQuit(t *testing.T) {
	m := loaded(t)
	m, _ = send(m, key("?"))
	if !m.help || !strings.Contains(m.View(), "press any key to close") {
		t.Fatal("? should open help")
	}
	m, _ = send(m, key("j"))
	if m.help || m.sel.name != "api" {
		t.Fatal("the key that closes help must not also act")
	}
	m, _ = send(m, key("enter"))
	if !m.zoom || strings.Contains(m.View(), "restarts") {
		t.Fatal("enter should give the logs the whole screen")
	}
	m, _ = send(m, key("esc"))
	if m.zoom {
		t.Fatal("esc should leave zoom")
	}
	for _, k := range []string{"q", "ctrl+c"} {
		_, cmd := send(loaded(t), key(k))
		if cmd == nil {
			t.Fatalf("%s should quit", k)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%s returned a non-quit command", k)
		}
	}
	// ctrl+c quits even from a confirmation prompt.
	m, _ = send(loaded(t), key("d"))
	if _, cmd := send(m, key("ctrl+c")); cmd == nil {
		t.Fatal("ctrl+c during confirm should quit")
	}
}

func TestHistoryStartsOverOnRestart(t *testing.T) {
	m := loaded(t)
	m, _ = send(m, listMsg{infos: infos(t)}, listMsg{infos: infos(t)})
	if len(m.cpu["api"]) != 3 {
		t.Fatalf("one sample per refresh: %d", len(m.cpu["api"]))
	}
	if _, ok := m.cpu["mailer"]; ok {
		t.Fatal("a process that isn't running has no samples")
	}
	restarted := infos(t)
	restarted[0].PID = 555
	m, _ = send(m, listMsg{infos: restarted})
	if len(m.cpu["api"]) != 1 {
		t.Fatalf("a new pid should start a fresh graph, got %d samples", len(m.cpu["api"]))
	}
	for i := 0; i < historyLen+10; i++ {
		m, _ = send(m, listMsg{infos: restarted})
	}
	if len(m.cpu["api"]) != historyLen {
		t.Fatalf("history should be capped at %d, got %d", historyLen, len(m.cpu["api"]))
	}
}

func TestLogPaneFollowsTheSelectedProcess(t *testing.T) {
	list := infos(t)
	if err := os.WriteFile(list[0].OutLog, []byte("api line 1\napi line 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(list[0].ErrLog, []byte("api error\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(list[1].OutLog, []byte("web line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ := send(newModel(nil), tea.WindowSizeMsg{Width: 100, Height: 30}, listMsg{infos: list})
	if got := lines(m); got != "api line 1|api line 2|!api error" {
		t.Fatalf("history: %s", got)
	}

	f, _ := os.OpenFile(list[0].OutLog, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString("api line 3\n")
	f.Close()
	m, _ = send(m, logTickMsg{})
	if !strings.HasSuffix(lines(m), "api line 3") {
		t.Fatalf("new line not picked up: %s", lines(m))
	}
	if !strings.Contains(m.View(), "api line 3") {
		t.Fatal("new line not on screen")
	}

	m, _ = send(m, key("j"))
	if got := lines(m); got != "web line" {
		t.Fatalf("after switching to web: %s", got)
	}
	m, _ = send(m, key("c"))
	if len(m.logs) != 0 {
		t.Fatal("c should clear the pane")
	}
}

func lines(m model) string {
	var out []string
	for _, l := range m.logs {
		if l.stderr {
			out = append(out, "!"+l.text)
		} else {
			out = append(out, l.text)
		}
	}
	return strings.Join(out, "|")
}

func TestViewFitsAnyTerminal(t *testing.T) {
	long := infos(t)
	long[0].Spec.Command = strings.Repeat("very-long-command ", 20)
	for _, size := range [][2]int{{30, 8}, {60, 16}, {100, 30}, {220, 60}} {
		m, _ := send(newModel(nil), tea.WindowSizeMsg{Width: size[0], Height: size[1]}, listMsg{infos: long})
		for _, mode := range []string{"normal", "zoom", "help"} {
			mm := m
			switch mode {
			case "zoom":
				mm, _ = send(mm, key("enter"))
			case "help":
				mm, _ = send(mm, key("?"))
			}
			view := mm.View()
			got := strings.Split(view, "\n")
			if (mode != "help" && len(got) != size[1]) || len(got) > size[1] {
				t.Errorf("%dx%d %s: %d lines for a %d-line terminal", size[0], size[1], mode, len(got), size[1])
			}
			for i, l := range got {
				if w := lipgloss.Width(l); w > size[0] {
					t.Errorf("%dx%d %s: line %d is %d wide", size[0], size[1], mode, i, w)
				}
			}
		}
	}
}

func TestViewStates(t *testing.T) {
	m := newModel(nil)
	if m.View() != "" {
		t.Fatal("nothing to draw before the terminal size is known")
	}
	m, _ = send(m, tea.WindowSizeMsg{Width: 80, Height: 20})
	if !strings.Contains(m.View(), "connecting") {
		t.Fatal("before the first list arrives")
	}
	m, _ = send(m, listMsg{err: errors.New("dial unix: no such file")})
	if !strings.Contains(m.View(), "lost connection to the daemon") {
		t.Fatal("daemon errors must be shown")
	}
	m, _ = send(m, listMsg{infos: infos(t)})
	view := m.View()
	for _, want := range []string{"3 processes", "2 online", "1 errored", "api", "$ node api.js", "Logs · api"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q", want)
		}
	}
}

func TestModeAsksBeforeSwitching(t *testing.T) {
	m := loaded(t)
	m, cmd := send(m, key("m"))
	if cmd != nil || m.confirm == "" || (m.confirmVerb != "cluster" && m.confirmVerb != "fork") {
		t.Fatalf("m should ask: %q %q", m.confirm, m.confirmVerb)
	}
	if !strings.Contains(m.View(), "mode?") {
		t.Fatal("the footer should say what will happen")
	}
	m, cmd = send(m, key("x"))
	if cmd != nil || len(m.busy) != 0 {
		t.Fatal("any other key cancels")
	}
}
