package tui

import (
	"fmt"
	"os"
	"strings"
	"time"
)

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
