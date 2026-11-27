package proc

import (
	"net"
	"net/netip"
	"sort"
	"time"
)

// WaitListening waits until every address accepts a TCP connection, up to
// limit. alive reports whether the process is still worth waiting for; when
// it says no, the wait ends early. It returns whether all addresses answered.
//
// An address bound to every interface (0.0.0.0, ::) is tried on loopback.
func WaitListening(addrs []netip.AddrPort, limit time.Duration, alive func() bool) bool {
	deadline := time.Now().Add(limit)
	pending := append([]netip.AddrPort(nil), addrs...)
	for {
		var still []netip.AddrPort
		for _, a := range pending {
			if !dialable(a) {
				still = append(still, a)
			}
		}
		pending = still
		if len(pending) == 0 {
			return true
		}
		if time.Now().After(deadline) || (alive != nil && !alive()) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func dialable(a netip.AddrPort) bool {
	ip := a.Addr()
	if ip.IsUnspecified() {
		if ip.Is4() {
			ip = netip.AddrFrom4([4]byte{127, 0, 0, 1})
		} else {
			ip = netip.IPv6Loopback()
		}
	}
	c, err := net.DialTimeout("tcp", netip.AddrPortFrom(ip, a.Port()).String(), 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// restartWaiting restarts p and, when it was listening on TCP ports before,
// waits (up to limit) until the new process listens on them again, so a
// rolling restart only moves on once this one serves. It returns how long it
// waited for the ports.
func (p *Process) restartWaiting(limit time.Duration) (time.Duration, error) {
	p.mu.Lock()
	pid := p.pid
	p.mu.Unlock()
	var ports []netip.AddrPort
	if pid > 0 && limit > 0 {
		ports = ListeningPorts(pid) // the process group's id is the first pid
	}
	if err := p.Restart(); err != nil {
		return 0, err
	}
	if len(ports) == 0 {
		return 0, nil
	}
	start := time.Now()
	WaitListening(ports, limit, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.cmd != nil && p.status == StatusOnline
	})
	return time.Since(start), nil
}

// PortNumbers returns the TCP ports each running process, with everything it
// started, listens on, by process name. Linux and macOS only; elsewhere the
// map is empty.
func (m *Manager) PortNumbers() map[string][]int {
	out := map[string][]int{}
	for _, in := range m.List() {
		if in.PID <= 0 {
			continue
		}
		seen := map[int]bool{}
		var ports []int
		for _, a := range ListeningPorts(in.PID) {
			if p := int(a.Port()); !seen[p] {
				seen[p] = true
				ports = append(ports, p)
			}
		}
		sort.Ints(ports)
		out[in.Name] = ports
	}
	return out
}
