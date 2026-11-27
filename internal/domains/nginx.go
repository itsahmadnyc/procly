package domains

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The nginx reader does not run nginx. It reads nginx.conf and what it
// includes, with a small parser for nginx's own syntax (directives ending in
// ";", blocks in braces, "#" comments, quoted words), and keeps the parts
// that say which names go to which upstream.

const (
	maxConfFiles    = 400
	maxConfBytes    = 4 << 20
	maxIncludeDepth = 10
)

type directive struct {
	name  string
	args  []string
	block []directive
	file  string
}

func readNginx(conf string) (Source, []Site) {
	src := Source{Name: "nginx"}
	if conf == "" {
		return src, nil
	}
	l := &loader{prefix: filepath.Dir(conf)}
	top, err := l.parseFile(conf, 0)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			src.Error = shortErr(err)
		}
		return src, nil
	}
	src.Found = true
	if l.err != "" {
		src.Error = l.err
	}
	return src, nginxSites(top)
}

func shortErr(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Op + " " + pe.Path + ": " + pe.Err.Error()
	}
	return err.Error()
}

type loader struct {
	prefix string
	files  int
	err    string // the first file that could not be read, when some could not
}

// parseFile reads one file and everything it includes.
func (l *loader) parseFile(path string, depth int) ([]directive, error) {
	if l.files++; l.files > maxConfFiles {
		return nil, fmt.Errorf("more than %d configuration files", maxConfFiles)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, maxConfBytes+1))
	if err != nil {
		return nil, err
	}
	if len(buf) > maxConfBytes {
		return nil, fmt.Errorf("%s is larger than %d MB", path, maxConfBytes>>20)
	}
	toks := lexNginx(string(buf))
	ds, _ := parseDirectives(toks, 0, path)
	return l.expand(ds, depth), nil
}

// expand replaces every include with the directives of the files it names.
func (l *loader) expand(ds []directive, depth int) []directive {
	var out []directive
	for _, d := range ds {
		if d.name == "include" && len(d.args) == 1 && depth < maxIncludeDepth {
			pat := d.args[0]
			if !filepath.IsAbs(pat) {
				pat = filepath.Join(l.prefix, pat)
			}
			matches, _ := filepath.Glob(pat)
			for _, m := range matches {
				sub, err := l.parseFile(m, depth+1)
				if err != nil {
					if l.err == "" && !errors.Is(err, fs.ErrNotExist) {
						l.err = shortErr(err)
					}
					continue
				}
				out = append(out, sub...)
			}
			continue
		}
		if d.block != nil {
			d.block = l.expand(d.block, depth)
		}
		out = append(out, d)
	}
	return out
}

type token struct {
	s    string
	punc bool // one of { } ;
}

func lexNginx(src string) []token {
	var out []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '#':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '{' || c == '}' || c == ';':
			out = append(out, token{string(c), true})
			i++
		case c == '"' || c == '\'':
			q := c
			i++
			var b strings.Builder
			for i < len(src) && src[i] != q {
				if src[i] == '\\' && i+1 < len(src) {
					i++
				}
				b.WriteByte(src[i])
				i++
			}
			i++
			out = append(out, token{b.String(), false})
		default:
			j := i
			for j < len(src) && !strings.ContainsRune(" \t\r\n;{}#", rune(src[j])) {
				j++
			}
			out = append(out, token{src[i:j], false})
			i = j
		}
	}
	return out
}

// parseDirectives reads directives until the end or a closing brace and
// returns them with the index after the last token it used.
func parseDirectives(toks []token, i int, file string) ([]directive, int) {
	var out []directive
	for i < len(toks) {
		t := toks[i]
		if t.punc && t.s == "}" {
			return out, i + 1
		}
		if t.punc { // a stray ; or {
			i++
			continue
		}
		d := directive{name: t.s, file: file}
		i++
		for i < len(toks) && !toks[i].punc {
			d.args = append(d.args, toks[i].s)
			i++
		}
		if i < len(toks) && toks[i].s == "{" {
			var block []directive
			block, i = parseDirectives(toks, i+1, file)
			if block == nil {
				block = []directive{}
			}
			d.block = block
		} else if i < len(toks) {
			i++ // the ;
		}
		out = append(out, d)
	}
	return out, i
}

