package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/itsahmadnyc/procly/internal/domains"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/version"
)

const mb = 1 << 20

type rect struct{ x, y, w, h int }

func (r rect) has(x, y int) bool {
	return r.w > 0 && r.h > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// layout places the panes. The header is the first line and the footer the
// last; the panes share what is between. Wide terminals get the table and the
// details side by side; narrow ones stack them and drop what does not fit.
type layout struct{ table, detail, logs rect }

func (m model) layout() layout {
	var l layout
	if m.w == 0 || m.h == 0 || m.help || !m.loaded || m.err != nil || len(m.views) == 0 {
		return l
	}
	top, bodyH := 1, m.h-2
	if bodyH < 1 {
		return l
	}
	if m.zoom {
		l.logs = rect{0, top, m.w, bodyH}
		return l
	}
	tableNeed := max(len(m.rows), 1) + 3 // frame and column titles
	if m.w >= 110 && bodyH >= 18 {
		topH := min(max(tableNeed, 12), bodyH-8)
		tw := m.w * 55 / 100
		l.table = rect{0, top, tw, topH}
		l.detail = rect{tw, top, m.w - tw, topH}
		l.logs = rect{0, top + topH, m.w, bodyH - topH}
		return l
	}
	tableH := min(tableNeed, max(bodyH*45/100, 5), bodyH)
	rest := bodyH - tableH
	detailH := 0
	if rest >= 7+4 {
		detailH = 7
	}
	logsH := rest - detailH
	if logsH < 4 {
		tableH, logsH = tableH+logsH, 0
	}
	l.table = rect{0, top, m.w, tableH}
	y := top + tableH
	if detailH > 0 {
		l.detail = rect{0, y, m.w, detailH}
		y += detailH
	}
	if logsH > 0 {
		l.logs = rect{0, y, m.w, logsH}
	}
	return l
}

func (m model) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	if m.help {
		return m.helpView()
	}
	lines := make([]string, m.h)
	lines[0] = m.headerLine()
	l := m.layout()
	msg := func(s ...string) {
		for i, t := range s {
			if 2+i < m.h-1 {
				lines[2+i] = "  " + t
			}
		}
	}
	switch {
	case m.err != nil:
		msg(sRed.Render("lost connection to the daemon"), sMuted.Render(m.err.Error()), "", sMuted.Render("Is it running? Try: procly ls"))
	case !m.loaded:
		msg(sMuted.Render("connecting…"))
	case len(m.views) == 0:
		msg(sMuted.Render("No processes yet. Start one with:"), "", "procly start app.js")
	default:
		paste(lines, l.table, m.tableBox(l.table))
		paste(lines, l.detail, m.detailBox(l.detail))
		paste(lines, l.logs, m.logBox(l.logs))
	}
	if m.h > 1 {
		lines[m.h-1] = m.footerLine()
	}
	for i := range lines {
		lines[i] = fit(lines[i], m.w)
	}
	return strings.Join(lines, "\n")
}

// paste puts a pane's lines into the screen. Panes are pasted left to right.
func paste(lines []string, r rect, pane []string) {
	for i, pl := range pane {
		y := r.y + i
		if y < 0 || y >= len(lines) {
			continue
		}
		if r.x == 0 {
			lines[y] = pl
		} else {
			lines[y] = fit(lines[y], r.x) + pl
		}
	}
}

// ---- header and footer ----

func (m model) headerLine() string {
	left := " " + sAccent.Bold(true).Render("procly") + " " + sMuted.Render(version.Version)
	var right string
	if len(m.views) > 0 {
		counts := map[proc.Status]int{}
		for _, v := range m.views {
			counts[v.Status]++
		}
		parts := []string{sMuted.Render(fmt.Sprintf("%d %s", len(m.views), plural(len(m.views), "process", "processes")))}
		for _, s := range []proc.Status{proc.StatusOnline, proc.StatusWaiting, proc.StatusOrphaned, proc.StatusStopped, proc.StatusErrored} {
			if counts[s] > 0 {
				parts = append(parts, lipgloss.NewStyle().Foreground(statusColor(s)).Render(fmt.Sprintf("%d %s", counts[s], s)))
			}
		}
		for len(parts) > 1 && lipgloss.Width(strings.Join(parts, " · "))+lipgloss.Width(left)+4 > m.w {
			parts = parts[:len(parts)-1]
		}
		right = strings.Join(parts, sMuted.Render(" · ")) + " "
	}
	room := m.w - lipgloss.Width(left) - lipgloss.Width(right) - 6
	if h := m.host.line(room); h != "" {
		left += sFaint.Render("  │  ") + h
	}
	return spread(left, right, m.w)
}

