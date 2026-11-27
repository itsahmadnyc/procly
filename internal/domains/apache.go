package domains

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The Apache reader reads *.conf files in the directories it is given
// (sites-enabled and conf.d, which hold the virtual hosts) and keeps, for
// each <VirtualHost>, the names, whether it speaks TLS, and its ProxyPass
// lines.

func readApache(roots []string) (Source, []Site) {
	src := Source{Name: "apache"}
	var sites []Site
	for _, root := range roots {
		files, _ := filepath.Glob(filepath.Join(root, "*.conf"))
		sort.Strings(files)
		if len(files) == 0 {
			if _, err := os.Stat(root); err == nil {
				src.Found = true
			}
			continue
		}
		src.Found = true
		for _, f := range files {
			b, err := readSmall(f)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) && src.Error == "" {
					src.Error = shortErr(err)
				}
				continue
			}
			sites = append(sites, apacheSites(f, string(b))...)
		}
	}
	return src, sites
}

func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxConfBytes))
}

func apacheSites(file, text string) []Site {
	var sites []Site
	var cur *Site
	loc := "/" // the <Location> a ProxyPass with one argument belongs to
	inLoc := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := apacheFields(line)
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(fields[0])
		switch {
		case name == "<virtualhost":
			cur = &Site{Source: "apache", File: file}
			for _, a := range fields[1:] {
				a = strings.TrimSuffix(a, ">")
				if strings.HasSuffix(a, ":443") {
					cur.TLS = true
				}
			}
		case name == "</virtualhost>":
			if cur != nil {
				if len(cur.Hosts) == 0 {
					cur.Hosts = []string{"*"}
				}
				sites = append(sites, *cur)
				cur = nil
			}
		case name == "<location" || name == "<locationmatch":
			inLoc = true
			loc = "/"
			if len(fields) > 1 {
				loc = strings.TrimSuffix(fields[1], ">")
				if name == "<locationmatch" {
					loc = "~ " + loc
				}
			}
		case name == "</location>" || name == "</locationmatch>":
			inLoc, loc = false, "/"
		case cur == nil:
			// a ProxyPass outside any virtual host applies to the whole server
			if name == "proxypass" {
				if t, p := apacheProxy(fields[1:], inLoc, loc); t != "" {
					g := Site{Source: "apache", File: file, Hosts: []string{"*"}}
					g.addUpstream(t, p)
					sites = append(sites, g)
				}
			}
		case name == "servername" && len(fields) > 1:
			cur.Hosts = append([]string{fields[1]}, cur.Hosts...)
		case name == "serveralias":
			for _, a := range fields[1:] {
				cur.Hosts = appendUnique(cur.Hosts, a)
			}
		case name == "sslengine" && len(fields) > 1 && strings.EqualFold(fields[1], "on"):
			cur.TLS = true
		case name == "proxypass" || name == "proxypassmatch":
			args := fields[1:]
			if name == "proxypassmatch" && len(args) > 0 {
				args = append([]string{"~ " + args[0]}, args[1:]...)
			}
			if t, p := apacheProxy(args, inLoc, loc); t != "" {
				cur.addUpstream(backrefs.ReplaceAllString(t, ""), p) // ProxyPassMatch targets end in $1
			}
		}
	}
	return sites
}

var backrefs = regexp.MustCompile(`\$[0-9]`)

// apacheProxy picks the path and the target out of a ProxyPass line:
// "ProxyPass /path http://host:port/" or, inside <Location>, just the target.
func apacheProxy(args []string, inLoc bool, loc string) (target, path string) {
	switch {
	case len(args) >= 2 && args[1] == "!":
		return "", "" // an exclusion
	case len(args) >= 2 && !strings.Contains(args[0], "://"):
		return args[1], args[0]
	case len(args) >= 1 && inLoc:
		return args[0], loc
	case len(args) >= 1:
		return args[0], "/"
	}
	return "", ""
}

// apacheFields splits a line on spaces, keeping quoted words whole.
func apacheFields(line string) []string {
	var out []string
	var b strings.Builder
	var q byte
	has := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case q != 0:
			if c == q {
				q = 0
			} else {
				b.WriteByte(c)
			}
		case c == '"' || c == '\'':
			q, has = c, true
		case c == ' ' || c == '\t':
			if has || b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
				has = false
			}
		case c == '#' && b.Len() == 0 && !has:
			return out
		default:
			b.WriteByte(c)
		}
	}
	if has || b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}
