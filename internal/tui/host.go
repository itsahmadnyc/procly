package tui

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/itsahmadnyc/procly/internal/sysinfo"
)

// hostStats is the whole machine, for the header. Zero fields are unknown.
type hostStats struct {
	cpu      float64 // percent of all cores, -1 when unknown
	memUsed  int64
	memTotal int64
	load1    float64
	cores    int
}

func newHostStats() hostStats { return hostStats{cpu: -1, cores: runtime.NumCPU()} }

// readHost reads memory and load like the web panel does, plus CPU where it
// can be measured.
func readHost() hostStats {
	s := sysinfo.Read()
	h := hostStats{cpu: cpuPercent(), cores: s.CPUs, load1: s.Load[0]}
	if s.MemTotal > 0 && s.MemAvailable > 0 { // macOS does not report available memory
		h.memTotal, h.memUsed = int64(s.MemTotal), int64(s.MemTotal-s.MemAvailable)
	}
	return h
}

// line renders the stats that are known, as short as the width allows.
func (h hostStats) line(maxW int) string {
	var parts []string
	if h.cpu >= 0 {
		parts = append(parts, sMuted.Render("cpu ")+level(h.cpu/100).Render(fmt.Sprintf("%.0f%%", h.cpu)))
	}
	if h.memTotal > 0 {
		frac := float64(h.memUsed) / float64(h.memTotal)
		parts = append(parts, sMuted.Render("mem ")+level(frac).Render(shortBytes(h.memUsed))+sMuted.Render("/"+shortBytes(h.memTotal)))
	}
	if h.load1 > 0 {
		parts = append(parts, sMuted.Render("load ")+level(h.load1/float64(max(h.cores, 1))).Render(fmt.Sprintf("%.2f", h.load1)))
	}
	for len(parts) > 0 {
		s := strings.Join(parts, sFaint.Render("  "))
		if lipgloss.Width(s) <= maxW {
			return s
		}
		parts = parts[:len(parts)-1]
	}
	return ""
}

// shortBytes is fmtBytes without the space, for tight places.
func shortBytes(b int64) string {
	const gb = 1 << 30
	if b >= gb {
		return fmt.Sprintf("%.1fG", float64(b)/gb)
	}
	return fmt.Sprintf("%dM", b>>20)
}
