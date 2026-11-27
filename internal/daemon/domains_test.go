package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/itsahmadnyc/procly/internal/domains"
	"github.com/itsahmadnyc/procly/internal/proc"
)

// TestHelperListen is not a test: it is the app of TestDomainsRoute. It
// listens on PROCLY_TEST_LISTEN until it is killed.
func TestHelperListen(t *testing.T) {
	port := os.Getenv("PROCLY_TEST_LISTEN")
	if port == "" {
		t.Skip("helper process only")
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(3)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			os.Exit(0)
		}
		c.Close()
	}
}

func TestDomainsRoute(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	dir := t.TempDir()
	conf := filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(conf, []byte(fmt.Sprintf(`http {
  server { listen 443 ssl; server_name app.example.test; location / { proxy_pass http://127.0.0.1:%d; } }
  server { listen 80; server_name other.example.test; location / { proxy_pass http://127.0.0.1:1; } }
}`, port)), 0o644); err != nil {
		t.Fatal(err)
	}
	domainOptions = func() domains.Options { return domains.Options{NginxConf: conf} }
	defer func() { domainOptions = domains.DefaultOptions }()

	a := newAPI(t)
	spec, _ := json.Marshal(proc.Spec{
		Name: "app", Command: "exec " + os.Args[0] + " -test.run='^TestHelperListen$'", Cwd: t.TempDir(),
		Env: map[string]string{"PROCLY_TEST_LISTEN": strconv.Itoa(port)},
	})
	if code := a.do("POST", "/v1/processes", string(spec), nil); code != 201 {
		t.Fatalf("start: %d", code)
	}
	a.start("quiet", "sleep 30")

	var rep domains.Report
	deadline := time.Now().Add(8 * time.Second)
	for {
		if code := a.do("GET", "/v1/domains?refresh=1", "", &rep); code != 200 {
			t.Fatalf("GET /v1/domains: %d", code)
		}
		if len(rep.Processes) == 2 && len(rep.Processes[0].Domains) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the app never showed a domain: %+v", rep)
		}
		time.Sleep(100 * time.Millisecond)
	}
	app, quiet := rep.Processes[0], rep.Processes[1]
	if app.Name != "app" || len(app.Ports) != 1 || app.Ports[0] != port {
		t.Fatalf("app: %+v", app)
	}
	d := app.Domains[0]
	if d.Host != "app.example.test" || d.Scheme != "https" || d.Source != "nginx" {
		t.Fatalf("domain: %+v", d)
	}
	if quiet.Name != "quiet" || len(quiet.Ports) != 0 || len(quiet.Domains) != 0 {
		t.Fatalf("a process with no sockets has no domains: %+v", quiet)
	}
	if len(rep.Sources) == 0 || rep.Sources[0].Name != "nginx" || !rep.Sources[0].Found || rep.Sources[0].Sites != 2 {
		t.Fatalf("sources: %+v", rep.Sources)
	}

	// a second call without refresh is the cached answer
	var again domains.Report
	a.do("GET", "/v1/domains", "", &again)
	if !again.ScannedAt.Equal(rep.ScannedAt) {
		t.Error("an answer is kept for a few seconds")
	}
}

func TestModeRoute(t *testing.T) {
	a := newAPI(t)
	a.start("tool", "sleep 30")
	var e struct{ Error string }
	if code := a.do("POST", "/v1/processes/tool/mode", `{"mode":"cluster","instances":2}`, &e); code != 400 || e.Error == "" {
		t.Fatalf("a command that is not a node script cannot be clustered: %d %q", code, e.Error)
	}
	if code := a.do("POST", "/v1/processes/tool/mode", `{"mode":"turbo"}`, &e); code != 400 {
		t.Fatalf("an unknown mode: %d", code)
	}
	if code := a.do("POST", "/v1/processes/tool/mode", `not json`, &e); code != 400 {
		t.Fatalf("bad JSON: %d", code)
	}
	if code := a.do("POST", "/v1/processes/nope/mode", `{"mode":"cluster"}`, &e); code != 404 {
		t.Fatalf("an unknown app: %d", code)
	}
	var in proc.Info
	if code := a.do("GET", "/v1/processes/tool", "", &in); code != 200 || in.Status != proc.StatusOnline {
		t.Fatalf("refused changes leave it running: %d %+v", code, in)
	}
	if _, err := exec.LookPath("node"); err != nil {
		return
	}
	spec, _ := json.Marshal(proc.Spec{Name: "web", Command: "node -e 'setInterval(()=>{},1000)'", Cwd: t.TempDir()})
	if code := a.do("POST", "/v1/processes", string(spec), nil); code != 201 {
		t.Fatalf("start: %d", code)
	}
	// "node -e" is not a script file, so it cannot be a cluster either
	if code := a.do("POST", "/v1/processes/web/mode", `{"mode":"cluster","instances":2}`, &e); code != 400 || !strings.Contains(e.Error, "script file") {
		t.Fatalf("node -e: %d %q", code, e.Error)
	}
}
