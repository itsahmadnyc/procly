package domains

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestCaddyfile(t *testing.T) {
	dir := tree(t, map[string]string{
		"Caddyfile": `{
	email me@example.com
}
(logging) {
	log { output file /var/log/caddy.log }
}
api.example.com, www.api.example.com {
	import logging
	encode gzip
	reverse_proxy localhost:3000   # the app
	reverse_proxy /ws/* 127.0.0.1:4000 127.0.0.1:4001 {
		lb_policy round_robin
	}
}
http://plain.example.com {
	reverse_proxy 127.0.0.1:5000
}
:8080 {
	handle_path /v2/* {
		reverse_proxy http://127.0.0.1:6000
	}
	handle {
		reverse_proxy {
			to 127.0.0.1:6001
		}
	}
	reverse_proxy 10.5.5.5:3000
}
import sites/*
`,
		"sites/blog.caddy": `blog.example.com {
	reverse_proxy 127.0.0.1:7000
}`,
	})
	src, sites := readCaddy([]string{filepath.Join(dir, "Caddyfile"), filepath.Join(dir, "missing")})
	if !src.Found || src.Error != "" || src.Name != "caddy" {
		t.Fatalf("source: %+v", src)
	}
	scan := Scan{Sites: sites}
	check := func(port int, host, scheme, path string, aliases []string) {
		t.Helper()
		got := scan.Match([]int{port})
		if len(got) != 1 {
			t.Fatalf("port %d: %+v", port, got)
		}
		d := got[0]
		if d.Host != host || d.Scheme != scheme || d.Path != path || d.Source != "caddy" || !reflect.DeepEqual(d.Aliases, aliases) {
			t.Errorf("port %d: %+v", port, d)
		}
	}
	check(3000, "api.example.com", "https", "", []string{"www.api.example.com"})
	check(4000, "api.example.com", "https", "/ws/*", []string{"www.api.example.com"})
	check(4001, "api.example.com", "https", "/ws/*", []string{"www.api.example.com"})
	check(5000, "plain.example.com", "http", "", nil)
	check(6000, "*", "http", "/v2/*", nil)
	check(6001, "*", "http", "", nil)
	check(7000, "blog.example.com", "https", "", nil)
	if len(scan.Match([]int{3000, 7000})) != 2 {
		t.Error("a process on two ports has both domains")
	}
	if src, _ := readCaddy([]string{filepath.Join(dir, "nope")}); src.Found {
		t.Error("no Caddyfile is not found")
	}
}

func TestApache(t *testing.T) {
	dir := tree(t, map[string]string{
		"site.conf": `# front
<VirtualHost *:443>
    ServerName app.example.com
    ServerAlias www.app.example.com "quoted.example.com"
    SSLEngine on
    ProxyPreserveHost On
    ProxyPass /static !
    ProxyPass / http://127.0.0.1:3000/
    ProxyPass "/api" "http://localhost:4000/api"
    ProxyPassMatch ^/img/(.*)$ http://127.0.0.1:9000/$1
    <Location /socket>
        ProxyPass ws://127.0.0.1:5000/
    </Location>
</VirtualHost>
<VirtualHost *:80>
    ServerName app.example.com
    Redirect permanent / https://app.example.com/
</VirtualHost>
<VirtualHost 10.0.0.1:80>
    ProxyPass / http://10.9.9.9:3000/
</VirtualHost>
`,
	})
	src, sites := readApache([]string{dir, filepath.Join(dir, "gone")})
	if !src.Found || src.Error != "" || src.Name != "apache" || src.Sites != 0 {
		t.Fatalf("source: %+v", src)
	}
	if len(sites) != 3 {
		t.Fatalf("three virtual hosts: %+v", sites)
	}
	scan := Scan{Sites: sites}
	for _, tc := range []struct {
		port int
		path string
	}{{3000, ""}, {4000, "/api"}, {9000, "~ ^/img/(.*)$"}, {5000, "/socket"}} {
		got := scan.Match([]int{tc.port})
		if len(got) != 1 || got[0].Host != "app.example.com" || got[0].Scheme != "https" || got[0].Path != tc.path || got[0].Source != "apache" {
			t.Errorf("port %d: %+v", tc.port, got)
		}
	}
	if got := scan.Match([]int{3000}); len(got[0].Aliases) != 2 {
		t.Errorf("aliases: %v", got[0].Aliases)
	}
}

func TestScanMatchesOnlyLocalUpstreamsAndDedupes(t *testing.T) {
	s := Scan{Sites: []Site{
		{Source: "nginx", File: "a", Hosts: []string{"x.example.com"}, TLS: true, Upstreams: []Upstream{{"127.0.0.1", 80, "/"}, {"10.1.2.3", 80, "/"}}},
		{Source: "nginx", File: "a", Hosts: []string{"x.example.com"}, Upstreams: []Upstream{{"localhost", 80, "/"}}}, // http twin
	}}
	got := s.Match([]int{80})
	if len(got) != 2 { // 127.0.0.1:80 and localhost:80 are two different upstream strings
		t.Fatalf("%+v", got)
	}
	for _, d := range got {
		if d.Scheme != "https" {
			t.Errorf("a host with a TLS site is https even through its plain twin: %+v", d)
		}
	}
}

func TestDNSClassification(t *testing.T) {
	if d := classify([]string{"104.21.38.114", "172.67.222.70"}); d.Status != "cloudflare" {
		t.Errorf("cloudflare: %+v", d)
	}
	if d := classify([]string{"127.0.0.1"}); d.Status != "elsewhere" && d.Status != "here" {
		t.Errorf("loopback: %+v", d) // 127.0.0.1 is on lo, so "here" is right too
	}
	if d := classify([]string{"203.0.113.9"}); d.Status != "elsewhere" || len(d.Addrs) != 1 {
		t.Errorf("elsewhere: %+v", d)
	}
	for host, want := range map[string]bool{"app.example.com": true, "*": false, "_": false, "*.example.com": false, "~^x": false, "10.0.0.1": false, "localhost": false, "intranet": false, "a b": false} {
		if got := plainHost(host); got != want {
			t.Errorf("plainHost(%q) = %v", host, got)
		}
	}
}
