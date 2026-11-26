package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/itsahmadnyc/procly/internal/proc"
)

var colorOn = func() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

func paint(code, s string) string {
	if !colorOn || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string   { return paint("1", s) }
func dim(s string) string    { return paint("2", s) }
func red(s string) string    { return paint("31", s) }
func green(s string) string  { return paint("32", s) }
func yellow(s string) string { return paint("33", s) }
func gray(s string) string   { return paint("90", s) }

func statusColor(s proc.Status) func(string) string {
	switch s {
	case proc.StatusOnline:
		return green
	case proc.StatusWaiting, proc.StatusOrphaned:
		return yellow
	case proc.StatusErrored:
		return red
	default:
		return gray
	}
}

func statusLabel(s proc.Status) string { return "● " + string(s) }

func width(s string) int { return utf8.RuneCountInString(s) }

// renderTable pads plain cells first, then styles them, so ANSI codes never
// throw off the alignment.
func renderTable(w io.Writer, headers []string, rows [][]string, right map[int]bool, style func(row, col int, s string) string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = width(h)
	}
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], width(c))
		}
	}
	pad := func(s string, col int) string {
		gap := strings.Repeat(" ", widths[col]-width(s))
		if right[col] {
			return gap + s
		}
		return s + gap
	}
	line := func(cells []string, styler func(col int, s string) string) {
		parts := make([]string, len(cells))
		for i, c := range cells {
			parts[i] = styler(i, pad(c, i))
		}
		fmt.Fprintln(w, "  "+strings.TrimRight(strings.Join(parts, "   "), " "))
	}
	line(headers, func(_ int, s string) string { return dim(s) })
	for ri, r := range rows {
		line(r, func(col int, s string) string { return style(ri, col, s) })
	}
}

// printList shows processes; server is "" for this machine.
func printList(w io.Writer, infos []proc.View, server string) {
	if len(infos) == 0 {
		if server != "" {
			fmt.Fprintln(w, dim("  No processes on "+server+"."))
			return
		}
		fmt.Fprintln(w, dim("  No processes. Start one with: procly start app.js"))
		return
	}
	headers := []string{"id", "name", "status", "pid", "uptime", "restarts", "cpu", "memory"}
	withHealth := false
	for _, in := range infos {
		if in.HealthCheck != "" {
			withHealth = true
		}
	}
	if withHealth {
		headers = append(headers, "health")
	}
	rows := make([][]string, len(infos))
	counts := map[proc.Status]int{}
	for i, in := range infos {
		counts[in.Status]++
		pid, up, cpu, mem := "-", "-", "-", "-"
		if in.PID > 0 {
			pid = fmt.Sprint(in.PID)
			up = fmtDuration(time.Duration(in.UptimeMs) * time.Millisecond)
			cpu = fmt.Sprintf("%.1f%%", in.CPU)
			mem = fmtBytes(in.Memory)
		}
		rows[i] = []string{fmt.Sprint(in.ID), in.Name, statusLabel(in.Status), pid, up, fmt.Sprint(in.Restarts), cpu, mem}
		if withHealth {
			h := string(in.Health)
			if in.HealthCheck == "" || h == "" {
				h = "-"
			}
			rows[i] = append(rows[i], h)
		}
	}
	right := map[int]bool{0: true, 3: true, 5: true, 6: true, 7: true}
	fmt.Fprintln(w)
	renderTable(w, headers, rows, right, func(row, col int, s string) string {
		switch col {
		case 1:
			return bold(s)
		case 2:
			return statusColor(infos[row].Status)(s)
		case 5:
			if infos[row].Restarts > 0 {
				return yellow(s)
			}
			return s
		case 0, 3:
			return dim(s)
		case 8:
			switch infos[row].Health {
			case proc.HealthHealthy:
				return green(s)
			case proc.HealthUnhealthy:
				return red(s)
			}
			return dim(s)
		}
		return s
	})

	summary := []string{fmt.Sprintf("%d %s", len(infos), plural(len(infos), "process", "processes"))}
	if server != "" {
		summary[0] += " on " + server
	}
	for _, s := range []proc.Status{proc.StatusOnline, proc.StatusWaiting, proc.StatusOrphaned, proc.StatusStopped, proc.StatusErrored} {
		if counts[s] > 0 {
			summary = append(summary, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, dim("  "+strings.Join(summary, " · ")))
	if n := counts[proc.StatusOrphaned]; n > 0 {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %s %d %s left over from a daemon that died. Nothing restarts them or rotates their logs.\n",
			yellow("!"), n, plural(n, "process is", "processes are"))
		fmt.Fprintln(w, dim("    procly stop <name> ends one, then procly resurrect or procly start brings it back; procly kill ends everything."))
	}
	fmt.Fprintln(w)
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

// tilde shortens paths under the home directory for display.
func tilde(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h+"/") {
		return "~" + p[len(h):]
	}
	return p
}