type hint struct{ k, v string }

func (m model) footerLine() string {
	if m.confirm != "" {
		what := m.confirm
		for _, r := range m.rows {
			if r.v == nil && r.group == m.confirm {
				what = fmt.Sprintf("all %d instances of %s", len(r.members), m.confirm)
			}
		}
		ask := map[string]string{
			"restart": "restart " + what + "? requests in flight may fail for a moment",
			"stop":    "stop " + what + "? it stays down until you start it",
			"delete":  "delete " + what + "? it will be stopped and forgotten",
			"cluster": "run " + what + " in cluster mode? it restarts, one worker per CPU",
			"fork":    "run " + what + " in fork mode? it restarts",
		}[m.confirmVerb]
		style := sRed
		if m.confirmVerb != "delete" {
			style = sAccent
		}
		return " " + style.Render(ask) +
			"   " + sKey.Render("y") + sMuted.Render(" yes   any other key cancels")
	}
	if m.input != inputNone {
		label := "filter processes"
		if m.input == inputLogs {
			label = "search logs"
		}
		return " " + sAccent.Render("/") + " " + m.inputBuf + sAccent.Render("█") + "   " +
			sMuted.Render(label+" · ") + sKey.Render("enter") + sMuted.Render(" keep · ") + sKey.Render("esc") + sMuted.Render(" clear")
	}

	var hints []hint
	switch {
	case m.zoom:
		hints = []hint{{"esc", "back"}, {"↑↓", "scroll"}, {"f", "follow"}, {"/", "search"}, {"e", "stream"}, {"w", "wrap"}}
	case m.focus == paneLogs:
		hints = []hint{{"↑↓", "scroll"}, {"f", "follow"}, {"/", "search"}, {"e", "stream"}, {"w", "wrap"}, {"c", "clear"}, {"tab", "processes"}, {"enter", "full screen"}}
	default:
		hints = []hint{{"↑↓", "select"}, {"r", "restart"}, {"s", "stop/start"}, {"R", "reload"}, {"m", "mode"}, {"d", "delete"}, {"/", "filter"}, {"o", "sort"}, {"tab", "logs"}, {"enter", "full screen"}}
	}

	var right string
	for target, verb := range m.busy {
		right = sMuted.Render(progressive[verb] + " " + target + "…")
		break
	}
	if right == "" && m.flash != "" && time.Since(m.flashAt) < flashFor {
		if m.flashErr {
			right = sRed.Render(m.flash)
		} else {
			right = sMuted.Render(m.flash)
		}
	}
	if right != "" {
		right += " "
	}

	tail := sKey.Render("?") + " " + sMuted.Render("help") + "   " + sKey.Render("q") + " " + sMuted.Render("quit")
	room := m.w - lipgloss.Width(right) - 2
	left := " "
	for _, h := range hints {
		part := sKey.Render(h.k) + " " + sMuted.Render(h.v) + "   "
		if lipgloss.Width(left+part+tail) > room {
			break
		}
		left += part
	}
	left += tail
	if lipgloss.Width(left) > room && right != "" {
		return " " + right
	}
	return spread(left, right, m.w)
}

// ---- process table ----

type column struct {
	key   string
	title string
	right bool
}

var allColumns = []column{
	{"name", "name", false}, {"status", "status", false},
	{"cpu", "cpu", true}, {"cpubar", "", false},
	{"mem", "memory", true}, {"membar", "", false},
	{"domain", "domain", false},
	{"uptime", "uptime", true}, {"restarts", "restarts", true},
	{"health", "health", false}, {"pid", "pid", true},
}

// dropOrder is which columns go first when the table is too narrow.
var dropOrder = []string{"pid", "membar", "cpubar", "domain", "uptime", "health", "restarts", "mem", "cpu"}

const barW = 8

// rowStats sums what the row shows.
type rowStats struct {
	cpu      float64
	mem      int64
	limit    int64 // max_memory; 0 none
	running  int
	restarts int
}

