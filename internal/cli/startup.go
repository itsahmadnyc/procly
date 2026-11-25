package cli

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"procly/internal/paths"
)

func cmdStartup() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	switch runtime.GOOS {
	case "linux":
		return startupSystemd(exe)
	case "darwin":
		return startupLaunchd(exe)
	default:
		return fmt.Errorf("startup is not supported on %s yet", runtime.GOOS)
	}
}

const systemdUnit = `[Unit]
Description=procly process manager
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
Environment=HOME=%s
Environment=PATH=%s
ExecStart=%s daemon --resurrect
ExecStop=%s kill
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
`

func startupSystemd(exe string) error {
	if os.Geteuid() != 0 {
		fmt.Print("\n  Installing a systemd service needs root. Run:\n\n")
		fmt.Println("  " + bold("sudo env PATH=$PATH "+exe+" startup") + "\n")
		return nil
	}
	name := os.Getenv("SUDO_USER")
	if name == "" {
		name = "root"
	}
	u, err := user.Lookup(name)
	if err != nil {
		return err
	}
	unit := fmt.Sprintf(systemdUnit, u.Username, u.HomeDir, os.Getenv("PATH"), exe, exe)
	path := "/etc/systemd/system/procly.service"
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "procly"}} {
		if out, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	fmt.Printf("  %s %s for user %s\n", green("installed"), path, u.Username)
	fmt.Println(dim("  On boot procly restores what you last saved. Run 'procly save' after starting your apps."))
	return nil
}

const launchdPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.procly.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
    <string>--resurrect</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>%s</string></dict>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`

func startupLaunchd(exe string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	log := esc(paths.DaemonLog())
	plist := fmt.Sprintf(launchdPlist, esc(exe), esc(os.Getenv("PATH")), log, log)
	path := filepath.Join(dir, "dev.procly.daemon.plist")
	if err := os.WriteFile(path, []byte(plist), 0o644); err != nil {
		return err
	}
	fmt.Printf("  %s %s\n", green("installed"), tilde(path))
	fmt.Println(dim("  procly will start when you log in and restore what you last saved."))
	fmt.Println(dim("  Run 'procly save' after starting your apps."))
	return nil
}
