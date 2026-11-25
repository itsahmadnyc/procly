package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/itsahmadnyc/procly/internal/paths"
)

type logFile struct {
	path    string
	name    string
	stderr  bool
	offset  int64
	partial []byte
}

func cmdLogs(argv []string) error {
	a, err := parse(argv, flagSet{
		valued: set("lines"),
		bools:  set("out", "err", "no-follow"),
	})
	if err != nil {
		return err
	}
	lines := 15
	if v := a.get("lines"); v != "" {
		if lines, err = strconv.Atoi(v); err != nil || lines < 0 {
			return errors.New("--lines must be a positive number")
		}
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
		tail, size := tailLines(f.path, lines)
		f.offset = size
		if lines == 0 {
			continue
		}
		fmt.Println(dim(fmt.Sprintf("  %s  last %d lines", tilde(f.path), lines)))
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
				follow(f, labelWidth)
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
	label := fmt.Sprintf("%-*s |", labelWidth, f.name)
	if f.stderr {
		label = red(label)
	} else {
		label = gray(label)
	}
	fmt.Printf("  %s %s\n", label, line)
}

// tailLines returns the last n lines of a file and its current size.
func tailLines(path string, n int) ([]string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0
	}
	size := fi.Size()
	const chunk = 256 << 10
	start := max(size-chunk, 0)
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, size
	}
	text := strings.TrimRight(string(buf), "\n")
	if text == "" {
		return nil, size
	}
	all := strings.Split(text, "\n")
	if start > 0 && len(all) > 1 {
		all = all[1:] // first line is probably cut in half
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all, size
}

func follow(f *logFile, labelWidth int) {
	fi, err := os.Stat(f.path)
	if err != nil {
		return
	}
	if fi.Size() < f.offset { // truncated or rotated
		f.offset, f.partial = 0, nil
	}
	if fi.Size() == f.offset {
		return
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return
	}
	defer fh.Close()
	buf := make([]byte, fi.Size()-f.offset)
	n, _ := fh.ReadAt(buf, f.offset)
	f.offset += int64(n)
	data := append(f.partial, buf[:n]...)
	idx := bytes.LastIndexByte(data, '\n')
	if idx < 0 {
		f.partial = data
		return
	}
	f.partial = append([]byte(nil), data[idx+1:]...)
	for _, l := range strings.Split(string(data[:idx]), "\n") {
		printLogLine(f, labelWidth, l)
	}
}
