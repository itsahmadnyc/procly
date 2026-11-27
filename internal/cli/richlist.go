package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	xterm "github.com/charmbracelet/x/term"

	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/version"
)

// The list in a terminal: a framed table with bars for CPU and memory, a
// tree for groups of instances, and a line under it for whatever needs
// attention. Anything that is not a terminal (a pipe, a file, a script, NO_COLOR)
// gets the plain table from printList, which scripts may rely on.

// richWidth reports whether w is a terminal that gets the framed list, and
// how many columns it has.
func richWidth(w io.Writer) (int, bool) {
	f, ok := w.(*os.File)
	if !ok || !colorOn {
		return 0, false
	}
	cols, _, err := xterm.GetSize(f.Fd())
	if err != nil || cols < 30 {
		return 100, true
	}
	return cols, true
}

// cell is one table cell: its plain text decides the width, paint colors it
// after it has been padded.
type cell struct {
	text  string
	paint func(string) string
}

type listCol struct {
	key   string
	title string
	right bool
}

var richCols = []listCol{
	{"id", "id", true}, {"name", "name", false}, {"status", "status", false},
	{"cpu", "cpu", true}, {"cpubar", "", false},
	{"mem", "memory", true}, {"membar", "", false},
	{"uptime", "uptime", true}, {"restarts", "restarts", true},
	{"health", "health", false}, {"pid", "pid", true},
}

// richDrop is what goes first when the table is wider than the terminal.
var richDrop = []string{"pid", "membar", "cpubar", "uptime", "health", "id", "restarts", "mem", "cpu"}

const meterW = 8

func identity(s string) string { return s }

// meterCell draws a bar n cells wide. Against a limit it turns yellow and red
// near the top; on a relative scale it keeps one calm color.
func meterCell(frac float64, limit bool) cell {
	frac = min(max(frac, 0), 1)
	full := int(frac*meterW + 0.5)
	if frac > 0 && full == 0 {
		full = 1
	}
	fill := func(s string) string { return green(s) }
	switch {
	case !limit:
		fill = func(s string) string { return paint("36", s) }
	case frac >= 0.9:
		fill = red
	case frac >= 0.7:
		fill = yellow
	}
	return cell{strings.Repeat("━", meterW), func(string) string {
		return fill(strings.Repeat("━", full)) + gray(strings.Repeat("━", meterW-full))
	}}
}

func levelPaint(frac float64) func(string) string {
	switch {
	case frac >= 0.9:
		return red
	case frac >= 0.7:
		return yellow
	}
	return identity
}

func healthCell(vs []proc.View) cell {
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
		return cell{"-", dim}
	case bad > 0:
		return cell{"✗ unhealthy", red}
	case healthy == checked:
		return cell{"✓ healthy", green}
	}
	return cell{"… starting", dim}
}

// listRowCells makes the cells of one row. maxMem is the biggest memory in
// the list, the scale for processes that have no max_memory.
func listRowCells(d listRow, last bool, maxMem int64) map[string]cell {
	vs := d.members
	if d.in != nil {
		vs = []proc.View{*d.in}
	}
	var cpu float64
	var mem, limit int64
	running := 0
	for _, v := range vs {
		if v.PID > 0 {
			running++
			cpu += v.CPU
			mem += v.Memory
			limit += v.MaxMemory
		}
	}
	c := map[string]cell{}

	// name
	switch {
	case d.in == nil:
		c["name"] = cell{"▾ " + d.name, bold}
	case d.indented:
		branch := "├ "
		if last {
			branch = "└ "
		}
		c["name"] = cell{"  " + branch + d.in.Name, func(s string) string { return strings.Replace(s, d.in.Name, bold(d.in.Name), 1) }}
	default:
		c["name"] = cell{d.name, bold}
	}
	// id
	if d.in != nil {
		c["id"] = cell{fmt.Sprint(d.in.ID), dim}
	} else {
		c["id"] = cell{"", identity}
	}
	// status
	if d.in != nil {
		c["status"] = cell{statusLabel(d.in.Status), statusColor(d.in.Status)}
	} else {
		s, color := groupStatus(d.members)
		c["status"] = cell{s, color}
	}
	// cpu and memory
	if running == 0 {
		for _, k := range []string{"cpu", "mem"} {
			c[k] = cell{"-", dim}
		}
		c["cpubar"] = cell{strings.Repeat(" ", meterW), identity}
		c["membar"] = cell{strings.Repeat(" ", meterW), identity}
	} else {
		c["cpu"] = cell{fmt.Sprintf("%.1f%%", cpu), levelPaint(cpu / 100 / float64(running))}
		c["cpubar"] = meterCell(cpu/100/float64(running), true)
		memPaint := identity
		if limit > 0 {
			memPaint = levelPaint(float64(mem) / float64(limit))
		}
		c["mem"] = cell{fmtBytes(mem), memPaint}
		switch {
		case limit > 0:
			c["membar"] = meterCell(float64(mem)/float64(limit), true)
		case d.in != nil && maxMem > 0:
			c["membar"] = meterCell(float64(mem)/float64(maxMem), false)
		default:
			c["membar"] = cell{strings.Repeat(" ", meterW), identity}
		}
	}
	// uptime, restarts, pid
	if d.in != nil && d.in.PID > 0 {
		c["uptime"] = cell{fmtDuration(time.Duration(d.in.UptimeMs) * time.Millisecond), dim}
		c["pid"] = cell{fmt.Sprint(d.in.PID), dim}
	} else {
		c["uptime"], c["pid"] = cell{"-", dim}, cell{"-", dim}
	}
	if d.restarts > 0 {
		c["restarts"] = cell{fmt.Sprint(d.restarts), yellow}
	} else {
		c["restarts"] = cell{"0", dim}
	}
	if d.in == nil {
		c["health"] = healthCell(vs)
	} else {
		c["health"] = healthCell(vs)
	}
	return c
}

