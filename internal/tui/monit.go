// Package tui implements `procly monit`, a live terminal dashboard.
package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/itsahmadnyc/procly/internal/client"
	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/version"
)

const (
	refreshEvery = time.Second
	logEvery     = 250 * time.Millisecond
	historyLen   = 60
	logKeep      = 1000
	sparkWidth   = 24
)

// Run opens the dashboard and blocks until the user quits.
func Run(c *client.Client) error {
	// Bubble Tea already asked the terminal for its background color during
	// init. PROCLY_THEME overrides that for terminals that answer wrongly.
	switch strings.ToLower(os.Getenv("PROCLY_THEME")) {
	case "light":
		lipgloss.SetHasDarkBackground(false)
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	}
	_, err := tea.NewProgram(newModel(c), tea.WithAltScreen()).Run()
	return err
}

type (
	tickMsg    struct{}
	logTickMsg struct{}
	listMsg    struct {
		infos []proc.Info
		err   error
	}
	actionMsg struct {
		verb, name string
		err        error
	}
)

type logLine struct {
	text   string
	stderr bool
}

type model struct {
	c       *client.Client
	infos   []proc.Info
	err     error
	loaded  bool
	selName string
	w, h    int

	cpu  map[string][]float64
	mem  map[string][]float64
	pids map[string]int // pid the history belongs to; a new pid starts a fresh graph

	logName string
	logs    []logLine
	outF    *logtail.Follower
	errF    *logtail.Follower

	zoom     bool
	help     bool
	confirm  string            // process waiting for delete confirmation
	busy     map[string]string // name -> verb in progress
	flash    string
	flashErr bool
}

func newModel(c *client.Client) model {
	return model{
		c:    c,
		cpu:  map[string][]float64{},
		mem:  map[string][]float64{},
		pids: map[string]int{},
		busy: map[string]string{},
	}
}

func (m model) Init() tea.Cmd { return tea.Batch(m.fetch(), logTick()) }

func (m model) fetch() tea.Cmd {
	c := m.c
	return func() tea.Msg {
		infos, err := c.List()
		return listMsg{infos, err}
	}
}

func tick() tea.Cmd {
	return tea.Tick(refreshEvery, func(time.Time) tea.Msg { return tickMsg{} })
}

func logTick() tea.Cmd {
	return tea.Tick(logEvery, func(time.Time) tea.Msg { return logTickMsg{} })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tickMsg:
		return m, m.fetch()
	case listMsg:
		m.loaded = true
		m.err = msg.err
		if msg.err == nil {
			m.infos = msg.infos
			m.record()
			m.keepSelection()
			m.syncLogs()
		}
		return m, tick()
	case logTickMsg:
		m.readLogs()
		return m, logTick()
	case actionMsg:
		delete(m.busy, msg.name)
		if msg.err != nil {
			m.flash, m.flashErr = msg.err.Error(), true
		} else {
			m.flash, m.flashErr = pastTense[msg.verb]+" "+msg.name, false
		}
		if msg.verb == "delete" && msg.name == m.logName {
			m.logName = "" // force a fresh log view for the next selection
		}
		return m, m.fetch()
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

var pastTense = map[string]string{"restart": "restarted", "start": "started", "stop": "stopped", "delete": "deleted"}
var progressive = map[string]string{"restart": "restarting", "start": "starting", "stop": "stopping", "delete": "deleting"}

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	if m.confirm != "" {
		name := m.confirm
		m.confirm = ""
		if s == "y" || s == "Y" {
			return m, m.act("delete", name)
		}
		m.flash, m.flashErr = "delete cancelled", false
		return m, nil
	}
	if m.help {
		m.help = false
		return m, nil
	}

	sel := m.selected()
	switch s {
	case "q":
		return m, tea.Quit
	case "esc":
		m.zoom = false
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "home", "g":
		m.move(-len(m.infos))
	case "end", "G":
		m.move(len(m.infos))
	case "enter", "l":
		m.zoom = !m.zoom
	case "?":
		m.help = true
	case "c":
		m.logs = nil
	case "r":
		if sel != nil {
			return m, m.act("restart", sel.Name)
		}
	case "s":
		if sel != nil {
			if sel.Status == proc.StatusOnline || sel.Status == proc.StatusWaiting || sel.Status == proc.StatusOrphaned {
				return m, m.act("stop", sel.Name)
			}
			return m, m.act("start", sel.Name)
		}
	case "d", "x":
		if sel != nil {
			m.confirm = sel.Name
		}
	}
	return m, nil
}