func statsOf(r row) rowStats {
	var s rowStats
	for _, v := range r.views() {
		s.restarts += v.Restarts
		if v.PID > 0 {
			s.running++
			s.cpu += v.CPU
			s.mem += v.Memory
			if v.MaxMemory > 0 {
				s.limit += v.MaxMemory
			}
		}
	}
	return s
}

func (m model) cell(r row, key string, maxMem int64) string {
	st := statsOf(r)
	switch key {
	case "name":
		switch {
		case r.v == nil:
			mark := "▾"
			if m.collapsed[r.group] && m.procFilter == "" {
				mark = "▸"
			}
			return fmt.Sprintf("%s %s ×%d", mark, r.group, len(r.members))
		case r.group != "":
			if r.last {
				return "  └ " + r.v.Name
			}
			return "  ├ " + r.v.Name
		}
		return r.v.Name
	case "status":
		if r.v == nil {
			if verb, ok := m.busy[r.group]; ok {
				return "◌ " + progressive[verb]
			}
			s, _ := groupStatus(r.members)
			return s
		}
		if verb, ok := m.busyVerb(r.v); ok {
			return "◌ " + progressive[verb]
		}
		return "● " + string(r.v.Status)
	case "cpu":
		if st.running == 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f%%", st.cpu)
	case "mem":
		if st.running == 0 {
			return "-"
		}
		return fmtBytes(st.mem)
	case "cpubar", "membar":
		return strings.Repeat(" ", barW)
	case "uptime":
		if r.v == nil || r.v.PID == 0 {
			return "-"
		}
		return fmtDuration(time.Duration(r.v.UptimeMs) * time.Millisecond)
	case "restarts":
		return fmt.Sprint(st.restarts)
	case "domain":
		return m.domainText(r)
	case "health":
		return healthText(r.views())
	case "pid":
		if r.v == nil || r.v.PID == 0 {
			return "-"
		}
		return fmt.Sprint(r.v.PID)
	}
	return ""
}

func healthText(vs []*proc.View) string {
	checked, healthy, bad := 0, 0, 0
	for _, v := range vs {
		if v.HealthCheck == "" || v.PID == 0 {
			continue
		}
		checked++
		switch v.Health {
		case proc.HealthHealthy:
			healthy++
		case proc.HealthUnhealthy:
			bad++
		}
	}
	switch {
	case checked == 0:
		return "-"
	case bad > 0 && len(vs) > 1:
		return fmt.Sprintf("✗ %d unhealthy", bad)
	case bad > 0:
		return "✗ unhealthy"
	case healthy == checked:
		return "✓ healthy"
	}
	return "… starting"
}

// tableWindow is the first row shown when rows of n fit: the selection
// stays near the middle.
func tableWindow(n, sel, rows int) int {
	if rows <= 0 || n <= rows {
		return 0
	}
	return min(max(sel-rows/2, 0), n-rows)
}

func (m model) rowAt(l layout, y int) int {
	visible := l.table.h - 3
	first := l.table.y + 2
	if visible <= 0 || y < first || y >= first+visible {
		return -1
	}
	i := tableWindow(len(m.rows), m.selIndex(), visible) + y - first
	if i >= len(m.rows) {
		return -1
	}
	return i
}

