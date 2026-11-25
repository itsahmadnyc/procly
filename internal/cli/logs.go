package cli

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/paths"
)

type logFile struct {
	path   string
	name   string
	stderr bool
	follow *logtail.Follower
}

func cmdLogs(argv []string) error {
	a, err := parse(argv, withServer(flagSet{
		valued: set("lines"),
		bools:  set("out", "err", "no-follow"),
	}))
	if err != nil {
		return err
	}
	lines := 15
	if v := a.get("lines"); v != "" {
		if lines, err = strconv.Atoi(v); err != nil || lines < 0 {
			return errors.New("--lines must be a positive number")
		}
	}

	if server := pickServer(a); server != "" {
		return remoteLogs(server, a.pos, lines, a.bools["out"], a.bools["err"], !a.bools["no-follow"])
	}

	names, err := logNames(a.pos)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		fmt.Println(dim("  No logs yet."))
		return nil
	}

	var files []*logFile
	for _, n := range names {
		if !a.bools["err"] {
			files = append(files, &logFile{path: filepath.Join(paths.LogDir(), n+"-out.log"), name: n})
		}
		if !a.bools["out"] {
			files = append(files, &logFile{path: filepath.Join(paths.LogDir(), n+"-error.log"), name: n, stderr: true})
		}
	}
	labelWidth := 0
	for _, n := range names {
		labelWidth = max(labelWidth, width(n))
	}

	for _, f := range files {
		tail, size := logtail.Tail(f.path, lines)
		f.follow = logtail.NewFollower(f.path, size)
		if lines == 0 {
			continue
		}
		fmt.Println(dim(fmt.Sprintf("  %s  last %d %s", tilde(f.path), lines, plural(lines, "line", "lines"))))
		for _, l := range tail {
			printLogLine(f, labelWidth, l)
		}
		fmt.Println()
	}
	if a.bools["no-follow"] {
		return nil
	}

	fmt.Println(dim("  following, ctrl-c to stop"))
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return nil
		case <-tick.C:
			for _, f := range files {
				for _, l := range f.follow.Read() {
					printLogLine(f, labelWidth, l)
				}
			}
		}
	}
}

// logNames resolves the requested names (or ids) to process names. With no
// arguments it returns every process, falling back to the log directory when
// the daemon is down.
func logNames(targets []string) ([]string, error) {
	c, ok := connect()
	if len(targets) > 0 {
		var names []string
		for _, t := range targets {
			if ok {
				if in, err := c.Get(t); err == nil {
					names = append(names, in.Name)
					continue
				}
			}
			if !fileExists(filepath.Join(paths.LogDir(), t+"-out.log")) {
				return nil, fmt.Errorf("no logs for %s", t)
			}
			names = append(names, t)
		}
		return names, nil
	}
	if ok {
		infos, err := c.List()
		if err != nil {
			return nil, err
		}
		names := make([]string, len(infos))
		for i, in := range infos {
			names[i] = in.Name
		}
		return names, nil
	}
	matches, _ := filepath.Glob(filepath.Join(paths.LogDir(), "*-out.log"))
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, strings.TrimSuffix(filepath.Base(m), "-out.log"))
	}
	sort.Strings(names)
	return names, nil
}

func printLogLine(f *logFile, labelWidth int, line string) {
	printLabeled(f.name, labelWidth, f.stderr, line)
}

func printLabeled(name string, labelWidth int, stderr bool, line string) {
	label := fmt.Sprintf("%-*s |", labelWidth, name)
	if stderr {
		label = red(label)
	} else {
		label = gray(label)
	}
	fmt.Printf("  %s %s\n", label, line)
}
