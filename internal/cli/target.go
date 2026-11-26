package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/remote"
)

// serverFlag is accepted by every command that can act on another server.
var serverFlag = map[string]string{"S": "server"}

// pickServer returns the server chosen with --server, or PROCLY_SERVER, or
// "" for this machine.
func pickServer(a args) string {
	v := a.get("server")
	if v == "" {
		v = os.Getenv("PROCLY_SERVER")
	}
	if v == remote.LocalName {
		return ""
	}
	return v
}

// withServer adds --server/-S to a command's flags.
func withServer(fs flagSet) flagSet {
	if fs.valued == nil {
		fs.valued = map[string]bool{}
	}
	fs.valued["server"] = true
	if fs.alias == nil {
		fs.alias = map[string]string{}
	}
	for k, v := range serverFlag {
		fs.alias[k] = v
	}
	return fs
}

// remoteServer looks up a server added with `procly server add`.
func remoteServer(name string) (*remote.Client, error) {
	list, err := remote.Load()
	if err != nil {
		return nil, err
	}
	for _, s := range list {
		if s.Name == name {
			return remote.NewClient(s), nil
		}
	}
	return nil, fmt.Errorf("no server named %q, see 'procly server ls'", name)
}

// remoteErr explains agent errors in the same words as `procly server add`.
func remoteErr(server string, err error) error {
	var se *remote.StatusError
	var ne net.Error
	switch {
	case errors.Is(err, remote.ErrFingerprint):
		return fmt.Errorf("%s: certificate fingerprint doesn't match the one saved with 'procly server add'", server)
	case errors.As(err, &se) && se.Code == 401:
		return fmt.Errorf("%s: the agent rejected the token", server)
	case errors.As(err, &se):
		return fmt.Errorf("%s: %s", server, se.Message)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne):
		return fmt.Errorf("%s is unreachable: %v", server, err)
	}
	return fmt.Errorf("%s: %w", server, err)
}

func shortCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

func remoteList(c *remote.Client, server string) ([]proc.View, error) {
	ctx, cancel := shortCtx()
	defer cancel()
	views, err := c.List(ctx)
	if err != nil {
		return nil, remoteErr(server, err)
	}
	return views, nil
}

// findView matches a process by name, then by numeric id.
func findView(views []proc.View, target string) (proc.View, bool) {
	for _, v := range views {
		if v.Name == target {
			return v, true
		}
	}
	if id, err := strconv.Atoi(target); err == nil {
		for _, v := range views {
			if v.ID == id {
				return v, true
			}
		}
	}
	return proc.View{}, false
}