func (m model) act(verb, name string) tea.Cmd {
	if _, running := m.busy[name]; running {
		return nil
	}
	m.busy[name] = verb
	c := m.c
	return func() tea.Msg {
		var err error
		switch verb {
		case "restart", "start":
			_, err = c.Restart(name)
		case "stop":
			_, err = c.Stop(name)
		case "delete":
			_, err = c.Delete(name)
		}
		return actionMsg{verb, name, err}
	}
}

func (m *model) selIndex() int {
	for i, in := range m.infos {
		if in.Name == m.selName {
			return i
		}
	}
	return -1
}

func (m *model) selected() *proc.Info {
	if i := m.selIndex(); i >= 0 {
		return &m.infos[i]
	}
	return nil
}

func (m *model) move(delta int) {
	if len(m.infos) == 0 {
		return
	}
	i := min(max(m.selIndex()+delta, 0), len(m.infos)-1)
	m.selName = m.infos[i].Name
	m.syncLogs()
}

func (m *model) keepSelection() {
	if m.selIndex() >= 0 {
		return
	}
	if len(m.infos) == 0 {
		m.selName = ""
		return
	}
	m.selName = m.infos[0].Name
}

// record appends one CPU/memory sample per running process.
func (m *model) record() {
	seen := map[string]bool{}
	for _, in := range m.infos {
		seen[in.Name] = true
		if in.PID == 0 {
			continue
		}
		if m.pids[in.Name] != in.PID {
			m.pids[in.Name] = in.PID
			m.cpu[in.Name], m.mem[in.Name] = nil, nil
		}
		m.cpu[in.Name] = appendCapped(m.cpu[in.Name], in.CPU)
		m.mem[in.Name] = appendCapped(m.mem[in.Name], float64(in.Memory))
	}
	for name := range m.cpu {
		if !seen[name] {
			delete(m.cpu, name)
			delete(m.mem, name)
			delete(m.pids, name)
		}
	}
}

func appendCapped(s []float64, v float64) []float64 {
	s = append(s, v)
	if len(s) > historyLen {
		s = s[len(s)-historyLen:]
	}
	return s
}

// syncLogs points the log pane at the selected process.
func (m *model) syncLogs() {
	if m.selName == m.logName {
		return
	}
	m.logName, m.logs, m.outF, m.errF = m.selName, nil, nil, nil
	sel := m.selected()
	if sel == nil {
		return
	}
	out, outSize := logtail.Tail(sel.OutLog, 200)
	errs, errSize := logtail.Tail(sel.ErrLog, 200)
	// The two files have no shared clock, so history shows stdout then
	// stderr. New lines after this point appear in arrival order.
	for _, l := range out {
		m.logs = append(m.logs, logLine{text: l})
	}
	for _, l := range errs {
		m.logs = append(m.logs, logLine{text: l, stderr: true})
	}
	m.outF = logtail.NewFollower(sel.OutLog, outSize)
	m.errF = logtail.NewFollower(sel.ErrLog, errSize)
}

func (m *model) readLogs() {
	if m.outF == nil {
		return
	}
	for _, l := range m.outF.Read() {
		m.logs = append(m.logs, logLine{text: l})
	}
	for _, l := range m.errF.Read() {
		m.logs = append(m.logs, logLine{text: l, stderr: true})
	}
	if len(m.logs) > logKeep {
		m.logs = m.logs[len(m.logs)-logKeep:]
	}
}

// ---- view ----

func (m model) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	if m.help {
		return m.helpView()
	}

	top := []string{m.headerLine(), ""}
	var body []string
	switch {
	case m.err != nil:
		body = append(body, "  "+sRed.Render("lost connection to the daemon"), "  "+sMuted.Render(m.err.Error()),
			"", "  "+sMuted.Render("Is it running? Try: procly ls"))
	case !m.loaded:
		body = append(body, "  "+sMuted.Render("connecting…"))
	case len(m.infos) == 0:
		body = append(body, "  "+sMuted.Render("No processes yet. Start one with:"), "", "  procly start app.js")
	default:
		if !m.zoom {
			body = append(body, m.tableLines(m.tableRows())...)
			body = append(body, "")
			body = append(body, m.detailLines()...)
			body = append(body, "")
		}
		logH := m.h - len(top) - len(body) - 1
		body = append(body, m.logLines(logH)...)
	}

	lines := append(top, body...)
	for len(lines) < m.h-1 {
		lines = append(lines, "")
	}
	lines = lines[:m.h-1]
	lines = append(lines, m.footerLine())
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.w, "…")
	}
	return strings.Join(lines, "\n")
}

