// Package cli implements the procly command line.
package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/itsahmadnyc/procly/internal/client"
	"github.com/itsahmadnyc/procly/internal/config"
	"github.com/itsahmadnyc/procly/internal/daemon"
	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/tui"
	"github.com/itsahmadnyc/procly/internal/version"
	"github.com/itsahmadnyc/procly/internal/web"
)

const usage = `procly %s, a process manager for your servers

Usage
  procly <command> [arguments]

Processes
  start <file|command|config>   Start a process, or every app in a config file
  stop <name|id|all>            Stop processes
  restart <name|id|all>         Restart processes
  delete <name|id|all>          Stop and forget processes          alias: rm
  list                          Show all processes                 alias: ls
  describe <name|id>            Details for one process            alias: show
  logs [name|id]                Print and follow logs
  monit                         Live dashboard in the terminal     alias: dash
  web                           Web panel (--addr, --port, --reset-password)

State
  save                          Remember the current process list
  resurrect                     Start everything from the last save
  startup                       Start procly on boot (systemd / launchd)
  kill                          Stop every process and the daemon

Start options
  -n, --name <name>             Process name (default: from file or command)
      --cwd <dir>               Working directory (default: current)
  -e, --env KEY=VALUE           Environment variable, repeatable
      --no-autorestart          Leave the process down when it exits
      --max-restarts <n>        Give up after n quick crashes (default 15)
  -- <args>                     Everything after -- goes to the script

Logs options
      --lines <n>               Lines of history to print (default 15)
      --out, --err              Only stdout or only stderr
      --no-follow               Print and exit

Examples
  procly start server.js --name api -e PORT=3000
  procly start "npm run start" --name web
  procly start procly.yml
  procly logs api --lines 100
`

// Run executes one CLI invocation and returns the exit code.
func Run(argv []string) int {
	if len(argv) == 0 {
		fmt.Printf(usage, version.Version)
		return 0
	}
	cmd, rest := argv[0], argv[1:]
	var err error
	switch cmd {
	case "start":
		err = cmdStart(rest)
	case "stop", "restart", "delete", "rm", "del":
		err = cmdAction(cmd, rest)
	case "list", "ls", "l", "status":
		err = cmdList()
	case "describe", "show", "info":
		err = cmdDescribe(rest)
	case "logs", "log":
		err = cmdLogs(rest)
	case "monit", "dash", "top":
		err = cmdMonit()
	case "web", "panel":
		err = cmdWeb(rest)
	case "save":
		err = cmdSave()
	case "resurrect":
		err = cmdResurrect()
	case "startup":
		err = cmdStartup()
	case "kill":
		err = cmdKill()
	case "daemon":
		err = cmdDaemon(rest)
	case "version", "-v", "--version":
		fmt.Println("procly", version.Version)
	case "help", "-h", "--help":
		fmt.Printf(usage, version.Version)
	default:
		err = fmt.Errorf("unknown command %q, run 'procly help'", cmd)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", red("error"), err)
		return 1
	}
	return 0
}

// ensureDaemon connects to the daemon, starting it in the background if needed.
func ensureDaemon() (*client.Client, error) {
	c := client.New()
	if _, err := c.Ping(); err == nil {
		return c, nil
	}
	if err := paths.Ensure(); err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(paths.DaemonLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "daemon")
	cmd.Dir = "/"
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}
	_ = cmd.Process.Release()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := c.Ping(); err == nil {
			fmt.Fprintln(os.Stderr, dim("  daemon started, logs in "+tilde(paths.DaemonLog())))
			return c, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("daemon did not come up, see %s", tilde(paths.DaemonLog()))
}

// connect returns a client only if the daemon is already running.
func connect() (*client.Client, bool) {
	c := client.New()
	_, err := c.Ping()
	return c, err == nil
}

