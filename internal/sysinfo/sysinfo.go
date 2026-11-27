// Package sysinfo reads a few facts about the machine for the web panel:
// load, memory, disk and uptime. Values it can't read on a platform are zero.
package sysinfo

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/itsahmadnyc/procly/internal/version"
)

type System struct {
	Hostname     string     `json:"hostname"`
	OS           string     `json:"os"`
	Distro       string     `json:"distro,omitempty"`
	Kernel       string     `json:"kernel,omitempty"`
	Arch         string     `json:"arch"`
	CPUs         int        `json:"cpus"`
	Load         [3]float64 `json:"load"`
	MemTotal     uint64     `json:"mem_total"`
	MemAvailable uint64     `json:"mem_available"` // 0 when unknown
	DiskTotal    uint64     `json:"disk_total"`    // the filesystem holding /
	DiskFree     uint64     `json:"disk_free"`
	UptimeS      int64      `json:"uptime_s"`
	Version      string     `json:"version"` // procly

	CPUModel  string   `json:"cpu_model,omitempty"`
	SwapTotal uint64   `json:"swap_total,omitempty"`
	SwapFree  uint64   `json:"swap_free,omitempty"`
	Addrs     []string `json:"addrs,omitempty"`    // "eth0 81.17.101.228", the addresses of this machine
	Node      string   `json:"node,omitempty"`     // the node on PATH, "v20.11.0"
	User      string   `json:"user,omitempty"`     // who procly runs as
	Timezone  string   `json:"timezone,omitempty"` // the server's local time, which restart_at uses
}

func Read() System {
	s := System{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), Version: version.Version}
	s.Hostname, _ = os.Hostname()
	read(&s)
	s.Addrs = addrs()
	s.Node = nodeVersion()
	if u, err := user.Current(); err == nil {
		s.User = u.Username
	}
	s.Timezone, _ = time.Now().Zone()
	if loc := time.Now().Location().String(); loc != "" && loc != "Local" {
		s.Timezone = loc + " (" + s.Timezone + ")"
	}
	return s
}

// addrs lists the addresses of the machine's interfaces that mean something to
// a person: not loopback, not link-local.
func addrs() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		as, _ := ifc.Addrs()
		for _, a := range as {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() || ipn.IP.IsLinkLocalMulticast() {
				continue
			}
			out = append(out, ifc.Name+" "+ipn.IP.String())
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return !strings.Contains(out[i], ":") && strings.Contains(out[j], ":") }) // IPv4 first
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

var (
	nodeMu  sync.Mutex
	nodeVer string
	nodeAt  time.Time
)

// nodeVersion is what `node --version` prints, asked at most every five
// minutes: the panel reads system info every few seconds.
func nodeVersion() string {
	nodeMu.Lock()
	defer nodeMu.Unlock()
	if !nodeAt.IsZero() && time.Since(nodeAt) < 5*time.Minute {
		return nodeVer
	}
	nodeAt = time.Now()
	nodeVer = ""
	path, err := exec.LookPath("node")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, path, "--version").Output(); err == nil {
		nodeVer = strings.TrimSpace(string(out))
	}
	return nodeVer
}
