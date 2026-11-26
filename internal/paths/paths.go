// Package paths resolves where procly keeps its state on disk.
package paths

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
)

// Home is the procly state directory. Override with PROCLY_HOME.
func Home() string {
	if v := os.Getenv("PROCLY_HOME"); v != "" {
		return v
	}
	h, err := os.UserHomeDir()
	if err != nil {
		h = os.TempDir()
	}
	return filepath.Join(h, ".procly")
}

// Socket is the unix socket the daemon listens on. Unix socket paths are
// limited to ~104 bytes, so a long home falls back to a hashed temp path.
func Socket() string { return socketPath("procly") }

// HubSocket is where `procly hub` serves connected agents to local clients.
func HubSocket() string { return socketPath("hub") }

func socketPath(name string) string {
	p := filepath.Join(Home(), name+".sock")
	if len(p) <= 100 {
		return p
	}
	sum := sha1.Sum([]byte(Home()))
	return filepath.Join(os.TempDir(), name+"-"+hex.EncodeToString(sum[:4])+".sock")
}

func LogDir() string    { return filepath.Join(Home(), "logs") }
func PidFile() string   { return filepath.Join(Home(), "daemon.pid") }
func DaemonLog() string { return filepath.Join(Home(), "daemon.log") }
func DumpFile() string  { return filepath.Join(Home(), "dump.json") }

// RunFile lists the processes the daemon has running, so a later daemon can
// tell which of them outlived it.
func RunFile() string { return filepath.Join(Home(), "running.json") }

// UpgradeFile holds what an upgrade is handing to the new daemon, so an
// interrupted upgrade can be finished by running it again.
func UpgradeFile() string { return filepath.Join(Home(), "upgrade.json") }

// Ensure creates the state directories with owner-only permissions.
func Ensure() error {
	return os.MkdirAll(LogDir(), 0o700)
}