// printRichList draws the framed list for a terminal cols wide.
func printRichList(w io.Writer, infos []proc.View, server string, cols int) {
	display := groupedForDisplay(infos)
	var maxMem int64
	withHealth := false
	counts := map[proc.Status]int{}
	for _, in := range infos {
		maxMem = max(maxMem, in.Memory)
		withHealth = withHealth || in.HealthCheck != ""
		counts[in.Status]++
	}

	keys := make([]listCol, 0, len(richCols))
	for _, c := range richCols {
		if c.key == "health" && !withHealth {
			continue
		}
		keys = append(keys, c)
	}
	rows := make([]map[string]cell, len(display))
	for i, d := range display {
		last := d.indented && (i+1 == len(display) || !display[i+1].indented)
		rows[i] = listRowCells(d, last, maxMem)
	}

	widths := func() map[string]int {
		ws := map[string]int{}
		for _, c := range keys {
			ws[c.key] = width(c.title)
			for _, r := range rows {
				ws[c.key] = max(ws[c.key], width(r[c.key].text))
			}
		}
		return ws
	}
	total := func(ws map[string]int) int {
		t := 4 // a border and a space of padding on each side
		for i, c := range keys {
			if i > 0 {
				t += 2
			}
			t += ws[c.key]
		}
		return t
	}
	ws := widths()
	// Too wide: first the columns that matter least go, then a long name is
	// cut (down to 14), then cpu and memory go, and last the name is cut again.
	const roomyName, minName = 14, 6
	over := func() int { return total(ws) + 2 - cols }
	for _, drop := range richDrop {
		if over() <= 0 {
			break
		}
		if (drop == "mem" || drop == "cpu") && ws["name"]-over() >= roomyName {
			break // cutting the name is enough
		}
		for i, c := range keys {
			if c.key == drop {
				keys = append(keys[:i], keys[i+1:]...)
				break
			}
		}
		ws = widths()
		if drop == "restarts" && ws["name"]-over() >= roomyName { // the minor columns are gone; try the name
			break
		}
	}
	if o := over(); o > 0 {
		ws["name"] = max(ws["name"]-o, minName)
	}
	inner := total(ws) - 2

	pad := func(s string, key string, right bool) string {
		s = truncate(s, ws[key])
		gap := strings.Repeat(" ", max(ws[key]-width(s), 0))
		if right {
			return gap + s
		}
		return s + gap
	}
	edge := func(s string) string { return gray(s) }

	// top edge: title on the left, counts on the right
	title := " procly " + version.Version
	if server != "" {
		title += " on " + server
	}
	title += " "
	var parts, plainParts []string
	plainParts = append(plainParts, fmt.Sprintf("%d %s", len(infos), plural(len(infos), "process", "processes")))
	parts = append(parts, dim(plainParts[0]))
	for _, s := range []proc.Status{proc.StatusOnline, proc.StatusWaiting, proc.StatusOrphaned, proc.StatusStopped, proc.StatusErrored} {
		if counts[s] > 0 {
			t := fmt.Sprintf("%d %s", counts[s], s)
			plainParts = append(plainParts, t)
			parts = append(parts, statusColor(s)(t))
		}
	}
	note, notePlain := " "+strings.Join(parts, dim(" · "))+" ", " "+strings.Join(plainParts, " · ")+" "
	for len(parts) > 1 && width(title)+width(notePlain)+4 > inner {
		parts, plainParts = parts[:len(parts)-1], plainParts[:len(plainParts)-1]
		note, notePlain = " "+strings.Join(parts, dim(" · "))+" ", " "+strings.Join(plainParts, " · ")+" "
	}
	if width(title)+width(notePlain)+4 > inner {
		note, notePlain = "", ""
	}
	fill := max(inner-1-width(title)-width(notePlain)-1, 0)

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+edge("╭─")+bold(title)+edge(strings.Repeat("─", fill))+note+edge("─╮"))
	line := func(cells []string) {
		fmt.Fprintln(w, "  "+edge("│")+" "+strings.Join(cells, "  ")+" "+edge("│"))
	}
	var head []string
	for _, c := range keys {
		head = append(head, dim(pad(c.title, c.key, c.right)))
	}
	line(head)
	for _, r := range rows {
		var cells []string
		for _, c := range keys {
			ce := r[c.key]
			padded := pad(ce.text, c.key, c.right)
			if ce.paint == nil {
				cells = append(cells, padded)
			} else {
				cells = append(cells, ce.paint(padded))
			}
		}
		line(cells)
	}
	fmt.Fprintln(w, "  "+edge("╰"+strings.Repeat("─", inner)+"╯"))

	printAttention(w, infos, cols-2)
	fmt.Fprintln(w)
}