// nginxSites turns the http servers into sites.
func nginxSites(top []directive) []Site {
	var sites []Site
	for _, h := range top {
		if h.name != "http" {
			continue
		}
		upstreams := map[string][]string{}
		walk(h.block, func(d directive) {
			if d.name == "upstream" && len(d.args) == 1 {
				for _, s := range d.block {
					if s.name == "server" && len(s.args) > 0 {
						upstreams[d.args[0]] = append(upstreams[d.args[0]], s.args[0])
					}
				}
			}
		})
		for _, d := range h.block {
			if d.name == "server" {
				sites = append(sites, nginxServer(d, upstreams))
			}
		}
	}
	return sites
}

// walk calls fn on every directive in ds and in the blocks inside them.
func walk(ds []directive, fn func(directive)) {
	for _, d := range ds {
		fn(d)
		walk(d.block, fn)
	}
}

func nginxServer(srv directive, upstreams map[string][]string) Site {
	site := Site{Source: "nginx", File: srv.file}
	seenUp := map[Upstream]bool{}
	var walkLoc func(ds []directive, path string)
	walkLoc = func(ds []directive, path string) {
		for _, d := range ds {
			switch d.name {
			case "server_name":
				for _, n := range d.args {
					if n == "_" || n == `""` || n == "" {
						n = "*"
					}
					site.Hosts = appendUnique(site.Hosts, n)
				}
			case "listen":
				if len(d.args) > 0 {
					if port := listenPort(d.args[0]); port == 443 {
						site.TLS = true
					}
					for _, a := range d.args[1:] {
						if a == "ssl" || a == "quic" {
							site.TLS = true
						}
					}
				}
			case "proxy_pass", "grpc_pass":
				if len(d.args) == 1 {
					for _, up := range resolveUpstream(d.args[0], upstreams) {
						up.Path = path
						if !seenUp[up] {
							seenUp[up] = true
							site.Upstreams = append(site.Upstreams, up)
						}
					}
				}
			case "location":
				p := "/"
				if len(d.args) > 0 {
					p = d.args[len(d.args)-1]
					if len(d.args) > 1 && strings.HasPrefix(d.args[0], "~") {
						p = "~ " + p
					}
				}
				walkLoc(d.block, p)
			default:
				if d.block != nil && (d.name == "if" || d.name == "limit_except") {
					walkLoc(d.block, path)
				}
			}
		}
	}
	walkLoc(srv.block, "/")
	if len(site.Hosts) == 0 {
		site.Hosts = []string{"*"}
	}
	return site
}

func appendUnique(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}

// listenPort reads the port out of a listen argument: "443", "[::]:443",
// "127.0.0.1:8080", "*:80". Anything else (a unix socket) gives 0.
func listenPort(a string) int {
	if strings.HasPrefix(a, "unix:") {
		return 0
	}
	if n, err := strconv.Atoi(a); err == nil {
		return n
	}
	if _, p, err := net.SplitHostPort(a); err == nil {
		n, _ := strconv.Atoi(p)
		return n
	}
	return 0
}

// resolveUpstream turns a proxy_pass target into addresses: a literal
// host:port, or the servers of a named upstream block.
func resolveUpstream(target string, upstreams map[string][]string) []Upstream {
	host, port, ok := parseUpstream(target)
	if !ok {
		return nil
	}
	if servers, named := upstreams[host]; named && !hasPortInTarget(target) {
		var out []Upstream
		for _, s := range servers {
			if h, p, ok := parseUpstream("http://" + s); ok {
				out = append(out, Upstream{Host: h, Port: p})
			}
		}
		return out
	}
	return []Upstream{{Host: host, Port: port}}
}

func hasPortInTarget(t string) bool {
	rest := t
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return strings.LastIndex(rest, ":") > strings.LastIndex(rest, "]")
}

// parseUpstream reads "http://127.0.0.1:3000/path", "localhost:3000" or
// "https://app" into a host and a port. Unix sockets and targets built from
// variables are not understood and give ok=false.
func parseUpstream(raw string) (host string, port int, ok bool) {
	s := strings.TrimSpace(raw)
	scheme := ""
	if i := strings.Index(s, "://"); i >= 0 {
		scheme, s = strings.ToLower(s[:i]), s[i+3:]
	}
	if strings.HasPrefix(s, "unix:") || strings.Contains(s, "$") || s == "" {
		return "", 0, false
	}
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil {
			return "", 0, false
		}
		return h, n, true
	}
	s = strings.Trim(s, "[]")
	if s == "" {
		return "", 0, false
	}
	if scheme == "https" {
		return s, 443, true
	}
	return s, 80, true
}
