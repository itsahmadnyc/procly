//go:build !linux

package proc

import "net/netip"

// ListeningPorts is only implemented on Linux, where /proc shows which
// sockets a process holds. Elsewhere restarts do not wait for ports; a
// health check still makes them wait.
func ListeningPorts(int) []netip.AddrPort { return nil }
