// Package sysinfo reads a few facts about the machine for the web panel:
// load, memory, disk and uptime. Values it can't read on a platform are zero.
package sysinfo

import (
	"os"
	"runtime"

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
}

func Read() System {
	s := System{OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), Version: version.Version}
	s.Hostname, _ = os.Hostname()
	read(&s)
	return s
}
