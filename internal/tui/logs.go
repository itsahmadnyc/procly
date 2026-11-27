package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/proc"
)

const (
	logKeep    = 2000 // lines kept in memory for the pane
	logHistory = 300  // lines read back when a process is picked
)

type logLine struct {
	text   string // ANSI stripped, tabs expanded
	stderr bool
	src    int // index into model.srcs, to tell a group's instances apart
}

type logSource struct {
	name     string
	out, err *logtail.Follower
}

type streamFilter int

const (
	streamAll streamFilter = iota
	streamErr
	streamOut
	streamModes
)

var streamNames = [...]string{"stdout and stderr", "stderr only", "stdout only"}

// clean makes a log line safe to draw: no escape sequences, no tabs, no CR,
// no other control characters (a bell, a backspace or a stray 0x9B would act
// on the terminal instead of being shown), and no emoji (see safe).
func clean(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\t", "    ")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7F && r <= 0x9F) {
			return -1
		}
		return r
	}, s)
	return safe(s)
}

// openLogs points the pane at the processes of the selection.
func (m *model) openLogs(views []*proc.View) {
	m.logs, m.srcs, m.logOff, m.unseen = nil, nil, 0, 0
	if len(views) == 0 {
		return
	}
	each := max(logHistory/len(views), 50)
	// One process: its stdout history, then its stderr history. The files
	// share no clock, and guessing an order would cut a multi-line message
	// (a startup banner, a stack trace) in two. Several instances: the
	// instances' histories are merged within each stream.
	var outs, errs [][]logLine
	for i, v := range views {
		out, outSize := logtail.Tail(v.OutLog, each)
		errl, errSize := logtail.Tail(v.ErrLog, each)
		var o, e []logLine
		for _, l := range out {
			o = append(o, logLine{text: clean(l), src: i})
		}
		for _, l := range errl {
			e = append(e, logLine{text: clean(l), stderr: true, src: i})
		}
		outs, errs = append(outs, o), append(errs, e)
		m.srcs = append(m.srcs, &logSource{name: v.Name, out: logtail.NewFollower(v.OutLog, outSize), err: logtail.NewFollower(v.ErrLog, errSize)})
	}
	m.logs = append(interleave(outs), interleave(errs)...)
	m.trimLogs()
}

// interleave merges the histories of several instances' files of one kind.
// They share no clock, so each line is placed by how far through its own
// file's tail it is: for instances that write steadily that is close to the
// real order, and far easier to read than one file after the other. Lines
// that arrive from now on appear in the order they are read.
func interleave(streams [][]logLine) []logLine {
	type placed struct {
		at   float64
		line logLine
	}
	var all []placed
	for _, s := range streams {
		for j, l := range s {
			all = append(all, placed{float64(j+1) / float64(len(s)), l})
		}
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].at < all[b].at })
	out := make([]logLine, len(all))
	for i, p := range all {
		out[i] = p.line
	}
	return out
}

func (m *model) readLogs() {
	var added []logLine
	for i, s := range m.srcs {
		for _, l := range s.out.Read() {
			added = append(added, logLine{text: clean(l), src: i})
		}
		for _, l := range s.err.Read() {
			added = append(added, logLine{text: clean(l), stderr: true, src: i})
		}
	}
	if len(added) == 0 {
		return
	}
	if m.logOff > 0 { // paused: keep the same lines on screen
		n := 0
		for _, l := range added {
			if m.shows(l) {
				n++
			}
		}
		m.logOff += n
		m.unseen += n
	}
	m.logs = append(m.logs, added...)
	m.trimLogs()
}

func (m *model) trimLogs() {
	if len(m.logs) > logKeep {
		m.logs = append(m.logs[:0:0], m.logs[len(m.logs)-logKeep:]...)
	}
}

// shows reports whether a line passes the stream and search filters.
func (m *model) shows(l logLine) bool {
	switch m.stream {
	case streamErr:
		if !l.stderr {
			return false
		}
	case streamOut:
		if l.stderr {
			return false
		}
	}
	if m.logFilter == "" {
		return true
	}
	return strings.Contains(strings.ToLower(l.text), strings.ToLower(m.logFilter))
}

func (m *model) visibleLogs() []logLine {
	if m.stream == streamAll && m.logFilter == "" {
		return m.logs
	}
	var out []logLine
	for _, l := range m.logs {
		if m.shows(l) {
			out = append(out, l)
		}
	}
	return out
}

// scrollLogs moves the view by delta lines; up is positive. It stops when the
// oldest line is at the top of the pane, and at the bottom the pane follows
// new output again.
func (m *model) scrollLogs(delta int) {
	m.logOff = min(max(m.logOff+delta, 0), m.maxLogOff())
	if m.logOff == 0 {
		m.unseen = 0
	}
}

// maxLogOff is how far up the view can go: the largest offset at which the
// pane is still full. Lines that wrap take several rows, so it counts rows.
func (m *model) maxLogOff() int {
	vis := m.visibleLogs()
	l := m.layout().logs
	rows := l.h - 2
	if rows <= 0 || l.w < 8 {
		return max(len(vis)-1, 0)
	}
	textW := m.logTextWidth(l.w - 4)
	used := 0
	for i, line := range vis {
		used += len(m.wrapLog(line.text, textW))
		if used >= rows {
			return len(vis) - (i + 1)
		}
	}
	return 0 // everything fits
}

// logTextWidth is the room for a log line's text inside a pane cw wide, after
// the gutter and, for a group, the instance name.
func (m *model) logTextWidth(cw int) int {
	w := cw - 2
	if len(m.srcs) > 1 {
		nameW := 0
		for _, s := range m.srcs {
			nameW = max(nameW, lipgloss.Width(s.name))
		}
		w -= nameW + 1
	}
	return max(w, 1)
}

// wrapLog cuts a log line into the rows it takes: one, truncated, when long
// lines are cut, otherwise wrapped.
func (m *model) wrapLog(text string, textW int) []string {
	if m.nowrap || lipgloss.Width(text) <= textW {
		return []string{ansi.Truncate(text, textW, "…")}
	}
	return strings.Split(ansi.Wrap(text, textW, ""), "\n")
}

func (m *model) follow() { m.logOff, m.unseen = 0, 0 }

// highlight marks every case-insensitive match of q in plain text s.
func highlight(s, q string) string {
	if q == "" {
		return s
	}
	lower, lq := strings.ToLower(s), strings.ToLower(q)
	if len(lower) != len(s) { // a case change altered byte lengths; skip marking
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(lower, lq)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(sMatch.Render(s[i : i+len(lq)]))
		s, lower = s[i+len(lq):], lower[i+len(lq):]
	}
}