func remoteAction(server, action string, targets []string) error {
	c, err := remoteServer(server)
	if err != nil {
		return err
	}
	verb := map[string]string{"stop": "stop", "restart": "restart", "reload": "reload"}[action]
	if verb == "" {
		verb = "delete"
	}
	views, err := remoteList(c, server)
	if err != nil {
		return err
	}
	// The agent works on one process at a time, so expand "all" here.
	var names []string
	for _, t := range targets {
		if t == "all" {
			for _, v := range views {
				if verb == "reload" && v.ReloadSignal == "" {
					continue // "all" reloads the ones that can be reloaded
				}
				names = append(names, v.Name)
			}
			continue
		}
		v, ok := findView(views, t)
		if !ok {
			return fmt.Errorf("%s: process not found: %s", server, t)
		}
		names = append(names, v.Name)
	}

	past := map[string]string{"stop": "stopped", "restart": "restarted", "reload": "reloaded", "delete": "deleted"}[verb]
	var errs []error
	for _, name := range names {
		ctx, cancel := shortCtx()
		err := c.Action(ctx, name, verb)
		cancel()
		if err != nil {
			errs = append(errs, remoteErr(server, err))
			continue
		}
		fmt.Printf("  %s %s %s\n", green(past), bold(name), dim("on "+server))
	}
	if views, err := remoteList(c, server); err == nil {
		printList(os.Stdout, views, server)
	} else {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func remoteDescribe(server, target string, asJSON bool) error {
	c, err := remoteServer(server)
	if err != nil {
		return err
	}
	views, err := remoteList(c, server)
	if err != nil {
		return err
	}
	v, ok := findView(views, target)
	if !ok {
		return fmt.Errorf("%s: process not found: %s", server, target)
	}
	if asJSON {
		return printJSON(os.Stdout, v)
	}
	auto := "no"
	if v.AutoRestart {
		auto = "yes"
	}
	rows := [][2]string{
		{"server", server},
		{"id", fmt.Sprint(v.ID)},
		{"command", v.Command},
		{"cwd", v.Cwd},
	}
	if v.PID > 0 {
		rows = append(rows,
			[2]string{"pid", fmt.Sprint(v.PID)},
			[2]string{"uptime", fmtDuration(time.Duration(v.UptimeMs) * time.Millisecond)},
			[2]string{"cpu", fmt.Sprintf("%.1f%%", v.CPU)},
			[2]string{"memory", fmtBytes(v.Memory)},
		)
	} else {
		rows = append(rows, [2]string{"last exit", fmt.Sprint(v.ExitCode)})
	}
	rows = append(rows, [2]string{"restarts", fmt.Sprint(v.Restarts)}, [2]string{"autorestart", auto})
	if v.StopSignal != "" {
		rows = append(rows, [2]string{"stop signal", v.StopSignal + ", then SIGKILL after the kill timeout"})
	}
	if v.ReloadSignal != "" {
		rows = append(rows, [2]string{"reload signal", v.ReloadSignal + "  (procly reload " + v.Name + " -S " + server + ")"})
	}
	if v.MaxMemory > 0 {
		rows = append(rows, [2]string{"max memory", fmtBytes(v.MaxMemory) + "  (restarts it above this, with its children)"})
	}
	if len(v.EnvKeys) > 0 {
		// Agents send variable names only; values stay on that server.
		rows = append(rows, [2]string{"env", strings.Join(v.EnvKeys, ", ")})
	}
	fmt.Printf("\n  %s  %s\n\n", bold(v.Name), statusColor(v.Status)(statusLabel(v.Status)))
	for _, r := range rows {
		fmt.Printf("  %s %s\n", dim(fmt.Sprintf("%-12s", r[0])), r[1])
	}
	fmt.Println()
	return nil
}

func remoteSave(server string) error {
	c, err := remoteServer(server)
	if err != nil {
		return err
	}
	ctx, cancel := shortCtx()
	defer cancel()
	n, err := c.Save(ctx)
	if err != nil {
		return remoteErr(server, err)
	}
	fmt.Printf("  %s %d %s on %s\n", green("saved"), n, plural(n, "process", "processes"), bold(server))
	return nil
}

// remoteLogs streams logs of the given processes (all when none) from an
// agent. History matches local logs: the last n lines of stdout and of stderr.
func remoteLogs(server string, targets []string, lines int, onlyOut, onlyErr, follow bool) error {
	c, err := remoteServer(server)
	if err != nil {
		return err
	}
	views, err := remoteList(c, server)
	if err != nil {
		return err
	}
	var names []string
	if len(targets) == 0 {
		for _, v := range views {
			names = append(names, v.Name)
		}
	}
	for _, t := range targets {
		v, ok := findView(views, t)
		if !ok {
			return fmt.Errorf("%s: process not found: %s", server, t)
		}
		names = append(names, v.Name)
	}
	if len(names) == 0 {
		fmt.Println(dim("  No processes on " + server + "."))
		return nil
	}
	labelWidth := 0
	for _, n := range names {
		labelWidth = max(labelWidth, width(n))
	}
	keep := func(e logtail.Event) bool {
		return !(onlyOut && e.Stream == "e") && !(onlyErr && e.Stream == "o")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	defer signal.Stop(stop)
	go func() {
		<-stop
		cancel()
	}()

	var mu sync.Mutex // one process's lines at a time
	var wg sync.WaitGroup
	errs := make([]error, len(names))
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first := true
			err := c.Logs(ctx, name, func(batch []logtail.Event) bool {
				if batch == nil {
					return true // keepalive
				}
				mu.Lock()
				defer mu.Unlock()
				if first {
					first = false
					var hist []logtail.Event
					for _, e := range lastPerStream(batch, lines) {
						if keep(e) {
							hist = append(hist, e)
						}
					}
					if len(hist) > 0 {
						fmt.Println(dim(fmt.Sprintf("  %s/%s  last %d %s", server, name, lines, plural(lines, "line", "lines"))))
						for _, e := range hist {
							printLabeled(name, labelWidth, e.Stream == "e", e.Line)
						}
						fmt.Println()
					}
					return follow
				}
				for _, e := range batch {
					if keep(e) {
						printLabeled(name, labelWidth, e.Stream == "e", e.Line)
					}
				}
				return true
			})
			if err != nil {
				errs[i] = remoteErr(server, err)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// lastPerStream keeps the last n stdout lines and the last n stderr lines,
// stdout first, the same as `procly logs` does for local files.
func lastPerStream(batch []logtail.Event, n int) []logtail.Event {
	var out, errs []logtail.Event
	for _, e := range batch {
		if e.Stream == "e" {
			errs = append(errs, e)
		} else {
			out = append(out, e)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	if len(errs) > n {
		errs = errs[len(errs)-n:]
	}
	return append(out, errs...)
}
