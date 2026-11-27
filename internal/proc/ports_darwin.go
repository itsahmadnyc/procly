//go:build darwin

package proc

import (
	"bufio"
	"bytes"
	"net"
	"net/netip"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// ListeningPorts on macOS asks ps for the members of the process group and
// lsof for their listening TCP sockets.
func ListeningPorts(pgid int) []netip.AddrPort {
	out, err := exec.Command("ps", "-axo", "pid=,pgid=").Output()
	if err != nil {
		return nil
	}
	var pids []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && f[1] == strconv.Itoa(pgid) {
			pids = append(pids, f[0])
		}
	}
	if len(pids) == 0 {
		return nil
	}
	// lsof exits 1 when it finds nothing; -F n prints one "n<address>:<port>" line per socket
	data, _ := exec.Command("lsof", "-nP", "-a", "-p", strings.Join(pids, ","), "-iTCP", "-sTCP:LISTEN", "-Fn").Output()
	seen := map[netip.AddrPort]bool{}
	sc = bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "n") {
			continue
		}
		host, port, err := net.SplitHostPort(strings.TrimPrefix(line, "n"))
		if err != nil {
			continue
		}
		p, err := strconv.Atoi(port)
		if err != nil {
			continue
		}
		ip := netip.IPv4Unspecified()
		if host != "*" {
			if a, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
				ip = a.Unmap()
			}
		}
		seen[netip.AddrPortFrom(ip, uint16(p))] = true
	}
	out2 := make([]netip.AddrPort, 0, len(seen))
	for a := range seen {
		out2 = append(out2, a)
	}
	sort.Slice(out2, func(i, j int) bool { return out2[i].Compare(out2[j]) < 0 })
	return out2
}
