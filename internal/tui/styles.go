package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/itsahmadnyc/procly/internal/proc"
)

// Basic ANSI colors follow the user's terminal theme instead of imposing one.
var (
	colGreen  = lipgloss.Color("2")
	colRed    = lipgloss.Color("1")
	colYellow = lipgloss.Color("3")
	colMuted  = lipgloss.AdaptiveColor{Light: "245", Dark: "243"}
	colFaint  = lipgloss.AdaptiveColor{Light: "250", Dark: "238"}
	colSelBg  = lipgloss.AdaptiveColor{Light: "254", Dark: "236"}

	sBold  = lipgloss.NewStyle().Bold(true)
	sMuted = lipgloss.NewStyle().Foreground(colMuted)
	sFaint = lipgloss.NewStyle().Foreground(colFaint)
	sRed   = lipgloss.NewStyle().Foreground(colRed)
	sKey   = lipgloss.NewStyle().Bold(true)
)

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

var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// spark draws the last n values. With fromZero the scale starts at 0 (CPU);
// otherwise it starts at the lowest value (memory). minSpan keeps small
// wobbles from filling the whole height.
func spark(vals []float64, n int, fromZero bool, minSpan float64) string {
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