func (m model) tableBox(r rect) []string {
	if r.h <= 0 {
		return nil
	}
	cw := r.w - 4
	visible := r.h - 3
	focused := m.focus == paneProcs

	var notes []string
	if m.procFilter != "" {
		notes = append(notes, "/"+m.procFilter)
	}
	if m.sortBy != sortID {
		notes = append(notes, "by "+sortNames[m.sortBy]+" ↓")
	}
	if len(m.rows) > visible && visible > 0 {
		start := tableWindow(len(m.rows), m.selIndex(), visible)
		notes = append(notes, fmt.Sprintf("%d–%d of %d", start+1, min(start+visible, len(m.rows)), len(m.rows)))
	}
	note := strings.Join(notes, " · ")
	if len(m.rows) == 0 {
		body := []string{"", sMuted.Render("no process matches “" + m.procFilter + "”"), sFaint.Render("esc clears the filter")}
		return box("Processes", note, body, r.w, r.h, focused)
	}

	withHealth, withDomain := false, false
	var maxMem int64
	for _, v := range m.views {
		withHealth = withHealth || v.HealthCheck != ""
		maxMem = max(maxMem, v.Memory)
	}
	for _, p := range m.doms.Processes {
		withDomain = withDomain || len(p.Domains) > 0
	}
	cols := []column{}
	for _, c := range allColumns {
		if (c.key == "health" && !withHealth) || (c.key == "domain" && !withDomain) {
			continue
		}
		cols = append(cols, c)
	}
	cells := make([][]string, len(m.rows))
	for i, rw := range m.rows {
		for _, c := range cols {
			cells[i] = append(cells[i], m.cell(rw, c.key, maxMem))
		}
	}
	widths := func() []int {
		w := make([]int, len(cols))
		for i, c := range cols {
			w[i] = lipgloss.Width(c.title)
			for _, row := range cells {
				w[i] = max(w[i], lipgloss.Width(row[i]))
			}
		}
		return w
	}
	total := func(w []int) int {
		t := 2 + 2*(len(w)-1)
		for _, x := range w {
			t += x
		}
		return t
	}
	ws := widths()
	for _, drop := range dropOrder {
		if total(ws) <= cw {
			break
		}
		for i, c := range cols {
			if c.key == drop {
				cols = append(cols[:i], cols[i+1:]...)
				for j := range cells {
					cells[j] = append(cells[j][:i:i], cells[j][i+1:]...)
				}
				break
			}
		}
		ws = widths()
	}
	if over := total(ws) - cw; over > 0 { // still too wide: the name gives way
		ws[0] = max(ws[0]-over, 4)
	}

	pad := func(s string, i int) string {
		s = ansi.Truncate(s, ws[i], "…")
		gap := strings.Repeat(" ", max(ws[i]-lipgloss.Width(s), 0))
		if cols[i].right {
			return gap + s
		}
		return s + gap
	}
	var head []string
	for i, c := range cols {
		head = append(head, pad(c.title, i))
	}
	body := []string{sMuted.Render("  " + strings.Join(head, "  "))}

	start := tableWindow(len(m.rows), m.selIndex(), visible)
	for ri := start; ri < min(start+visible, len(m.rows)); ri++ {
		rw := m.rows[ri]
		selected := rw.sel() == m.sel
		base := lipgloss.NewStyle()
		if selected {
			base = base.Background(colSelBg)
		}
		gutter := base.Render("  ")
		if selected && focused {
			gutter = sAccent.Inherit(base).Render("▌ ")
		} else if selected {
			gutter = sMuted.Inherit(base).Render("› ")
		}
		st := statsOf(rw)
		var b strings.Builder
		b.WriteString(gutter)
		for i, c := range cols {
			if i > 0 {
				b.WriteString(base.Render("  "))
			}
			b.WriteString(m.styleCell(rw, c.key, pad(cells[ri][i], i), st, maxMem, base))
		}
		line := b.String()
		if selected {
			line += base.Render(strings.Repeat(" ", max(cw-lipgloss.Width(line), 0)))
		}
		body = append(body, line)
	}
	return box("Processes", note, body, r.w, r.h, focused)
}

func (m model) styleCell(r row, key, s string, st rowStats, maxMem int64, base lipgloss.Style) string {
	switch key {
	case "name":
		if r.v != nil && r.group != "" {
			return base.Render(s)
		}
		return sBold.Inherit(base).Render(s)
	case "status":
		status := proc.StatusStopped
		if r.v != nil {
			status = r.v.Status
		} else {
			_, status = groupStatus(r.members)
		}
		fg := lipgloss.NewStyle().Foreground(statusColor(status))
		if strings.HasPrefix(strings.TrimSpace(s), "◌") {
			fg = sYellow
		}
		return fg.Inherit(base).Render(s)
	case "cpu":
		return level(st.cpu / 100).Inherit(base).Render(s)
	case "mem":
		if st.limit > 0 {
			return level(float64(st.mem) / float64(st.limit)).Inherit(base).Render(s)
		}
		return base.Render(s)
	case "cpubar":
		if st.running == 0 {
			return base.Render(s)
		}
		return meter(st.cpu/100/float64(max(len(r.views()), 1)), barW, true, base)
	case "membar":
		if st.running == 0 {
			return base.Render(s)
		}
		if st.limit > 0 {
			return meter(float64(st.mem)/float64(st.limit), barW, true, base)
		}
		if r.v == nil || maxMem == 0 { // groups have no common scale without a limit
			return base.Render(s)
		}
		return meter(float64(st.mem)/float64(maxMem), barW, false, base)
	case "restarts":
		if st.restarts > 0 {
			return sYellow.Inherit(base).Render(s)
		}
		return sMuted.Inherit(base).Render(s)
	case "health":
		switch {
		case strings.HasPrefix(s, "✓"):
			return sGreen.Inherit(base).Render(s)
		case strings.HasPrefix(s, "✗"):
			return sRed.Inherit(base).Render(s)
		}
		return sMuted.Inherit(base).Render(s)
	case "domain":
		if strings.TrimSpace(s) == "-" {
			return sFaint.Inherit(base).Render(s)
		}
		return sGreen.Inherit(base).Render(s)
	case "pid", "uptime":
		return sMuted.Inherit(base).Render(s)
	}
	return base.Render(s)
}

