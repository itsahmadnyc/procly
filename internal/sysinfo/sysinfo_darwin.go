package sysinfo

import (
	"encoding/binary"
	"syscall"
	"time"
)

func read(s *System) {
	if v, err := syscall.Sysctl("kern.osproductversion"); err == nil {
		s.Distro = "macOS " + v
	}
	s.Kernel, _ = syscall.Sysctl("kern.osrelease")
	s.CPUModel, _ = syscall.Sysctl("machdep.cpu.brand_string")
	// struct xsw_usage { u_int64_t xsu_total, xsu_avail, xsu_used; ... }
	if b := sysctlBytes("vm.swapusage"); len(b) >= 16 {
		s.SwapTotal = binary.LittleEndian.Uint64(b[0:8])
		s.SwapFree = binary.LittleEndian.Uint64(b[8:16])
	}
	if b := sysctlBytes("hw.memsize"); len(b) >= 8 {
		s.MemTotal = binary.LittleEndian.Uint64(b)
	}
	// struct loadavg { fixpt_t ldavg[3]; long fscale; }
	if b := sysctlBytes("vm.loadavg"); len(b) >= 24 {
		scale := float64(binary.LittleEndian.Uint64(b[16:24]))
		for i := 0; i < 3 && scale > 0; i++ {
			s.Load[i] = float64(binary.LittleEndian.Uint32(b[i*4:])) / scale
		}
	}
	// struct timeval { time_t tv_sec; ... }
	if b := sysctlBytes("kern.boottime"); len(b) >= 8 {
		boot := int64(binary.LittleEndian.Uint64(b))
		s.UptimeS = time.Now().Unix() - boot
	}
	var st syscall.Statfs_t
	if syscall.Statfs("/", &st) == nil {
		s.DiskTotal = st.Blocks * uint64(st.Bsize)
		s.DiskFree = st.Bavail * uint64(st.Bsize)
	}
}

// sysctlBytes returns a sysctl's raw value. syscall.Sysctl drops a trailing
// NUL byte, which is part of binary values, so it is put back.
func sysctlBytes(name string) []byte {
	v, err := syscall.Sysctl(name)
	if err != nil {
		return nil
	}
	return append([]byte(v), 0)
}