func cmdStart(argv []string) error {
	a, err := parse(argv, flagSet{
		valued: set("name", "cwd", "env", "max-restarts"),
		bools:  set("no-autorestart"),
		alias:  map[string]string{"n": "name", "e": "env"},
	})
	if err != nil {
		return err
	}
	if len(a.pos) == 0 {
		return errors.New("usage: procly start <file|command|config> [options]")
	}
	c, err := ensureDaemon()
	if err != nil {
		return err
	}

	var specs []proc.Spec
	if len(a.pos) == 1 && config.IsConfig(a.pos[0]) {
		if specs, err = config.Load(a.pos[0]); err != nil {
			return err
		}
	} else {
		// `procly start api` brings back a stopped process by name.
		if len(a.pos) == 1 && len(a.rest) == 0 && len(a.values) == 0 && !fileExists(a.pos[0]) {
			if info, err := c.Get(a.pos[0]); err == nil {
				if info.Status == proc.StatusOnline || info.Status == proc.StatusWaiting {
					return fmt.Errorf("%s is already running, use 'procly restart %s'", info.Name, info.Name)
				}
				if _, err := c.Restart(info.Name); err != nil {
					return err
				}
				fmt.Printf("  %s %s\n", green("started"), bold(info.Name))
				return showList(c)
			}
		}
		spec, err := buildSpec(a)
		if err != nil {
			return err
		}
		specs = []proc.Spec{spec}
	}

	var errs []error
	for _, s := range specs {
		inheritPath(&s)
		info, err := c.Start(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		fmt.Printf("  %s %s %s\n", green("started"), bold(info.Name), dim(info.Spec.Command))
	}
	// Give a process that dies immediately a moment to show it in the table.
	time.Sleep(300 * time.Millisecond)
	if err := showList(c); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

var interpreters = map[string]string{
	".js": "node", ".mjs": "node", ".cjs": "node",
	".ts": "npx tsx", ".py": "python3", ".rb": "ruby",
	".php": "php", ".sh": "bash", ".pl": "perl",
}

var runnerTools = set("npm", "pnpm", "yarn", "bun", "deno", "npx")

func buildSpec(a args) (proc.Spec, error) {
	cwd := a.get("cwd")
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return proc.Spec{}, err
	}

	var command, name string
	first := a.pos[0]
	target := first
	if !filepath.IsAbs(target) {
		target = filepath.Join(cwd, first)
	}
	if fi, err := os.Stat(target); err == nil && !fi.IsDir() && len(a.pos) == 1 {
		ext := strings.ToLower(filepath.Ext(first))
		name = strings.TrimSuffix(filepath.Base(first), filepath.Ext(first))
		switch interp, ok := interpreters[ext]; {
		case ok:
			command = interp + " " + shellQuote(first)
		case fi.Mode()&0o111 != 0:
			if !strings.Contains(first, "/") {
				first = "./" + first
			}
			command = shellQuote(first)
		default:
			return proc.Spec{}, fmt.Errorf("don't know how to run %s, pass the full command instead, e.g. procly start \"python3 %s\"", first, first)
		}
	} else {
		command = strings.Join(a.pos, " ")
		fields := strings.Fields(command)
		name = filepath.Base(fields[0])
		// "npm run start" reads better as "start" than "npm".
		if runnerTools[name] && len(fields) > 1 {
			name = fields[len(fields)-1]
		}
	}
	for _, r := range a.rest {
		command += " " + shellQuote(r)
	}
	if n := a.get("name"); n != "" {
		name = n
	} else {
		name = sanitizeName(name)
	}

	spec := proc.Spec{Name: name, Command: command, Cwd: cwd}
	for _, kv := range a.values["env"] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return spec, fmt.Errorf("invalid --env %q, expected KEY=VALUE", kv)
		}
		if spec.Env == nil {
			spec.Env = map[string]string{}
		}
		spec.Env[k] = v
	}
	if a.bools["no-autorestart"] {
		off := false
		spec.AutoRestart = &off
	}
	if v := a.get("max-restarts"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return spec, fmt.Errorf("--max-restarts must be a positive number")
		}
		spec.MaxRestarts = n
	}
	return spec, nil
}

