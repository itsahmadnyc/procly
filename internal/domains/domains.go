// Package domains finds out which domain names reach a local process: it reads
// the configuration of the reverse proxies on this machine (nginx, Caddy,
// Apache), and maps each site's upstream address to the ports a process
// listens on. It reads files and the DNS; it changes nothing.
package domains

import (
	"context"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Upstream is where a site sends its traffic.
type Upstream struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	Path string `json:"path,omitempty"` // the location it applies to, "/" when the whole site
}

// Site is one virtual host of a reverse proxy.
type Site struct {
	Source    string     `json:"source"` // nginx, caddy, apache
	File      string     `json:"file"`
	Hosts     []string   `json:"hosts"` // server names; "*" for a default site
	TLS       bool       `json:"tls"`
	Upstreams []Upstream `json:"upstreams"`
}

// Source says what was looked at, so an empty answer can be told from "nothing
// to look at" and "could not read".
type Source struct {
	Name  string `json:"name"`
	Found bool   `json:"found"`           // its configuration was there
	Sites int    `json:"sites"`           // how many sites it defines
	Error string `json:"error,omitempty"` // why it could not be read
}

// Scan is the result of reading all the proxies.
type Scan struct {
	Sources []Source `json:"sources"`
	Sites   []Site   `json:"sites"`
}

// Domain is one name that reaches a process.
type Domain struct {
	Host     string   `json:"host"`
	Scheme   string   `json:"scheme"` // https when the site has TLS
	Source   string   `json:"source"`
	File     string   `json:"file"`
	Upstream string   `json:"upstream"` // host:port the proxy forwards to
	Path     string   `json:"path,omitempty"`
	DNS      DNS      `json:"dns"`
	Aliases  []string `json:"aliases,omitempty"` // other names of the same site
}

// DNS says where a name points. Status is "here" (an address of this machine),
// "cloudflare", "elsewhere", "none" (does not resolve) or "" (not checked).
type DNS struct {
	Status string   `json:"status,omitempty"`
	Addrs  []string `json:"addrs,omitempty"`
}

// Options say where to look. The zero value looks in the usual places.
type Options struct {
	NginxConf   string   // main nginx.conf
	CaddyFiles  []string // Caddyfiles
	ApacheRoots []string // directories of Apache configuration files
}

// DefaultOptions are the usual places on Linux.
func DefaultOptions() Options {
	return Options{
		NginxConf:  "/etc/nginx/nginx.conf",
		CaddyFiles: []string{"/etc/caddy/Caddyfile"},
		ApacheRoots: []string{
			"/etc/apache2/sites-enabled", "/etc/apache2/conf-enabled", // Debian, Ubuntu
			"/etc/httpd/conf.d", // RHEL, Fedora
		},
	}
}

// Read reads every proxy it is told about.
func Read(o Options) Scan {
	var s Scan
	add := func(src Source, sites []Site) {
		src.Sites = len(sites)
		s.Sources = append(s.Sources, src)
		s.Sites = append(s.Sites, sites...)
	}
	add(readNginx(o.NginxConf))
	add(readCaddy(o.CaddyFiles))
	add(readApache(o.ApacheRoots))
	return s
}

