package domains

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The Caddy reader understands the Caddyfile: site blocks (addresses, then a
// block in braces) and the reverse_proxy lines inside them. Named snippets and
// the global options block are skipped.

func readCaddy(files []string) (Source, []Site) {
	src := Source{Name: "caddy"}
	var sites []Site
	for _, f := range files {
		lines, err := caddyLines(f, 0)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) && src.Error == "" {
				src.Error = shortErr(err)
			}
			continue
		}
		src.Found = true
		sites = append(sites, caddySites(lines)...)
	}
	return src, sites
}

type caddyLine struct {
	file string
	text string
}

// caddyLines reads a Caddyfile with its import lines replaced by the files
// they name; "import snippet-name" is left out.
func caddyLines(path string, depth int) ([]caddyLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxConfBytes))
	if err != nil {
		return nil, err
	}
	var out []caddyLine
	for _, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(stripHashComment(raw))
		if line == "" {
			continue
		}
		if fields := strings.Fields(line); fields[0] == "import" && len(fields) > 1 {
			pat := fields[1]
			if !filepath.IsAbs(pat) {
				pat = filepath.Join(filepath.Dir(path), pat)
			}
			if depth < maxIncludeDepth && strings.ContainsAny(fields[1], "/*") {
				matches, _ := filepath.Glob(pat)
				for _, m := range matches {
					if sub, err := caddyLines(m, depth+1); err == nil {
						out = append(out, sub...)
					}
				}
			}
			continue
		}
		out = append(out, caddyLine{path, line})
	}
	return out, nil
}

// stripHashComment cuts a "#" comment that starts a word, outside quotes.
func stripHashComment(s string) string {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '`':
			q = c
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return s[:i]
		}
	}
	return s
}

type caddyScope struct {
	depth int    // brace depth at which this scope was opened
	path  string // the path a handle block applies to
}

func caddySites(lines []caddyLine) []Site {
	var sites []Site
	depth := 0
	var cur *Site
	var skipUntil = -1 // depth to return to when a skipped block ends
	var scopes []caddyScope
	inProxy := -1 // depth of an open reverse_proxy { } block
	flush := func() {
		if cur != nil {
			sites = append(sites, *cur)
			cur = nil
		}
	}
	for _, ln := range lines {
		text := ln.text
		opens := strings.HasSuffix(text, "{")
		closes := text == "}"
		head := strings.TrimSpace(strings.TrimSuffix(text, "{"))
		switch {
		case closes:
			depth--
			for len(scopes) > 0 && scopes[len(scopes)-1].depth > depth {
				scopes = scopes[:len(scopes)-1]
			}
			if inProxy > depth {
				inProxy = -1
			}
			if skipUntil >= 0 && depth <= skipUntil {
				skipUntil = -1
			}
			if depth == 0 {
				flush()
			}
		case skipUntil >= 0:
			if opens {
				depth++
			}
		case depth == 0 && opens:
			depth++
			if head == "" || strings.HasPrefix(head, "(") { // global options, a snippet
				skipUntil = 0
				continue
			}
			cur = caddySite(head, ln.file)
		case cur != nil && depth >= 1:
			fields := strings.Fields(head)
			if len(fields) == 0 {
				continue
			}
			path := "/"
			if len(scopes) > 0 {
				path = scopes[len(scopes)-1].path
			}
			switch fields[0] {
			case "reverse_proxy":
				args := fields[1:]
				if len(args) > 0 && (strings.HasPrefix(args[0], "/") || strings.HasPrefix(args[0], "*")) {
					path, args = args[0], args[1:]
				}
				for _, a := range args {
					cur.addUpstream(a, path)
				}
				if opens {
					inProxy = depth
				}
			case "to":
				if inProxy >= 0 {
					for _, a := range fields[1:] {
						cur.addUpstream(a, path)
					}
				}
			case "handle", "handle_path", "route":
				p := path
				if len(fields) > 1 && strings.HasPrefix(fields[1], "/") {
					p = fields[1]
				}
				if opens {
					scopes = append(scopes, caddyScope{depth + 1, p})
				}
			}
			if opens {
				depth++
			}
		}
	}
	flush()
	return sites
}

// caddySite reads the addresses on a site block's first line.
func caddySite(head, file string) *Site {
	site := &Site{Source: "caddy", File: file}
	// a Caddy site is https unless it says http:// or is on port 80
	httpOnly := true
	for _, a := range strings.FieldsFunc(head, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		https := true
		if i := strings.Index(a, "://"); i >= 0 {
			https = strings.EqualFold(a[:i], "https")
			a = a[i+3:]
		}
		if i := strings.LastIndex(a, ":"); i >= 0 {
			if a[i+1:] == "80" {
				https = false
			}
			a = a[:i]
		}
		if a == "" { // only a port: served over plain http, there is no name to get a certificate for
			https = false
			a = "*"
		}
		if https {
			httpOnly = false
		}
		site.Hosts = appendUnique(site.Hosts, a)
	}
	site.TLS = !httpOnly
	return site
}

func (s *Site) addUpstream(raw, path string) {
	host, port, ok := parseUpstream(raw)
	if !ok {
		return
	}
	up := Upstream{Host: host, Port: port, Path: path}
	for _, e := range s.Upstreams {
		if e == up {
			return
		}
	}
	s.Upstreams = append(s.Upstreams, up)
}
