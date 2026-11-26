package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/itsahmadnyc/procly/internal/proc"
)

// Basic ANSI colors follow the user's terminal theme instead of imposing one.
var (
	colGreen  = lipgloss.Color("2")
	colRed    = lipgloss.Color("1")
	colYellow = lipgloss.Color("3")
	colAccent = lipgloss.Color("6")
	colMuted  = lipgloss.AdaptiveColor{Light: "245", Dark: "243"}
	colFaint  = lipgloss.AdaptiveColor{Light: "250", Dark: "238"}
	colSelBg  = lipgloss.AdaptiveColor{Light: "254", Dark: "236"}
	colMatch  = lipgloss.AdaptiveColor{Light: "222", Dark: "58"}

	sBold   = lipgloss.NewStyle().Bold(true)
	sMuted  = lipgloss.NewStyle().Foreground(colMuted)
	sFaint  = lipgloss.NewStyle().Foreground(colFaint)
	sRed    = lipgloss.NewStyle().Foreground(colRed)
	sGreen  = lipgloss.NewStyle().Foreground(colGreen)
	sYellow = lipgloss.NewStyle().Foreground(colYellow)
	sAccent = lipgloss.NewStyle().Foreground(colAccent)
	sKey    = lipgloss.NewStyle().Bold(true)
	sMatch  = lipgloss.NewStyle().Background(colMatch)
)

// sourceColors tell the instances of a group apart in a shared log pane.
var sourceColors = []lipgloss.TerminalColor{lipgloss.Color("6"), lipgloss.Color("5"), lipgloss.Color("4"), lipgloss.Color("2"), lipgloss.Color("3")}

func statusColor(s proc.Status) lipgloss.TerminalColor {
	switch s {
	case proc.StatusOnline:
		return colGreen
	case proc.StatusWaiting, proc.StatusOrphaned:
		return colYellow
	case proc.StatusErrored:
		return colRed
	default:
		return colMuted
	}
}

// level colors a value by how close it is to its limit.
func level(frac float64) lipgloss.Style {
	switch {
	case frac >= 0.9:
		return sRed
	case frac >= 0.7:
		return sYellow
	}
	return lipgloss.NewStyle()
}

var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// spark draws the last n values. With fromZero the scale starts at 0 (CPU);
// otherwise it starts at the lowest value (memory). minSpan keeps small
// wobbles from filling the whole height.
func spark(vals []float64, n int, fromZero bool, minSpan float64) string {
	if n <= 0 {
		return ""
	}
	if len(vals) > n {
		vals = vals[len(vals)-n:]
	}
	lo, hi := 0.0, 0.0
	for i, v := range vals {
		if i == 0 || v < lo {
			lo = v
		}
		hi = max(hi, v)
	}
	if fromZero {
		lo = 0
	}
	span := max(hi-lo, minSpan)
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", n-len(vals)))
	for _, v := range vals {
		i := 0
		if span > 0 {
			i = int((v-lo)/span*float64(len(sparkBlocks)-1) + 0.5)
		}
		b.WriteRune(sparkBlocks[min(max(i, 0), len(sparkBlocks)-1)])
	}
	return b.String()
}

// bar is a meter n cells wide, filled to frac and colored by level.
func bar(frac float64, n int) string {
	if n <= 0 {
		return ""
	}
	frac = min(max(frac, 0), 1)
	full := int(frac*float64(n) + 0.5)
	if frac > 0 && full == 0 {
		full = 1
	}
	st := level(frac)
	if frac < 0.7 {
		st = sGreen
	}
	return st.Render(strings.Repeat("━", full)) + sFaint.Render(strings.Repeat("━", n-full))
}

// fit cuts s to w cells and pads it to exactly w.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = ansi.Truncate(s, w, "…")
	if gap := w - lipgloss.Width(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

// spread puts left and right on one line w wide, dropping right when both
// do not fit.
func spread(left, right string, w int) string {
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if right == "" || gap < 2 {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// box draws a rounded frame w by h with a title on its top edge. The body is
// cut or padded to the inside of the frame.
func box(title, note string, body []string, w, h int, focused bool) []string {
	if w < 6 || h < 2 {
		out := make([]string, max(h, 0))
		for i := range out {
			if i < len(body) {
				out[i] = fit(body[i], max(w, 0))
			} else {
				out[i] = fit("", max(w, 0))
			}
		}
		return out
	}
	edge, head := sFaint, sMuted
	if focused {
		edge, head = sAccent, sBold
	}
	inner := w - 2
	left := " " + title + " "
	right := ""
	if note != "" {
		right = " " + note + " "
	}
	if lipgloss.Width(left)+lipgloss.Width(right)+2 > inner {
		right = ""
	}
	if lipgloss.Width(left)+2 > inner {
		left = ansi.Truncate(left, inner-2, "…")
	}
	fill := inner - 1 - lipgloss.Width(left) - lipgloss.Width(right) - 1
	top := edge.Render("╭─") + head.Render(left) + edge.Render(strings.Repeat("─", max(fill, 0))) + sMuted.Render(right) + edge.Render("─╮")
	out := []string{top}
	for i := 0; i < h-2; i++ {
		line := ""
		if i < len(body) {
			line = body[i]
		}
		out = append(out, edge.Render("│")+" "+fit(line, inner-2)+" "+edge.Render("│"))
	}
	out = append(out, edge.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return out
}
