package proc

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// The two ways an app can run. In fork mode (the default) it is one process,
// or, with instances, several separate ones. In cluster mode it is one
// process, procly's cluster launcher, whose children are workers running the
// app's Node script and sharing the ports it listens on, as in PM2's cluster
// mode.
const (
	ExecFork    = "fork"
	ExecCluster = "cluster"
)

//go:embed cluster_launcher.js
var launcherJS []byte

func (s Spec) isCluster() bool { return s.ExecMode == ExecCluster }

// Workers is how many workers a cluster-mode app runs: its instances, or one
// per CPU core when none is given.
func (s Spec) Workers() int {
	if s.Instances > 0 {
		return s.Instances
	}
	return min(max(runtime.NumCPU(), 1), MaxInstances)
}

type clusterCmd struct {
	node     string   // the node binary, as written ("node", "/usr/bin/node")
	execArgv []string // node flags for the workers (--max-old-space-size=512, -r x)
	script   string
	args     []string
}

var needsValue = map[string]bool{"-r": true, "--require": true, "--import": true, "--loader": true, "--experimental-loader": true, "--env-file": true}

// parseClusterCommand reads "node [flags] script [args]" (or a bare script.js).
// Cluster mode runs a Node script; "npm run start" and the like start another
// program, which cannot be put into a cluster.
func parseClusterCommand(cmd string) (clusterCmd, error) {
	words, err := splitWords(cmd)
	if err != nil {
		return clusterCmd{}, err
	}
	if len(words) == 0 {
		return clusterCmd{}, errors.New("command is required")
	}
	if assignmentRe.MatchString(words[0]) {
		return clusterCmd{}, errors.New("cluster mode: put environment variables in env, not in front of the command")
	}
	cc := clusterCmd{node: "node"}
	i := 0
	switch base := filepath.Base(words[0]); {
	case base == "node" || base == "nodejs":
		cc.node = words[0]
		i = 1
		for i < len(words) && strings.HasPrefix(words[i], "-") {
			f := words[i]
			switch f {
			case "-e", "--eval", "-p", "--print", "-":
				return clusterCmd{}, fmt.Errorf("cluster mode needs a script file, not %s", f)
			}
			cc.execArgv = append(cc.execArgv, f)
			i++
			if needsValue[f] && i < len(words) {
				cc.execArgv = append(cc.execArgv, words[i])
				i++
			}
		}
	case strings.HasSuffix(base, ".js") || strings.HasSuffix(base, ".mjs") || strings.HasSuffix(base, ".cjs"):
		// a script written alone: run it with node
	default:
		return clusterCmd{}, fmt.Errorf("cluster mode runs a Node script: use a command like \"node server.js\" (%q starts another program, which procly cannot put into a cluster)", words[0])
	}
	if i >= len(words) {
		return clusterCmd{}, errors.New("cluster mode needs a script to run: \"node server.js\"")
	}
	cc.script, cc.args = words[i], append([]string(nil), words[i+1:]...)
	return cc, nil
}

// splitWords splits a command into words, honoring single and double quotes. A
// shell operator outside quotes is an error: cluster mode needs a plain command.
func splitWords(s string) ([]string, error) {
	var out []string
	var b strings.Builder
	var q byte
	has := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else if c == '\\' && q == '"' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			} else {
				b.WriteByte(c)
			}
		case c == '\'' || c == '"':
			q, has = c, true
		case c == ' ' || c == '\t':
			if has || b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
				has = false
			}
		case strings.IndexByte(";&|<>()`\n$", c) >= 0:
			return nil, errors.New("cluster mode needs a plain command (\"node server.js\"), without pipes, && or $variables")
		default:
			b.WriteByte(c)
		}
	}
	if q != 0 {
		return nil, errors.New("unclosed quote in the command")
	}
	if has || b.Len() > 0 {
		out = append(out, b.String())
	}
	return out, nil
}

var safeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	if safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ensureLauncher writes the launcher script into dir (named after its
// content, so a newer procly writes a newer file) and returns its path.
func ensureLauncher(dir string) (string, error) {
	sum := sha256.Sum256(launcherJS)
	path := filepath.Join(dir, ".cluster-launcher-"+hex.EncodeToString(sum[:4])+".js")
	if fi, err := os.Stat(path); err == nil && fi.Size() == int64(len(launcherJS)) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, launcherJS, 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}

// launchCommand is what sh runs for the spec: the command as written, or in
// cluster mode the launcher with the script.
func launchCommand(s Spec, dir string) (string, error) {
	if !s.isCluster() {
		return shellCommand(s.Command), nil
	}
	cc, err := parseClusterCommand(s.Command)
	if err != nil {
		return "", err
	}
	path, err := ensureLauncher(dir)
	if err != nil {
		return "", fmt.Errorf("writing the cluster launcher: %w", err)
	}
	parts := []string{"exec", shellQuote(cc.node), shellQuote(path), "--workers", fmt.Sprint(s.Workers()),
		"--status", shellQuote(clusterStatusPath(dir, s.Name)), "--ctl", shellQuote(clusterCtlPath(dir, s.Name))}
	// max_memory is per worker in cluster mode (as in PM2): the launcher enforces it.
	if n, _ := s.maxMemoryBytes(); n > 0 {
		parts = append(parts, "--max-worker-memory", fmt.Sprint(n))
	}
	if s.ListenTimeoutMs > 0 {
		parts = append(parts, "--listen-timeout", fmt.Sprint(s.ListenTimeoutMs))
	}
	for _, f := range cc.execArgv {
		parts = append(parts, "--exec-argv", shellQuote(f))
	}
	parts = append(parts, "--", shellQuote(cc.script))
	for _, a := range cc.args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " "), nil
}