// truncate cuts s to n columns with an ellipsis.
func truncate(s string, n int) string {
	if width(s) <= n {
		return s
	}
	if n <= 1 {
		return string([]rune(s)[:max(n, 0)])
	}
	return string([]rune(s)[:n-1]) + "…"
}

// attention is one thing that needs a look: a mark, the process it is about,
// what is wrong, and the command that helps.
type attention struct {
	mark  string // "✗" for something broken, "!" for something to look at
	paint func(string) string
	name  string
	text  string
	hint  string
}

// printAttention says, under the table, what is wrong and what to do about it.
// Each line fits room columns: the hint goes first when it does not fit, then
// the text is cut.
func printAttention(w io.Writer, infos []proc.View, room int) {
	var items []attention
	orphans := 0
	for _, in := range infos {
		switch {
		case in.Status == proc.StatusErrored:
			items = append(items, attention{"✗", red, in.Name, fmt.Sprintf("gave up after %d %s (exit code %d)", in.Restarts, plural(in.Restarts, "restart", "restarts"), in.ExitCode), "procly logs " + in.Name})
		case in.Status == proc.StatusOrphaned:
			orphans++
		case in.Status == proc.StatusOnline && in.Health == proc.HealthUnhealthy:
			items = append(items, attention{"!", yellow, in.Name, "is failing its health check", "procly describe " + in.Name})
		}
	}
	if orphans > 0 {
		items = append(items, attention{"!", yellow, "", fmt.Sprintf("%d %s left over from a daemon that died; nothing restarts them", orphans, plural(orphans, "process is", "processes are")), "procly stop <name>, then procly resurrect"})
	}
	if len(items) == 0 {
		return
	}
	fmt.Fprintln(w)
	for i, it := range items {
		if i == 5 {
			fmt.Fprintf(w, "  %s\n", dim(fmt.Sprintf("… and %d more", len(items)-5)))
			break
		}
		head := it.mark + " "
		name := it.name
		if name != "" {
			name += " "
		}
		text := it.text
		hint := "  " + it.hint
		if width(head+name+text+hint) > room {
			hint = ""
		}
		var line string
		if width(head+name+text) > room {
			line = it.paint(it.mark) + " " + truncate(name+text, max(room-width(head), 4))
		} else {
			line = it.paint(it.mark) + " " + bold(name) + text
		}
		if hint != "" {
			line += dim(hint)
		}
		fmt.Fprintln(w, "  "+line)
	}
}
