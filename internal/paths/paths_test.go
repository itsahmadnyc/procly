package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHomeOverride(t *testing.T) {
	t.Setenv("PROCLY_HOME", "/srv/procly-test")
	if Home() != "/srv/procly-test" {
		t.Fatal(Home())
	}
	if Socket() != "/srv/procly-test/procly.sock" || HubSocket() != "/srv/procly-test/hub.sock" {
		t.Fatalf("sockets: %s %s", Socket(), HubSocket())
	}
	if LogDir() != "/srv/procly-test/logs" || DumpFile() != "/srv/procly-test/dump.json" {
		t.Fatalf("paths: %s %s", LogDir(), DumpFile())
	}
}

func TestHomeDefault(t *testing.T) {
	t.Setenv("PROCLY_HOME", "")
	h, _ := os.UserHomeDir()
	if Home() != filepath.Join(h, ".procly") {
		t.Fatal(Home())
	}
}

// Unix socket paths are limited to about 104 bytes.
func TestLongHomeFallsBackToShortSocket(t *testing.T) {
	long := "/tmp/" + strings.Repeat("d", 120)
	t.Setenv("PROCLY_HOME", long)
	for _, s := range []string{Socket(), HubSocket()} {
		if len(s) > 104 || strings.HasPrefix(s, long) {
			t.Fatalf("socket path %q is too long or still under the long home", s)
		}
	}
	if Socket() == HubSocket() {
		t.Fatal("daemon and hub sockets collide")
	}
	other := Socket()
	t.Setenv("PROCLY_HOME", long+"x")
	if Socket() == other {
		t.Fatal("two homes share one fallback socket")
	}
}

func TestEnsureIsPrivate(t *testing.T) {
	home := filepath.Join(t.TempDir(), "h")
	t.Setenv("PROCLY_HOME", home)
	if err := Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{home, LogDir()} {
		fi, err := os.Stat(d)
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", d, fi.Mode().Perm(), err)
		}
	}
}
