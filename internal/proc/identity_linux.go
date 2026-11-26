//go:build linux

package proc

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

var (
	bootOnce sync.Once
	bootID   string
)

func identity(pid int) (string, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	s := string(b)
	// The command name is in parentheses and may contain spaces and parentheses
	// itself, so count fields after the last one.
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", false
	}
	f := strings.Fields(s[i+1:])
	// f[0] is field 3 (state); the start time is field 22, so f[19].
	if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
		return "", false
	}
	bootOnce.Do(func() {
		if b, err := os.ReadFile("/proc/sys/kernel/random/boot_id"); err == nil {
			bootID = strings.TrimSpace(string(b))
		}
	})
	// The start time counts from boot, so the boot id keeps pids from before a
	// reboot from matching.
	return bootID + ":" + f[19], true
}