func (m model) headerLine() string {
	left := sBold.Render("procly") + sMuted.Render("  "+version.Version)
	var right string
	if len(m.infos) > 0 {
		counts := map[proc.Status]int{}
		for _, in := range m.infos {
			counts[in.Status]++
		}
		parts := []string{fmt.Sprintf("%d %s", len(m.infos), plural(len(m.infos), "process", "processes"))}
		for _, s := range []proc.Status{proc.StatusOnline, proc.StatusWaiting, proc.StatusOrphaned, proc.StatusStopped, proc.StatusErrored} {
			if counts[s] > 0 {
				parts = append(parts, lipgloss.NewStyle().Foreground(statusColor(s)).Render(fmt.Sprintf("%d %s", counts[s], s)))
			}
		}
		right = strings.Join(parts, sMuted.Render(" · "))
	}
	gap := max(m.w-lipgloss.Width(left)-lipgloss.Width(right)-4, 1)
	return "  " + left + strings.Repeat(" ", gap) + right
}

// tableRows is how many process rows fit, leaving room for details and logs.
func (m model) tableRows() int {
	avail := m.h - 2 /*header*/ - 1 /*footer*/ - 1 /*table header*/ - 5 /*details*/ - 6 /*min logs*/
	return min(len(m.infos), max(avail, 3), max(m.h/2-3, 3))
}

func (m model) tableLines(rows int) []string {
	headers := []string{"", "name", "status", "pid", "cpu", "memory", "uptime", "restarts"}
	right := map[int]bool{3: true, 4: true, 5: true, 7: true}
	cells := make([][]string, len(m.infos))
	for i, in := range m.infos {
		status := "● " + string(in.Status)
		if verb, ok := m.busy[in.Name]; ok {
			status = "◌ " + progressive[verb]
		}
		pid, cpu, mem, up := "-", "-", "-", "-"
		if in.PID > 0 {
			pid = fmt.Sprint(in.PID)
			cpu = fmt.Sprintf("%.1f%%", in.CPU)
			mem = fmtBytes(in.Memory)
			up = fmtDuration(time.Since(in.StartedAt))
		}
		marker := " "
		if in.Name == m.selName {
			marker = "›"
		}
		cells[i] = []string{marker, in.Name, status, pid, cpu, mem, up, fmt.Sprint(in.Restarts)}
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = lipgloss.Width(h)
	}
	for _, r := range cells {
		for i, c := range r {
			widths[i] = max(widths[i], lipgloss.Width(c))
		}
	}
	pad := func(s string, col int) string {
		gap := strings.Repeat(" ", widths[col]-lipgloss.Width(s))
		if right[col] {
			return gap + s
		}
		return s + gap
	}

	var lines []string
	var hdr []string
	for i, h := range headers {
		hdr = append(hdr, pad(h, i))
	}
	lines = append(lines, " "+sMuted.Render(strings.Join(hdr, "   ")))

	start := 0
	if sel := m.selIndex(); sel >= rows {
		start = sel - rows + 1
	}
	end := min(start+rows, len(cells))
	for ri := start; ri < end; ri++ {
		in := m.infos[ri]
		selected := in.Name == m.selName
		base := lipgloss.NewStyle()
		if selected {
			base = base.Background(colSelBg)
		}
		var b strings.Builder
		b.WriteString(base.Render(" "))
		for ci, c := range cells[ri] {
			st := base
			switch ci {
			case 0, 1:
				st = st.Bold(true)
			case 2:
				st = st.Foreground(statusColor(in.Status))
				if _, ok := m.busy[in.Name]; ok {
					st = st.Foreground(colYellow)
				}
			case 3:
				st = st.Foreground(colMuted)
			case 7:
				if in.Restarts > 0 {
					st = st.Foreground(colYellow)
				}
			}
			if ci > 0 {
				b.WriteString(base.Render("   "))
			}
			b.WriteString(st.Render(pad(c, ci)))
		}
		line := b.String()
		if selected {
			line += base.Render(strings.Repeat(" ", max(m.w-lipgloss.Width(line), 0)))
		}
		lines = append(lines, line)
	}
	if hidden := len(cells) - (end - start); hidden > 0 {
		lines = append(lines, "  "+sFaint.Render(fmt.Sprintf("%d more, use ↑↓ to scroll", hidden)))
	}
	return lines
}

