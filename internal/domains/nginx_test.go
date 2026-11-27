package domains

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// tree writes files under a temp dir (a path ending in "->target" makes a
// symlink, the way sites-enabled does) and returns the dir.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if i := strings.Index(name, "->"); i >= 0 {
			link, target := filepath.Join(dir, name[:i]), filepath.Join(dir, name[i+2:])
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			continue
		}
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const nginxMain = `
user www-data;
events { worker_connections 768; }
http {
    include /etc/nginx/mime.types;   # does not exist here, and must not matter
    default_type application/octet-stream;
    include sites-enabled/*;
    include conf.d/*.conf;
}
stream { server { listen 5432; proxy_pass 127.0.0.1:5432; } }
`

// the shape certbot leaves behind: an https site that proxies, and an http
// site that only redirects
const certbotSite = `
server {
    server_name auclux-api.example.com;
    location / {
        proxy_pass http://127.0.0.1:5703;
        proxy_set_header Host $host;
    }
    listen [::]:443 ssl; # managed by Certbot
    listen 443 ssl; # managed by Certbot
    ssl_certificate /etc/letsencrypt/live/auclux-api.example.com/fullchain.pem;
}
server {
    if ($host = auclux-api.example.com) { return 301 https://$host$request_uri; } # managed by Certbot
    listen 80;
    listen [::]:80;
    server_name auclux-api.example.com;
    return 404; # managed by Certbot
}
`

func TestNginxReadsACertbotSiteThroughASymlinkedInclude(t *testing.T) {
	dir := tree(t, map[string]string{
		"nginx.conf":                 nginxMain,
		"sites-available/auclux-api": certbotSite,
		"sites-enabled/auclux-api->sites-available/auclux-api": "",
	})
	src, sites := readNginx(filepath.Join(dir, "nginx.conf"))
	if !src.Found || src.Error != "" || src.Name != "nginx" {
		t.Fatalf("source: %+v", src)
	}
	if len(sites) != 2 {
		t.Fatalf("both server blocks are sites, got %d: %+v", len(sites), sites)
	}
	scan := Scan{Sites: sites}
	got := scan.Match([]int{5703})
	if len(got) != 1 {
		t.Fatalf("one domain reaches port 5703: %+v", got)
	}
	d := got[0]
	if d.Host != "auclux-api.example.com" || d.Scheme != "https" || d.Source != "nginx" || d.Upstream != "127.0.0.1:5703" || d.Path != "" {
		t.Fatalf("domain: %+v", d)
	}
	if !strings.HasSuffix(d.File, "auclux-api") {
		t.Fatalf("the file it came from: %q", d.File)
	}
	if len(scan.Match([]int{5704})) != 0 || len(scan.Match(nil)) != 0 {
		t.Fatal("other ports and no ports match nothing")
	}
}

func TestNginxUpstreamBlocksLocationsAndNames(t *testing.T) {
	dir := tree(t, map[string]string{
		"nginx.conf": `http {
  upstream backend { server 127.0.0.1:3000; server 127.0.0.1:3001 weight=2; server unix:/run/x.sock; server 10.9.9.9:3000; }
  server {
    listen 80;
    server_name shop.example.com www.shop.example.com;
    location / { proxy_pass http://backend; }
    location /admin/ { proxy_pass http://localhost:4000/; }
    location ~ ^/ws { proxy_pass http://[::1]:4100; }
    location /static/ { root /var/www; }
    # location /old { proxy_pass http://127.0.0.1:9999; }
  }
}`,
	})
	_, sites := readNginx(filepath.Join(dir, "nginx.conf"))
	if len(sites) != 1 {
		t.Fatalf("sites: %+v", sites)
	}
	s := sites[0]
	if !reflect.DeepEqual(s.Hosts, []string{"shop.example.com", "www.shop.example.com"}) || s.TLS {
		t.Fatalf("hosts %v tls %v", s.Hosts, s.TLS)
	}
	scan := Scan{Sites: sites}
	for _, tc := range []struct {
		port int
		path string
	}{{3000, ""}, {3001, ""}, {4000, "/admin/"}, {4100, "~ ^/ws"}} {
		got := scan.Match([]int{tc.port})
		if len(got) != 1 || got[0].Host != "shop.example.com" || got[0].Path != tc.path || got[0].Scheme != "http" || !reflect.DeepEqual(got[0].Aliases, []string{"www.shop.example.com"}) {
			t.Errorf("port %d: %+v", tc.port, got)
		}
	}
	if len(scan.Match([]int{9999})) != 0 {
		t.Error("a commented-out proxy_pass must not count")
	}
	if len(scan.Match([]int{5432})) != 0 {
		t.Error("an upstream on another machine is not this process")
	}
}

