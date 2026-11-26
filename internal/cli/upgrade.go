package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/itsahmadnyc/procly/internal/client"
	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/upgrade"
	"github.com/itsahmadnyc/procly/internal/version"
)

// cmdUpgrade installs a newer release and hands the daemon over to it.
//
// The binary is replaced first. Then, unless told not to, the running daemon
// passes its list on and exits WITHOUT stopping the apps, a daemon from the new
// binary starts, and each app is restarted once, one at a time (the instances of
// a group roll). Stopped apps stay stopped, and the saved list is not touched.
//
// Two cases cannot hand over: a daemon run by systemd (stopping the unit would
// stop the apps with it), where the binary is replaced and the command says how
// to restart the service, and a binary that Homebrew or npm owns, which they
// upgrade themselves.
func cmdUpgrade(argv []string) error {
	a, err := parse(argv, flagSet{valued: set("version"), bools: set("check", "dry-run", "no-restart", "force")})
	if err != nil {
		return err
	}
	if len(a.pos) > 0 {
		return errors.New("usage: procly upgrade [--check] [--dry-run] [--no-restart] [--force] [--version <v>]")
	}
	src := upgrade.Source{Base: os.Getenv("PROCLY_DOWNLOAD_BASE"), LatestURL: os.Getenv("PROCLY_LATEST_URL")}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	current := version.Version
	ctx := context.Background()

	// A previous upgrade that was interrupted after the old daemon left.
	if entries, ok := readUpgradeFile(); ok && !a.bools["check"] {
		fmt.Println(dim("  An earlier upgrade was interrupted; finishing it."))
		return resumeHandover(exe, entries)
	}

	want := strings.TrimPrefix(a.get("version"), "v")
	latest := want
	var lerr error
	if want == "" {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		latest, lerr = src.LatestVersion(cctx)
		cancel()
		if lerr != nil && src.Base == "" {
			return lerr
		}
	}

	daemon, daemonUp := daemonVersion()

	if a.bools["check"] {
		return reportCheck(current, latest, daemon, daemonUp)
	}
	if owner := upgrade.Owner(exe); owner != "" {
		return fmt.Errorf("this procly was installed by %s: upgrade it with %s", owner, map[string]string{"homebrew": "brew upgrade procly", "npm": "npm update -g procly"}[owner])
	}

	newer := needsBinary(a.bools["force"], want, latest, current)
	handoverOnly := !newer && daemonUp && daemon.Version != current

	switch {
	case !newer && !handoverOnly:
		fmt.Printf("  procly %s is up to date.\n", current)
		return nil
	case handoverOnly:
		fmt.Printf("  The binary is procly %s, but the daemon still runs %s.\n", current, daemon.Version)
	}

	canHandover := daemonUp && daemon.Supervisor == ""
	printUpgradePlan(current, latest, newer, daemonUp, daemon, canHandover, !a.bools["no-restart"])
	if a.bools["dry-run"] {
		fmt.Println(dim("  --dry-run: nothing was changed."))
		return nil
	}

	if newer {
		tmp, err := os.MkdirTemp("", "procly-upgrade-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		fmt.Println(dim("  downloading and checking the release…"))
		dctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		bin, got, err := src.Download(dctx, want, tmp)
		cancel()
		if err != nil {
			return err
		}
		if !a.bools["force"] && want == "" && upgrade.Compare(current, got) >= 0 {
			fmt.Printf("  procly %s is up to date (the release is %s).\n", current, got)
			return nil
		}
		if err := replaceBinary(exe, bin); err != nil {
			return err
		}
		fmt.Printf("  %s procly %s to %s %s\n", green("installed"), got, tilde(exe), dim("(the old one is kept as "+filepath.Base(exe)+".old until the daemon is handed over)"))
		current = got
	}

	if a.bools["no-restart"] || !daemonUp {
		upgrade.RemoveBackup(exe)
		if daemonUp {
			fmt.Println(dim("  The daemon and the apps keep running the old version. Run 'procly upgrade' again to hand the daemon over."))
		}
		return nil
	}
	if daemon.Supervisor != "" {
		upgrade.RemoveBackup(exe)
		fmt.Printf("\n  The daemon is run by %s, which stops the apps with it, so it was not handed over.\n", daemon.Supervisor)
		fmt.Println("  Restarting the service restarts every app together, from the last 'procly save':")
		fmt.Printf("\n    %s\n\n", bold("procly save && sudo systemctl restart procly"))
		return nil
	}

	if err := handover(exe); err != nil {
		return err
	}
	upgrade.RemoveBackup(exe)
	c := client.New()
	return showList(c)
}

// needsBinary says whether a new binary has to be downloaded. A version asked
// for with --version is installed whenever it differs from this one (that
// includes going back); otherwise the latest release is installed when it is
// newer. When the latest is not known (a mirror without a latest page) the
// download itself tells, and the caller compares afterwards.
func needsBinary(force bool, want, latest, current string) bool {
	switch {
	case force:
		return true
	case want != "":
		return upgrade.Compare(current, want) != 0
	case latest == "":
		return true
	}
	return upgrade.Compare(current, latest) < 0
}

// daemonVersion is what the running daemon says, and whether there is one.
func daemonVersion() (client.Ping, bool) {
	p, err := client.New().Ping()
	return p, err == nil
}

func reportCheck(current, latest string, daemon client.Ping, daemonUp bool) error {
	switch {
	case latest == "":
		fmt.Printf("  procly %s. Could not tell which release is the latest.\n", current)
	case upgrade.Compare(current, latest) < 0:
		fmt.Printf("  procly %s is available; you have %s. Run: %s\n", latest, current, bold("procly upgrade"))
	case upgrade.Compare(current, latest) > 0:
		fmt.Printf("  procly %s is newer than the latest release (%s).\n", current, latest)
	default:
		fmt.Printf("  procly %s is the latest version.\n", current)
	}
	if daemonUp && daemon.Version != current {
		fmt.Printf("  The daemon still runs %s; 'procly upgrade' hands it over to %s.\n", daemon.Version, current)
	}
	return nil
}

func printUpgradePlan(current, latest string, newer, daemonUp bool, daemon client.Ping, canHandover, restart bool) {
	if newer {
		to := latest
		if to == "" {
			to = "the latest release"
		}
		fmt.Printf("  procly %s -> %s\n", current, to)
	}
	switch {
	case !daemonUp:
		fmt.Println(dim("  No daemon is running, so only the binary is replaced."))
	case !restart:
		fmt.Println(dim("  --no-restart: the daemon and the apps keep running the old version."))
	case !canHandover:
		fmt.Printf(dim("  The daemon is run by %s: it will not be handed over (see below).\n"), daemon.Supervisor)
	default:
		n := 0
		if all, err := client.New().List(); err == nil {
			for _, in := range all {
				if in.Status == proc.StatusOnline || in.Status == proc.StatusWaiting {
					n++
				}
			}
		}
		fmt.Printf("  The daemon hands over to the new one and each of the %d running %s is restarted once, one at a time.\n", n, plural(n, "app", "apps"))
		fmt.Println(dim("  Stopped apps stay stopped, and your saved list is not changed. Apps restart a moment after each other, so a group keeps serving."))
	}
}

// replaceBinary swaps the binary in, using sudo when the folder is not writable
// by this user (as install.sh does).
func replaceBinary(exe, newBin string) error {
	err := upgrade.Replace(exe, newBin)
	if !errors.Is(err, upgrade.ErrNeedsPrivilege) {
		return err
	}
	if _, lerr := exec.LookPath("sudo"); lerr != nil {
		return fmt.Errorf("%s is not writable by you and sudo is not available: run procly upgrade as a user that can write there", filepath.Dir(exe))
	}
	fmt.Printf("  %s is not writable by you; using sudo to replace the binary.\n", filepath.Dir(exe))
	tmp := filepath.Join(filepath.Dir(exe), ".procly-upgrade-sudo")
	for _, args := range [][]string{
		{"install", "-m", "0755", newBin, tmp},
		{"mv", "-f", exe, exe + ".old"},
		{"mv", "-f", tmp, exe},
	} {
		cmd := exec.Command("sudo", args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			_ = exec.Command("sudo", "rm", "-f", tmp).Run()
			if _, serr := os.Stat(exe); serr != nil {
				_ = exec.Command("sudo", "mv", "-f", exe+".old", exe).Run() // put it back
			}
			return fmt.Errorf("sudo %s: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

// handover moves the daemon to the binary now at exe, restarting each app once.
func handover(exe string) error {
	old := client.New()
	entries, err := old.Handover()
	if err != nil {
		return fmt.Errorf("the daemon would not hand over: %w", err)
	}
	if err := writeUpgradeFile(entries); err != nil {
		return err
	}
	fmt.Println(dim("  handing over; the apps keep running until their turn…"))
	if !waitGone(old, 20*time.Second) {
		return errors.New("the old daemon did not exit; the apps are still running under it, nothing was restarted")
	}
	c, err := startDaemon(client.New(), exe)
	if err != nil {
		// The new binary's daemon will not start: go back to the old one, which
		// finds the apps still running, and restart them under it.
		fmt.Fprintf(os.Stderr, "  %s the new daemon did not start (%v); going back to the previous version\n", yellow("!"), err)
		if rerr := upgrade.Restore(exe); rerr != nil {
			return fmt.Errorf("%v; and the previous binary could not be put back: %v (it is %s.old)", err, rerr, exe)
		}
		c, err2 := startDaemon(client.New(), exe)
		if err2 != nil {
			return fmt.Errorf("%v; and the previous version would not start either: %v", err, err2)
		}
		if ferr := finishHandover(c, entries); ferr != nil {
			return ferr
		}
		return fmt.Errorf("the upgrade was rolled back: %w", err)
	}
	return finishHandover(c, entries)
}

// resumeHandover finishes a handover that was interrupted: the daemon is
// whichever is running, or a new one if none is.
func resumeHandover(exe string, entries []proc.HandoverEntry) error {
	c := client.New()
	if _, err := c.Ping(); err != nil {
		var serr error
		if c, serr = startDaemon(c, exe); serr != nil {
			return serr
		}
	}
	if err := finishHandover(c, entries); err != nil {
		return err
	}
	return showList(c)
}

// finishHandover takes each entry in turn: an app that was running is stopped
// where it is (the new daemon sees it as an orphan) and started again from its
// spec; one that was stopped is registered as stopped. Entries that are already
// done are skipped, so running this twice is safe.
func finishHandover(c *client.Client, entries []proc.HandoverEntry) error {
	var errs []error
	for _, e := range entries {
		name := e.Spec.Name
		now := map[string]proc.Info{}
		if all, err := c.List(); err == nil {
			for _, in := range all {
				now[in.Name] = in
			}
		}
		cur, exists := now[name]
		if !e.Running {
			if !exists {
				if _, err := c.Add(e.Spec); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, err))
					continue
				}
			}
			fmt.Printf("  %s %s\n", dim("kept stopped"), bold(name))
			continue
		}
		if exists && cur.Status != proc.StatusOrphaned {
			fmt.Printf("  %s %s %s\n", dim("already running"), bold(name), dim("(done earlier)"))
			continue
		}
		if exists {
			if _, err := c.Stop(name); err != nil {
				errs = append(errs, fmt.Errorf("stopping %s: %w", name, err))
				continue
			}
		}
		if _, err := c.Start(e.Spec); err != nil {
			errs = append(errs, fmt.Errorf("starting %s: %w", name, err))
			continue
		}
		note := waitReady(c, e.Spec)
		fmt.Printf("  %s %s%s\n", green("restarted"), bold(name), note)
	}
	if len(errs) > 0 {
		fmt.Fprintln(os.Stderr, dim("  Run 'procly upgrade' again to retry the rest; "+tilde(paths.UpgradeFile())+" keeps the list."))
		return errors.Join(errs...)
	}
	_ = os.Remove(paths.UpgradeFile())
	return nil
}

// waitReady gives a restarted app a moment to come up, and if it has a health
// check, to pass it, before the next one is touched. It never blocks for long.
func waitReady(c *client.Client, spec proc.Spec) string {
	limit := 5 * time.Second
	if spec.Health != "" {
		limit = 60 * time.Second
	}
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		in, err := c.Get(spec.Name)
		if err == nil && in.Status == proc.StatusOnline && (spec.Health == "" || in.Health == proc.HealthHealthy) {
			return ""
		}
		if err == nil && in.Status == proc.StatusErrored {
			return " " + yellow("(errored; see procly logs "+spec.Name+")")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return " " + yellow("(not ready after "+limit.String()+"; carrying on)")
}

func waitGone(c *client.Client, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := c.Ping(); err != nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func writeUpgradeFile(entries []proc.HandoverEntry) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := paths.UpgradeFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, paths.UpgradeFile())
}

func readUpgradeFile() ([]proc.HandoverEntry, bool) {
	data, err := os.ReadFile(paths.UpgradeFile())
	if err != nil {
		return nil, false
	}
	var entries []proc.HandoverEntry
	if json.Unmarshal(data, &entries) != nil || len(entries) == 0 {
		_ = os.Remove(paths.UpgradeFile()) // empty or damaged: nothing to finish
		return nil, false
	}
	return entries, true
}