// Match maps the sites onto a process that listens on ports.
func (s Scan) Match(ports []int) []Domain {
	if len(ports) == 0 {
		return nil
	}
	want := map[int]bool{}
	for _, p := range ports {
		want[p] = true
	}
	// Does any site of this host have TLS? A host often has a plain-http site
	// that only redirects, next to the https one that proxies.
	tls := map[string]bool{}
	for _, site := range s.Sites {
		if site.TLS {
			for _, h := range site.Hosts {
				tls[h] = true
			}
		}
	}
	var out []Domain
	seen := map[string]bool{}
	for _, site := range s.Sites {
		for _, up := range site.Upstreams {
			if !want[up.Port] || !isLocalHost(up.Host) {
				continue
			}
			hosts := site.Hosts
			if len(hosts) == 0 {
				hosts = []string{"*"}
			}
			host := hosts[0]
			k := site.Source + "|" + host + "|" + up.Path + "|" + net.JoinHostPort(up.Host, itoa(up.Port))
			if seen[k] {
				continue
			}
			seen[k] = true
			scheme := "http"
			if site.TLS || tls[host] {
				scheme = "https"
			}
			path := up.Path
			if path == "/" {
				path = ""
			}
			out = append(out, Domain{Host: host, Scheme: scheme, Source: site.Source, File: site.File,
				Upstream: net.JoinHostPort(up.Host, itoa(up.Port)), Path: path, Aliases: append([]string(nil), hosts[1:]...)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// isLocalHost reports whether a proxy's upstream host is this machine.
func isLocalHost(h string) bool {
	h = strings.Trim(h, "[]")
	switch strings.ToLower(h) {
	case "localhost", "", "0.0.0.0", "::", "::1", "ip6-localhost":
		return true
	}
	ip, err := netip.ParseAddr(h)
	if err != nil {
		hn, _ := os.Hostname()
		return hn != "" && strings.EqualFold(h, hn)
	}
	if ip.IsLoopback() {
		return true
	}
	for _, a := range localAddrs() {
		if a == ip {
			return true
		}
	}
	return false
}

var (
	localMu    sync.Mutex
	localCache []netip.Addr
	localAt    time.Time
)

// localAddrs are the addresses of this machine's interfaces.
func localAddrs() []netip.Addr {
	localMu.Lock()
	defer localMu.Unlock()
	if time.Since(localAt) < time.Minute && localCache != nil {
		return localCache
	}
	var out []netip.Addr
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	localCache, localAt = out, time.Now()
	return out
}

type dnsEntry struct {
	d  DNS
	at time.Time
}

var (
	dnsMu    sync.Mutex
	dnsCache = map[string]dnsEntry{}
)

// CheckDNS fills in where each domain's name points, looking the names up in
// parallel with a short limit. Names that are not plain host names (wildcards,
// regular expressions, the default site, addresses) are left unchecked.
func CheckDNS(ctx context.Context, ds []Domain) {
	resolve := func(host string) DNS {
		dnsMu.Lock()
		if c, ok := dnsCache[host]; ok && time.Since(c.at) < time.Minute {
			dnsMu.Unlock()
			return c.d
		}
		dnsMu.Unlock()
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		d := DNS{Status: "none"}
		if addrs, err := net.DefaultResolver.LookupHost(ctx, host); err == nil && len(addrs) > 0 {
			sort.Strings(addrs)
			d = classify(addrs)
		}
		dnsMu.Lock()
		dnsCache[host] = dnsEntry{d, time.Now()}
		dnsMu.Unlock()
		return d
	}
	var wg sync.WaitGroup
	cache := map[string]DNS{}
	var mu sync.Mutex
	for i := range ds {
		h := ds[i].Host
		if !plainHost(h) {
			continue
		}
		mu.Lock()
		_, done := cache[h]
		if !done {
			cache[h] = DNS{}
		}
		mu.Unlock()
		if done {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := resolve(h)
			mu.Lock()
			cache[h] = d
			mu.Unlock()
		}()
	}
	wg.Wait()
	for i := range ds {
		ds[i].DNS = cache[ds[i].Host]
	}
}

// plainHost reports whether h is a name a DNS lookup makes sense for.
func plainHost(h string) bool {
	if h == "" || h == "*" || h == "_" || strings.HasPrefix(h, "*.") || strings.HasPrefix(h, "~") || strings.ContainsAny(h, " ,/:$") {
		return false
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return false
	}
	return strings.Contains(h, ".") && !strings.EqualFold(h, "localhost")
}

// classify says where a set of addresses points.
func classify(addrs []string) DNS {
	d := DNS{Addrs: addrs}
	here, cf := 0, 0
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		for _, l := range localAddrs() {
			if l == ip {
				here++
				break
			}
		}
		if cloudflare(ip) {
			cf++
		}
	}
	switch {
	case here > 0:
		d.Status = "here"
	case cf > 0 && cf == len(addrs):
		d.Status = "cloudflare"
	default:
		d.Status = "elsewhere"
	}
	return d
}

var cloudflareNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
		"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func cloudflare(ip netip.Addr) bool {
	for _, p := range cloudflareNets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// ProcessDomains is what reaches one process: the ports it listens on and the
// names the proxies send to them.
type ProcessDomains struct {
	Name    string   `json:"name"`
	Ports   []int    `json:"ports"`
	Domains []Domain `json:"domains"`
}

// Report is the answer to "which of my processes have a domain?".
type Report struct {
	ScannedAt time.Time        `json:"scanned_at"`
	Sources   []Source         `json:"sources"`
	Processes []ProcessDomains `json:"processes"`
}

// Build matches a scan against the ports of each process (by name) and checks
// where the names point.
func Build(ctx context.Context, scan Scan, ports map[string][]int, checkDNS bool) Report {
	r := Report{ScannedAt: time.Now(), Sources: scan.Sources}
	names := make([]string, 0, len(ports))
	for n := range ports {
		names = append(names, n)
	}
	sort.Strings(names)
	var all []*Domain
	for _, n := range names {
		pd := ProcessDomains{Name: n, Ports: ports[n], Domains: scan.Match(ports[n])}
		if pd.Domains == nil {
			pd.Domains = []Domain{}
		}
		r.Processes = append(r.Processes, pd)
	}
	if checkDNS {
		for i := range r.Processes {
			for j := range r.Processes[i].Domains {
				all = append(all, &r.Processes[i].Domains[j])
			}
		}
		flat := make([]Domain, len(all))
		for i, d := range all {
			flat[i] = *d
		}
		CheckDNS(ctx, flat)
		for i, d := range all {
			d.DNS = flat[i].DNS
		}
	}
	return r
}