func (m model) detailLines() []string {
	sel := m.selected()
	if sel == nil {
		return nil
	}
	dot := lipgloss.NewStyle().Foreground(statusColor(sel.Status)).Render("● " + string(sel.Status))
	var facts []string
	if sel.PID > 0 {
		facts = append(facts, fmt.Sprintf("pid %d", sel.PID), "up "+fmtDuration(time.Since(sel.StartedAt)))
	} else {
		facts = append(facts, fmt.Sprintf("last exit code %d", sel.ExitCode))
	}
	facts = append(facts, fmt.Sprintf("%d %s", sel.Restarts, plural(sel.Restarts, "restart", "restarts")))
	l1 := "  " + sBold.Render(sel.Name) + "  " + dot + sMuted.Render("  ·  "+strings.Join(facts, "  ·  "))

	cpuNow, memNow := "-", "-"
	if sel.PID > 0 {
		cpuNow = fmt.Sprintf("%.1f%%", sel.CPU)
		memNow = fmtBytes(sel.Memory)
	}
	const mb = 1 << 20
	line := lipgloss.NewStyle().Foreground(colGreen)
	l2 := "  " + sMuted.Render("cpu ") + line.Render(spark(m.cpu[sel.Name], sparkWidth, true, 10)) +
		" " + fmt.Sprintf("%-7s", cpuNow) + "   " +
		sMuted.Render("mem ") + line.Render(spark(m.mem[sel.Name], sparkWidth, false, 8*mb)) +
		" " + memNow
	l3 := "  " + sMuted.Render("$ ") + sel.Spec.Command + sMuted.Render("   in "+tilde(sel.Spec.Cwd))
	return []string{l1, l2, l3}
}

func (m model) logLines(height int) []string {
	if height < 2 {
		return nil
	}
	title := "logs"
	if m.selName != "" {
		title = "logs · " + m.selName
	}
	hint := "enter to expand"
	if m.zoom {
		hint = "esc to go back"
	}
	label := sMuted.Render("── " + title + " ")
	right := sFaint.Render(" " + hint + " ──")
	fill := max(m.w-lipgloss.Width(label)-lipgloss.Width(right)-2, 0)
	lines := []string{"  " + label + sFaint.Render(strings.Repeat("─", fill)) + right}

	n := height - 1
	if len(m.logs) == 0 {
		return append(lines, "  "+sFaint.Render("no output yet"))
	}
	start := max(len(m.logs)-n, 0)
	gutterOut := sFaint.Render("│ ")
	gutterErr := sRed.Render("│ ")
	for _, l := range m.logs[start:] {
		text := strings.ReplaceAll(strings.ReplaceAll(l.text, "\t", "    "), "\r", "")
		g := gutterOut
		if l.stderr {
			g = gutterErr
		}
		lines = append(lines, "  "+g+text)
	}
	return lines
}

func (m model) footerLine() string {
	if m.confirm != "" {
		return "  " + sRed.Render(fmt.Sprintf("delete %s? it will be stopped and forgotten", m.confirm)) +
			sMuted.Render("   ") + sKey.Render("y") + sMuted.Render(" confirm   any key cancels")
	}
	type hint struct{ k, v string }
	hints := []hint{{"↑↓", "select"}, {"r", "restart"}, {"s", "stop/start"}, {"d", "delete"}, {"enter", "logs"}, {"?", "help"}, {"q", "quit"}}
	var parts []string
	for _, h := range hints {
		parts = append(parts, sKey.Render(h.k)+" "+sMuted.Render(h.v))
	}
	left := "  " + strings.Join(parts, "   ")

	var right string
	for name, verb := range m.busy {
		right = sMuted.Render(progressive[verb] + " " + name + "…")
		break
	}
	if right == "" && m.flash != "" {
		if m.flashErr {
			right = sRed.Render(m.flash)
		} else {
			right = sMuted.Render(m.flash)
		}
	}
	if right == "" {
		return left
	}
	gap := m.w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 2 {
		return "  " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m model) helpView() string {
	rows := [][2]string{
		{"↑ ↓  k j", "move selection"},
		{"g  G", "first / last process"},
		{"r", "restart selected"},
		{"s", "stop, or start if stopped"},
		{"d  x", "delete (asks first)"},
		{"enter  l", "expand logs, esc to go back"},
		{"c", "clear the log pane"},
		{"q  ctrl+c", "quit, processes keep running"},
	}
	lines := []string{m.headerLine(), "", "  " + sBold.Render("Keys"), ""}
	for _, r := range rows {
		lines = append(lines, "  "+sKey.Render(fmt.Sprintf("%-12s", r[0]))+sMuted.Render(r[1]))
	}
	lines = append(lines, "", "  "+sFaint.Render("press any key to close"))
	if len(lines) > m.h {
		lines = lines[:m.h]
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.w, "…")
	}
	return strings.Join(lines, "\n")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

func fmtBytes(b int64) string {
	const unit = 1024
	switch {
	case b < unit:
		return fmt.Sprintf("%d B", b)
	case b < unit*unit:
		return fmt.Sprintf("%.1f KB", float64(b)/unit)
	case b < unit*unit*unit:
		return fmt.Sprintf("%.1f MB", float64(b)/(unit*unit))
	default:
		return fmt.Sprintf("%.2f GB", float64(b)/(unit*unit*unit))
	}
}

func tilde(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h+"/") {
		return "~" + p[len(h):]
	}
	return p
}
