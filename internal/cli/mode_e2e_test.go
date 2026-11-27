package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/itsahmadnyc/procly/internal/proc"
)

const modeApp = `
const cluster = require('cluster');
const http = require('http');
const offset = cluster.isWorker ? 0 : Number(process.env.PROCLY_INSTANCE || 0);
http.createServer((q, r) => r.end(String(process.pid))).listen(Number(process.env.PORT) + offset);
`

func answeringPids(port, tries int) map[string]bool {
	seen := map[string]bool{}
	for i := 0; i < tries; i++ {
		c := http.Client{Timeout: 500 * time.Millisecond, Transport: &http.Transport{DisableKeepAlives: true}}
		r, err := c.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/")
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		seen[string(b)] = true
	}
	return seen
}

func TestClusterFromTheCommandLine(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	c := newCLI(t)
	c.file("app.js", modeApp)
	views := func() map[string]proc.View {
		var vs []proc.View
		if err := json.Unmarshal([]byte(c.ok("ls", "--json")), &vs); err != nil {
			t.Fatal(err)
		}
		m := map[string]proc.View{}
		for _, v := range vs {
			m[v.Name] = v
		}
		return m
	}
	wait := func(what string, ok func() bool) {
		t.Helper()
		waitFor(t, what, ok)
	}

	c.ok("start", "app.js", "--name", "web", "--exec-mode", "cluster", "--instances", "2", "-e", fmt.Sprintf("PORT=%d", port))
	wait("two workers", func() bool { return len(answeringPids(port, 30)) == 2 })
	v := views()["web"]
	if v.ExecMode != "cluster" || v.Workers != 2 || v.ReloadSignal != "SIGUSR2" {
		t.Fatalf("a cluster app in the list: %+v", v)
	}
	contains(t, c.ok("describe", "web"), "cluster, 2 workers")
	c.ok("reload", "web")

	// without --yes and without a terminal nothing changes
	out := c.ok("mode", "web", "fork", "--instances", "2")
	contains(t, out, "Switching it to fork restarts it", "pass --yes")
	if vs := views(); vs["web"].ExecMode != "cluster" {
		t.Fatalf("it must not change without a yes: %+v", vs)
	}

	c.ok("mode", "web", "fork", "--instances", "2", "--yes")
	vs := views()
	if _, gone := vs["web"]; gone || vs["web-1"].Group != "web" || vs["web-2"].Group != "web" || vs["web-1"].ExecMode != "" {
		t.Fatalf("two separate instances: %+v", vs)
	}
	wait("each instance", func() bool { return len(answeringPids(port, 10)) == 1 && len(answeringPids(port+1, 10)) == 1 })

	c.ok("mode", "web", "cluster", "--yes") // the two instances become two workers
	vs = views()
	if v := vs["web"]; v.ExecMode != "cluster" || v.Workers != 2 || len(vs) != 1 {
		t.Fatalf("back to a cluster: %+v", vs)
	}
	wait("workers again", func() bool { return len(answeringPids(port, 30)) == 2 })

	contains(t, c.fails("mode", "web", "cluster", "--yes"), "already runs that way")
	contains(t, c.fails("mode", "web", "turbo"), "usage")
	contains(t, c.fails("mode", "nope", "cluster", "--yes"), "not found")
	c.ok("start", "sleep 30", "--name", "tool")
	contains(t, c.fails("mode", "tool", "cluster", "--yes"), "another program")
	contains(t, c.fails("start", "sleep 30", "--name", "bad", "--exec-mode", "cluster"), "another program")
}