// meter is a bar n cells wide. Against a real limit it turns yellow and red
// near the top; on a relative scale it stays neutral.
func meter(frac float64, n int, limit bool, base lipgloss.Style) string {
	frac = min(max(frac, 0), 1)
	full := int(frac*float64(n) + 0.5)
	if frac > 0 && full == 0 {
		full = 1
	}
	fill := sAccent
	if limit {
		fill = sGreen
		if frac >= 0.7 {
			fill = level(frac)
		}
	}
	return fill.Inherit(base).Render(strings.Repeat("━", full)) + sFaint.Inherit(base).Render(strings.Repeat("━", n-full))
}

// ---- details ----

func (m model) detailBox(r rect) []string {
	if r.h <= 0 {
		return nil
	}
	rw, ok := m.selectedRow()
	var body []string
	if ok {
		body = m.detailLines(rw, r.w-4, r.h-2)
	}
	return box("Details", "", body, r.w, r.h, false)
}

func (m model) detailLines(r row, cw, n int) []string {
	vs := r.views()
	first := vs[0]
	st := statsOf(r)
	key := r.sel().key()

	var head, state string
	if r.v != nil {
		v := r.v
		status := lipgloss.NewStyle().Foreground(statusColor(v.Status)).Render("● " + string(v.Status))
		if verb, busy := m.busyVerb(v); busy {
			status = sYellow.Render("◌ " + progressive[verb])
		}
		head = sBold.Render(v.Name) + "  " + status
		if h := healthText(vs); h != "-" {
			head += "  " + m.styleCell(r, "health", h, st, 0, lipgloss.NewStyle())
		}
		state = stateLine(v)
	} else {
		s, status := groupStatus(r.members)
		head = sBold.Render(fmt.Sprintf("%s ×%d", r.group, len(r.members))) + "  " + lipgloss.NewStyle().Foreground(statusColor(status)).Render(s)
		state = sMuted.Render(fmt.Sprintf("%d instances · %d %s in total · r restarts them one at a time",
			len(r.members), st.restarts, plural(st.restarts, "restart", "restarts")))
	}

	cpuNow, memNow := "-", "-"
	if st.running > 0 {
		cpuNow = fmt.Sprintf("%.1f%%", st.cpu)
		memNow = fmtBytes(st.mem)
		if st.limit > 0 {
			memNow += sMuted.Render(" of ") + fmtBytes(st.limit)
		}
	}
	memSpark := sGreen
	if st.limit > 0 {
		if f := float64(st.mem) / float64(st.limit); f >= 0.7 {
			memSpark = level(f)
		}
	}
	// graph shows the samples so far at the right, and the time not yet
	// sampled as a faint baseline.
	graph := func(vals []float64, sw int, st lipgloss.Style, fromZero bool, minSpan float64) string {
		n := min(len(vals), sw)
		return sFaint.Render(strings.Repeat("▁", sw-n)) + st.Render(spark(vals, n, fromZero, minSpan))
	}
	graphs := func(sw int) (string, string) {
		return sMuted.Render("cpu ") + graph(m.cpu[key], sw, sGreen, true, 10) + " " + cpuNow,
			sMuted.Render("mem ") + graph(m.mem[key], sw, memSpark, false, 8*mb) + " " + memNow
	}
	command := sMuted.Render("$ ") + safe(first.Command)
	cwd := sMuted.Render("in ") + safe(tilde(first.Cwd))
	settings := settingsLine(first)

	if n <= 5 { // stacked layout: the essentials, two graphs on one line
		sw := min(max((cw-36)/2, 6), historyLen)
		cpu, mem := graphs(sw)
		out := []string{head, state, cpu + "    " + mem, command + sMuted.Render("   ") + cwd}
		out = append(out, m.domainLines(r)...)
		if settings != "" {
			out = append(out, settings)
		}
		return out[:min(len(out), n)]
	}

	sw := min(max(cw-24, 8), historyLen)
	cpu, mem := graphs(sw)
	out := []string{head, state, ""}
	if st.running > 0 || len(m.cpu[key]) > 0 { // nothing to graph for a process that is not running
		out = append(out, cpu, mem, "")
	}
	out = append(out, command, cwd)
	out = append(out, m.domainLines(r)...)
	if settings != "" {
		out = append(out, settings)
	}
	if r.v != nil && r.v.HealthError != "" && r.v.Health == proc.HealthUnhealthy {
		out = append(out, sRed.Render("health: "+safe(r.v.HealthError)))
	}
	if r.v == nil { // a group lists its members
		out = append(out, "")
		for _, v := range r.members {
			if len(out) >= n {
				break
			}
			line := fmt.Sprintf("%-14s", v.Name) + " " + lipgloss.NewStyle().Foreground(statusColor(v.Status)).Render(fmt.Sprintf("%-10s", "● "+string(v.Status)))
			if v.PID > 0 {
				line += sMuted.Render(fmt.Sprintf("  %5.1f%%  %s  up %s", v.CPU, fmtBytes(v.Memory), fmtDuration(time.Duration(v.UptimeMs)*time.Millisecond)))
			}
			out = append(out, line)
		}
	} else if len(r.v.Events) > 0 && len(out)+2 < n {
		out = append(out, "", sMuted.Render("recent"))
		for _, e := range r.v.Events {
			if len(out) >= n {
				break
			}
			out = append(out, eventLine(e))
		}
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// stateLine says in words what the process is doing and what comes next.
func stateLine(v *proc.View) string {
	exit := ""
	if v.ExitCode != 0 {
		exit = fmt.Sprintf(" · last exit code %d", v.ExitCode)
	}
	switch v.Status {
	case proc.StatusOnline:
		return sMuted.Render(fmt.Sprintf("pid %d · up %s · %d %s", v.PID,
			fmtDuration(time.Duration(v.UptimeMs)*time.Millisecond), v.Restarts, plural(v.Restarts, "restart", "restarts")))
	case proc.StatusWaiting:
		return sYellow.Render(fmt.Sprintf("exited with code %d · restarting, attempt %d of %d", v.ExitCode, v.Restarts, v.MaxRestarts))
	case proc.StatusErrored:
		return sRed.Render(fmt.Sprintf("gave up after %d %s", v.Restarts, plural(v.Restarts, "restart", "restarts"))) +
			sMuted.Render(exit+" · s starts it again")
	case proc.StatusOrphaned:
		return sYellow.Render("left over from a daemon that died") + sMuted.Render(fmt.Sprintf(" · pid %d · nothing restarts it", v.PID))
	}
	return sMuted.Render("stopped" + exit + " · s starts it")
}

func settingsLine(v *proc.View) string {
	var parts []string
	if v.ReloadSignal != "" {
		parts = append(parts, "reload "+v.ReloadSignal)
	}
	if v.RestartEvery != "" {
		parts = append(parts, "restart every "+v.RestartEvery)
	}
	if v.RestartAt != "" {
		parts = append(parts, "daily at "+v.RestartAt)
	}
	if v.HealthCheck != "" {
		parts = append(parts, "health "+v.HealthCheck)
	}
	if !v.AutoRestart {
		parts = append(parts, "autorestart off")
	}
	if n := len(v.EnvKeys); n > 0 {
		parts = append(parts, fmt.Sprintf("%d env %s", n, plural(n, "var", "vars")))
	}
	if len(parts) == 0 {
		return ""
	}
	return sMuted.Render(strings.Join(parts, " · "))
}

func eventLine(e proc.ViewEvent) string {
	ago := fmtDuration(time.Duration(e.AgoMs)*time.Millisecond) + " ago"
	kind := sMuted
	switch e.Kind {
	case "crashed", "errored", "unhealthy", "memory":
		kind = sRed
	case "started":
		kind = sGreen
	case "reloaded", "scheduled":
		kind = sAccent
	case "orphaned":
		kind = sYellow
	}
	s := sFaint.Render(fmt.Sprintf("%-9s", ago)) + " " + kind.Render(fmt.Sprintf("%-9s", e.Kind))
	if e.ExitCode != 0 {
		s += sMuted.Render(fmt.Sprintf(" exit %d", e.ExitCode))
	}
	if e.Detail != "" {
		s += sMuted.Render(" " + safe(e.Detail))
	}
	return s
}

// ---- logs ----

func (m model) logBox(r rect) []string {
	if r.h <= 0 {
		return nil
	}
	title := "Logs"
	if rw, ok := m.selectedRow(); ok {
		if rw.v == nil {
			title += fmt.Sprintf(" · %s ×%d", rw.group, len(rw.members))
		} else {
			title += " · " + rw.v.Name
		}
	}
	var notes []string
	if m.logFilter != "" {
		notes = append(notes, fmt.Sprintf("/%s %d %s", m.logFilter, len(m.visibleLogs()), plural(len(m.visibleLogs()), "match", "matches")))
	}
	if m.stream != streamAll {
		notes = append(notes, streamNames[m.stream])
	}
	if m.nowrap {
		notes = append(notes, "no wrap")
	}
	if m.logOff > 0 {
		p := "paused"
		if m.unseen > 0 {
			p += fmt.Sprintf(" · %d new", m.unseen)
		}
		notes = append(notes, p+" · f follows")
	} else {
		notes = append(notes, "live")
	}
	return box(title, strings.Join(notes, " · "), m.logLines(r.w-4, r.h-2), r.w, r.h, m.zoom || m.focus == paneLogs)
}

func (m model) logLines(cw, n int) []string {
	if n <= 0 || cw <= 0 {
		return nil
	}
	vis := m.visibleLogs()
	if len(vis) == 0 {
		switch {
		case m.logFilter != "":
			return []string{sFaint.Render("no line matches “" + m.logFilter + "”")}
		case len(m.logs) > 0:
			return []string{sFaint.Render("nothing on " + strings.TrimSuffix(streamNames[m.stream], " only") + " yet")}
		}
		return []string{sFaint.Render("no output yet")}
	}

	nameW := 0
	if len(m.srcs) > 1 {
		for _, s := range m.srcs {
			nameW = max(nameW, lipgloss.Width(s.name))
		}
	}
	textW := m.logTextWidth(cw)

	end := min(max(len(vis)-m.logOff, 0), len(vis))
	var out []string
	for i := end - 1; i >= 0 && len(out) < n; i-- {
		l := vis[i]
		gutter := sFaint.Render("│ ")
		if l.stderr {
			gutter = sRed.Render("┃ ")
		}
		prefix, blank := "", ""
		if nameW > 0 {
			name := ""
			if l.src < len(m.srcs) {
				name = m.srcs[l.src].name
			}
			prefix = lipgloss.NewStyle().Foreground(sourceColors[l.src%len(sourceColors)]).Render(fit(name, nameW)) + " "
			blank = strings.Repeat(" ", nameW+1)
		}
		segs := m.wrapLog(l.text, textW)
		var block []string
		for j, s := range segs {
			p := prefix
			if j > 0 {
				p = blank
			}
			block = append(block, gutter+p+highlight(s, m.logFilter))
		}
		if room := n - len(out); len(block) > room {
			block = block[len(block)-room:]
		}
		out = append(block, out...)
	}
	return out
}

// ---- help ----

func (m model) helpView() string {
	sections := []struct {
		title string
		rows  [][2]string
	}{
		{"Processes", [][2]string{
			{"↑ ↓  k j  g G", "move, first / last; click a row with the mouse"},
			{"← →  h l", "fold / unfold a group of instances (space toggles)"},
			{"/", "filter by name, esc clears"},
			{"o", "sort by id, name, cpu, memory, restarts"},
		}},
		{"Actions", [][2]string{
			{"r", "restart (a group restarts one instance at a time)"},
			{"R", "reload: send reload_signal, the process keeps running"},
			{"s", "stop, or start when stopped"},
			{"m", "switch fork / cluster, asks first"},
			{"d  x", "delete, asks first"},
		}},
		{"Logs", [][2]string{
			{"tab", "switch between the table and the logs"},
			{"enter  z", "logs full screen, esc goes back"},
			{"pgup pgdn", "scroll a page; the mouse wheel scrolls too"},
			{"f", "follow new output again (G too, in the logs)"},
			{"/", "search, matching lines only"},
			{"e", "stdout and stderr, stderr only, stdout only"},
			{"w", "wrap or cut long lines"},
			{"c", "clear the pane"},
		}},
		{"", [][2]string{
			{"q  ctrl+c", "quit; processes keep running"},
			{"shift+drag", "select text (option+drag in iTerm2)"},
		}},
	}
	var body []string
	for _, s := range sections {
		if s.title != "" {
			body = append(body, sBold.Render(s.title))
		}
		for _, r := range s.rows {
			body = append(body, "  "+sKey.Render(fmt.Sprintf("%-12s", r[0]))+sMuted.Render(r[1]))
		}
		body = append(body, "")
	}
	body = append(body, sFaint.Render("press any key to close"))
	lines := []string{m.headerLine()}
	lines = append(lines, box("Keys", "procly monit", body, m.w, m.h-1, true)...)
	for i := range lines {
		lines[i] = fit(lines[i], m.w)
	}
	return strings.Join(lines[:min(len(lines), m.h)], "\n")
}

// ---- domains ----

// domainsOf are the names the proxies send to the process or, for a group,
// to any of its instances.
func (m model) domainsOf(r row) (ds []domains.Domain, ports []int, known bool) {
	if !m.domsOK {
		return nil, nil, false
	}
	for _, v := range r.views() {
		for _, p := range m.doms.Processes {
			if p.Name == v.Name {
				known = true
				ds = append(ds, p.Domains...)
				ports = append(ports, p.Ports...)
			}
		}
	}
	return ds, ports, known
}

// domainText is the table cell: the first name, and how many more.
func (m model) domainText(r row) string {
	ds, _, known := m.domainsOf(r)
	if !known || len(ds) == 0 {
		return "-"
	}
	var hosts []string
	for _, d := range ds {
		h := d.Host
		if h == "*" {
			h = "default site"
		}
		hosts = appendOnce(hosts, h)
	}
	s := hosts[0]
	if len(hosts) > 1 {
		s += fmt.Sprintf(" +%d", len(hosts)-1)
	}
	return ansi.Truncate(safe(s), 30, "…")
}

func appendOnce(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// domainLines are the lines of the details that answer "is this connected to
// a domain?": the names with how they are served and where they point, or
// the reason there are none.
func (m model) domainLines(r row) []string {
	ds, ports, known := m.domainsOf(r)
	if !known {
		return nil
	}
	label := sMuted.Render("domain ")
	if len(ds) > 0 {
		var out []string
		for i, d := range ds {
			if i >= 3 {
				out = append(out, sMuted.Render(fmt.Sprintf("       +%d more", len(ds)-3)))
				break
			}
			host := d.Host
			if host == "*" {
				host = "default site"
			}
			line := sGreen.Render(safe(host+d.Path)) + sMuted.Render("  "+d.Scheme+" · "+d.Source)
			switch d.DNS.Status {
			case "here":
				line += sMuted.Render(" · dns points here")
			case "cloudflare":
				line += sMuted.Render(" · via Cloudflare")
			case "elsewhere":
				if len(d.DNS.Addrs) > 0 {
					line += sMuted.Render(" · dns → " + d.DNS.Addrs[0])
				}
			case "none":
				line += sYellow.Render(" · dns does not resolve")
			}
			if i == 0 {
				out = append(out, label+line)
			} else {
				out = append(out, "       "+line)
			}
		}
		return out
	}
	if len(ports) == 0 {
		for _, v := range r.views() {
			if v.PID > 0 {
				return []string{label + sMuted.Render("not listening on a port")}
			}
		}
		return nil
	}
	var ps []string
	for _, p := range ports {
		ps = append(ps, fmt.Sprintf(":%d", p))
	}
	return []string{label + sMuted.Render("none found, listens on "+strings.Join(ps, " ")+"; no nginx, Caddy or Apache site sends traffic there")}
}
