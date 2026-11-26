//go:build !linux

package tui

// cpuPercent is unknown off Linux: measuring it needs two samples from
// host_statistics, which the standard library cannot reach.
func cpuPercent() float64 { return -1 }