// inheritPath records the caller's PATH so the daemon finds the same
// node/python/etc. the user just ran from their shell (nvm, pyenv...).
func inheritPath(s *proc.Spec) {
	if _, ok := s.Env["PATH"]; ok {
		return
	}
	if s.Env == nil {
		s.Env = map[string]string{}
	}
	s.Env["PATH"] = os.Getenv("PATH")
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeName(n string) string {
	n = strings.Trim(unsafeName.ReplaceAllString(n, "-"), "-._")
	if n == "" || n == "all" {
		return "app"
	}
	if len(n) > 64 {
		n = n[:64]
	}
	return n
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9@%+=:,./_-]+$`)

func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func cmdAction(action string, argv []string) error {
	a, err := parse(argv, flagSet{})
	if err != nil {
		return err
	}
	if len(a.pos) == 0 {
		return fmt.Errorf("usage: procly %s <name|id|all>", action)
	}
	c, ok := connect()
	if !ok {
		return errors.New("daemon is not running, nothing to " + action)
	}
	var errs []error
	for _, target := range a.pos {
		var infos []proc.Info
		var verb string
		switch action {
		case "stop":
			infos, err = c.Stop(target)
			verb = "stopped"
		case "restart":
			infos, err = c.Restart(target)
			verb = "restarted"
		default:
			infos, err = c.Delete(target)
			verb = "deleted"
		}
		if err != nil {
			errs = append(errs, err)
		}
		for _, in := range infos {
			fmt.Printf("  %s %s\n", green(verb), bold(in.Name))
		}
	}
	if err := showList(c); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func showList(c *client.Client) error {
	infos, err := c.List()
	if err != nil {
		return err
	}
	printList(os.Stdout, infos)
	return nil
}

func cmdList() error {
	c, ok := connect()
	if !ok {
		fmt.Println(dim("\n  Daemon is not running. Start a process with: procly start app.js\n"))
		return nil
	}
	return showList(c)
}

func cmdDescribe(argv []string) error {
	if len(argv) != 1 {
		return errors.New("usage: procly describe <name|id>")
	}
	c, ok := connect()
	if !ok {
		return errors.New("daemon is not running")
	}
	in, err := c.Get(argv[0])
	if err != nil {
		return err
	}
	auto := "no"
	if in.Spec.ShouldAutoRestart() {
		auto = fmt.Sprintf("yes, up to %d quick crashes", in.Spec.MaxRestartsOrDefault())
	}
	rows := [][2]string{
		{"id", fmt.Sprint(in.ID)},
		{"command", in.Spec.Command},
		{"cwd", tilde(in.Spec.Cwd)},
	}
	if in.PID > 0 {
		rows = append(rows,
			[2]string{"pid", fmt.Sprint(in.PID)},
			[2]string{"uptime", fmtDuration(time.Since(in.StartedAt))},
			[2]string{"cpu", fmt.Sprintf("%.1f%%", in.CPU)},
			[2]string{"memory", fmtBytes(in.Memory)},
		)
	} else {
		rows = append(rows, [2]string{"last exit", fmt.Sprint(in.ExitCode)})
	}
	rows = append(rows,
		[2]string{"restarts", fmt.Sprint(in.Restarts)},
		[2]string{"autorestart", auto},
		[2]string{"stdout", tilde(in.OutLog)},
		[2]string{"stderr", tilde(in.ErrLog)},
	)
	keys := make([]string, 0, len(in.Spec.Env))
	for k := range in.Spec.Env {
		if k != "PATH" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for i, k := range keys {
		label := ""
		if i == 0 {
			label = "env"
		}
		rows = append(rows, [2]string{label, k + "=" + in.Spec.Env[k]})
	}

	fmt.Printf("\n  %s  %s\n\n", bold(in.Name), statusColor(in.Status)(statusLabel(in.Status)))
	for _, r := range rows {
		fmt.Printf("  %s %s\n", dim(fmt.Sprintf("%-12s", r[0])), r[1])
	}
	fmt.Println()
	return nil
}

func cmdSave() error {
	c, ok := connect()
	if !ok {
		return errors.New("daemon is not running, nothing to save")
	}
	n, path, err := c.Save()
	if err != nil {
		return err
	}
	fmt.Printf("  %s %d %s to %s\n", green("saved"), n, plural(n, "process", "processes"), tilde(path))
	return nil
}

func cmdResurrect() error {
	c, err := ensureDaemon()
	if err != nil {
		return err
	}
	infos, rerr := c.Resurrect()
	fmt.Printf("  %s %d %s\n", green("resurrected"), len(infos), plural(len(infos), "process", "processes"))
	if err := showList(c); err != nil {
		return errors.Join(rerr, err)
	}
	return rerr
}

func cmdKill() error {
	c, ok := connect()
	if !ok {
		fmt.Println(dim("  daemon is not running"))
		return nil
	}
	infos, err := c.Kill()
	if err != nil {
		return err
	}
	fmt.Printf("  %s %d %s and the daemon\n", green("stopped"), len(infos), plural(len(infos), "process", "processes"))
	return nil
}

func cmdMonit() error {
	fi, err := os.Stdout.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return errors.New("monit needs an interactive terminal, use 'procly ls' in scripts")
	}
	c, ok := connect()
	if !ok {
		return errors.New("daemon is not running, start a process first: procly start app.js")
	}
	return tui.Run(c)
}

func cmdWeb(argv []string) error {
	a, err := parse(argv, flagSet{
		valued: set("addr", "port"),
		bools:  set("reset-password"),
	})
	if err != nil {
		return err
	}
	addr := web.DefaultAddr
	if v := a.get("addr"); v != "" {
		addr = v
	}
	if v := a.get("port"); v != "" {
		if a.get("addr") != "" {
			return errors.New("use either --addr or --port, not both")
		}
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
			return errors.New("--port must be between 1 and 65535")
		}
		addr = "127.0.0.1:" + v
	}
	// The panel is useful only with a daemon behind it, so make sure one runs.
	if _, err := ensureDaemon(); err != nil {
		return err
	}
	return web.Run(web.Options{Addr: addr, ResetPassword: a.bools["reset-password"], Out: os.Stdout})
}

func cmdDaemon(argv []string) error {
	a, err := parse(argv, flagSet{bools: set("resurrect")})
	if err != nil {
		return err
	}
	return daemon.Run(a.bools["resurrect"])
}
