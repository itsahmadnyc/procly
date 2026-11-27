package proc

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Warning is something about an app that matters when it runs as several
// workers or copies. procly cannot know what an app does, so these come from
// reading its source for the patterns that cause trouble: they are hints, never
// a refusal.
type Warning struct {
	Kind   string `json:"kind"`           // startup, jobs, realtime, sessions, port
	Text   string `json:"text"`           // one sentence for a person
	File   string `json:"file,omitempty"` // where it was seen
	Advice string `json:"advice,omitempty"`
}

type pattern struct {
	kind   string
	re     *regexp.Regexp
	text   string
	advice string
}

var preflightPatterns = []pattern{
	{"startup", regexp.MustCompile(`(?i)\b(sequelize|db|database)\w*\.sync\s*\(|\.sync\s*\(\s*\{[^}]*(alter|force)|synchronize\s*:\s*true|syncIndexes\s*\(`),
		"It syncs the database schema when it starts, so every worker does it at boot.",
		"Run it only in worker 0: if (process.env.NODE_APP_INSTANCE === '0') { ... }, or move it to a one-off migration step."},
	{"startup", regexp.MustCompile(`(?i)migrate\.latest|umzug|\.migrate\s*\(|prisma\s+migrate|runMigrations\s*\(|migrations?Run`),
		"It runs migrations when it starts, so every worker runs them at boot.",
		"Run them in worker 0 only (NODE_APP_INSTANCE === '0') or before the app starts."},
	{"jobs", regexp.MustCompile(`(?i)node-cron|cron\.schedule|new\s+CronJob|node-schedule|schedule\.scheduleJob|require\(['"]agenda['"]\)|setInterval\s*\(\s*[^)]*(sync|cleanup|sendEmail|poll)`),
		"It schedules jobs inside the app, so every worker runs each job.",
		"Run jobs in worker 0 only (NODE_APP_INSTANCE === '0'), or in a separate fork process."},
	{"realtime", regexp.MustCompile(`(?i)socket\.io|require\(['"]ws['"]\)|from\s+['"]ws['"]|new\s+WebSocket\.Server|new\s+WebSocketServer|@nestjs/websockets`),
		"It uses websockets. Workers take connections in turn, so a client's polling requests can reach different workers.",
		"Use a shared adapter (socket.io with the Redis adapter) or keep realtime in one fork process."},
	{"sessions", regexp.MustCompile(`(?i)new\s+session\.MemoryStore|MemoryStore|express-session`),
		"It may keep sessions in memory, which each worker has separately: a user can look signed out on the next request.",
		"Use a shared store (Redis, a database) for sessions."},
}

var skipDirs = map[string]bool{"node_modules": true, ".git": true, "dist": true, "build": true, "coverage": true, ".next": true, "test": true, "tests": true, "__tests__": true}

// Preflight reads the app's source for what goes wrong when it runs more than
// once. mode "cluster" looks at all of it; "fork" with copies looks at the
// fixed port. It reads at most a few hundred files, a few hundred KB each.
func Preflight(s Spec, mode string, copies int) []Warning {
	var out []Warning
	seen := map[string]bool{}
	add := func(w Warning) {
		k := w.Kind + "|" + w.Text
		if !seen[k] {
			seen[k] = true
			out = append(out, w)
		}
	}
	if mode == ExecFork && copies > 1 {
		if v, ok := s.Env["PORT"]; ok && strings.TrimSpace(v) != "" && s.IncrementVar == "" {
			add(Warning{Kind: "port", Text: fmt.Sprintf("%d copies would all listen on PORT=%s; all but one would fail.", copies, v),
				Advice: "Use increment_var: PORT so each copy gets its own port, or use cluster mode."})
		}
	}
	if mode != ExecCluster && !(mode == ExecFork && copies > 1) {
		return out
	}
	root := s.Cwd
	if root == "" {
		return out
	}
	scanSource(root, func(path, rel, text string) {
		isPkg := filepath.Base(path) == "package.json"
		for _, p := range preflightPatterns {
			if mode == ExecFork && p.kind != "port" {
				continue
			}
			if isPkg && p.kind != "realtime" && p.kind != "sessions" && p.kind != "jobs" {
				continue
			}
			if loc := p.re.FindString(text); loc != "" {
				// a session library with a shared store is fine
				if p.kind == "sessions" && regexp.MustCompile(`(?i)connect-redis|connect-mongo|connect-pg-simple|session-file-store|RedisStore|MongoStore`).MatchString(text) {
					continue
				}
				add(Warning{Kind: p.kind, Text: p.text, File: rel, Advice: p.advice})
			}
		}
	})
	sort.SliceStable(out, func(i, j int) bool { return warnRank(out[i].Kind) < warnRank(out[j].Kind) })
	return out
}

func warnRank(k string) int {
	switch k {
	case "startup":
		return 0
	case "port":
		return 1
	case "jobs":
		return 2
	case "realtime":
		return 3
	}
	return 4
}

// PreflightMode is Preflight for a running app, by name: what to know before
// switching it to a mode.
func (m *Manager) PreflightMode(target, mode string, workers int) ([]Warning, error) {
	ps, err := m.resolve(target)
	if err != nil {
		return nil, err
	}
	p := ps[0]
	p.mu.Lock()
	s := p.spec
	p.mu.Unlock()
	if mode != ExecFork && mode != ExecCluster {
		return nil, fmt.Errorf("mode must be %q or %q", ExecFork, ExecCluster)
	}
	return Preflight(s, mode, workers), nil
}

// scanSource calls fn with each source file of an app (not node_modules, not
// tests), up to a few hundred small ones.
func scanSource(root string, fn func(path, rel, text string)) {
	files := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".js", ".mjs", ".cjs", ".ts", ".json":
		default:
			return nil
		}
		if files++; files > 400 {
			return filepath.SkipAll
		}
		if fi, err := d.Info(); err != nil || fi.Size() > 400<<10 {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		fn(path, rel, string(b))
		return nil
	})
}

var instanceAware = regexp.MustCompile(`PROCLY_INSTANCE|NODE_APP_INSTANCE|pm_id`)

// knowsItsInstance reports whether the app's source reads the variable that
// tells each copy which one it is: such an app picks its own port (or does
// its own splitting), so separate copies are not a mistake.
func knowsItsInstance(root string) bool {
	found := false
	if root == "" {
		return false
	}
	scanSource(root, func(path, rel, text string) {
		if !found && filepath.Base(path) != "package.json" && instanceAware.MatchString(text) {
			found = true
		}
	})
	return found
}
