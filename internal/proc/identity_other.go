//go:build !linux

package proc

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func identity(pid int) (string, bool) {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	s := strings.TrimSpace(string(out))
	if err != nil || s == "" {
		return "", false
	}
	return "ps:" + s, true
}
