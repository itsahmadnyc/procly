package sysinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOSRelease(t *testing.T) {
	p := filepath.Join(t.TempDir(), "os-release")
	os.WriteFile(p, []byte("NAME=\"Ubuntu\"\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nID=ubuntu\n"), 0o644)
	if got := osRelease(p); got != "Ubuntu 24.04.1 LTS" {
		t.Errorf("osRelease = %q", got)
	}
	if got := osRelease(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Errorf("missing file gave %q", got)
	}
}