func TestNginxDefaultSiteQuotesAndStreams(t *testing.T) {
	dir := tree(t, map[string]string{
		"nginx.conf": nginxMain,
		"conf.d/default.conf": `server {
  listen 80 default_server;
  server_name _;
  location / { proxy_pass "http://127.0.0.1:8080"; }
}
server {
  listen 443 ssl http2;
  server_name "api.example.org" *.example.org ~^(?<sub>.+)\.example\.net$;
  location / { proxy_pass http://127.0.0.1:8081; }
}`,
	})
	_, sites := readNginx(filepath.Join(dir, "nginx.conf"))
	scan := Scan{Sites: sites}
	d := scan.Match([]int{8080})
	if len(d) != 1 || d[0].Host != "*" || d[0].Scheme != "http" {
		t.Fatalf("the default site: %+v", d)
	}
	d = scan.Match([]int{8081})
	if len(d) != 1 || d[0].Host != "api.example.org" || d[0].Scheme != "https" || len(d[0].Aliases) != 2 {
		t.Fatalf("names with quotes, a wildcard and a regular expression: %+v", d)
	}
	if len(scan.Match([]int{5432})) != 0 {
		t.Fatal("a stream (TCP) proxy is not a web domain")
	}
}

func TestNginxMissingOrUnreadable(t *testing.T) {
	src, sites := readNginx(filepath.Join(t.TempDir(), "nope.conf"))
	if src.Found || src.Error != "" || sites != nil {
		t.Fatalf("no nginx: %+v", src)
	}
	dir := tree(t, map[string]string{"nginx.conf": "http { include conf.d/*.conf; include sites/*; server { listen 80; server_name a.example.com; location / { proxy_pass http://127.0.0.1:1; } } }", "sites/locked": "server {}"})
	if err := os.Chmod(filepath.Join(dir, "sites/locked"), 0); err == nil && os.Getuid() != 0 {
		src, sites = readNginx(filepath.Join(dir, "nginx.conf"))
		if !src.Found || src.Error == "" || len(sites) != 1 {
			t.Fatalf("one unreadable include is reported and the rest still read: %+v %d", src, len(sites))
		}
	}
	// a loop of includes ends
	loop := tree(t, map[string]string{"nginx.conf": "http { include nginx.conf; server { listen 80; } }"})
	if _, sites := readNginx(filepath.Join(loop, "nginx.conf")); len(sites) == 0 {
		t.Fatal("a self-including config still gives its own sites")
	}
}

func TestParseUpstream(t *testing.T) {
	for in, want := range map[string][3]any{
		"http://127.0.0.1:3000":      {"127.0.0.1", 3000, true},
		"http://127.0.0.1:3000/path": {"127.0.0.1", 3000, true},
		"localhost:3000":             {"localhost", 3000, true},
		"https://app":                {"app", 443, true},
		"http://app":                 {"app", 80, true},
		"http://[::1]:4100/x":        {"::1", 4100, true},
		"http://user:pw@10.0.0.1:81": {"10.0.0.1", 81, true},
		"http://unix:/run/x.sock:/":  {"", 0, false},
		"http://$upstream":           {"", 0, false},
		"":                           {"", 0, false},
	} {
		h, p, ok := parseUpstream(in)
		if h != want[0] || p != want[1] || ok != want[2] {
			t.Errorf("parseUpstream(%q) = %q %d %v, want %v", in, h, p, ok, want)
		}
	}
}
