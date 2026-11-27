//go:build linux

package proc

import (
	"encoding/hex"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ListeningPorts returns the TCP addresses that the processes of process
// group pgid listen on: the sockets their open files point to, matched
// against the kernel's tables of listening sockets.
func ListeningPorts(pgid int) []netip.AddrPort {
	inodes := map[string]bool{}
	for _, pid := range groupPIDs(pgid) {
		dir := "/proc/" + strconv.Itoa(pid) + "/fd"
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(dir + "/" + fd.Name())
			if err == nil && strings.HasPrefix(link, "socket:[") {
				inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
			}
		}
	}
	if len(inodes) == 0 {
		return nil
	}
	seen := map[netip.AddrPort]bool{}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(table)
		if err != nil {
			continue
		}
		for _, a := range parseListening(string(data), inodes) {
			seen[a] = true
		}
	}
	out := make([]netip.AddrPort, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Compare(out[j]) < 0 })
	return out
}

// groupPIDs lists the processes whose process group is pgid.
func groupPIDs(pgid int) []int {
	var pids []int
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if pg, ok := pgrpOf(pid); ok && pg == pgid {
			pids = append(pids, pid)
		}
	}
	return pids
}

// pgrpOf reads a process's group id from /proc/<pid>/stat.
func pgrpOf(pid int) (int, bool) {
	st, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(st)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 3 { // f[0] state, f[1] ppid, f[2] process group
		return 0, false
	}
	pg, err := strconv.Atoi(f[2])
	return pg, err == nil
}

// parseListening reads /proc/net/tcp or tcp6 and returns the local addresses
// of listening sockets (state 0A) whose inode is in inodes.
func parseListening(table string, inodes map[string]bool) []netip.AddrPort {
	var out []netip.AddrPort
	for _, line := range strings.Split(table, "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 10 || f[3] != "0A" || !inodes[f[9]] {
			continue
		}
		if a, ok := parseHexAddr(f[1]); ok {
			out = append(out, a)
		}
	}
	return out
}

// parseHexAddr decodes "0100007F:1F90": the address in host byte order, 32
// bits at a time (little-endian on every Linux procly supports), then the port.
func parseHexAddr(s string) (netip.AddrPort, bool) {
	host, port, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, false
	}
	raw, err := hex.DecodeString(host)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, false
	}
	for i := 0; i < len(raw); i += 4 { // each 32-bit word is little-endian
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	pn, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return netip.AddrPort{}, false
	}
	ip, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(ip.Unmap(), uint16(pn)), true
}
