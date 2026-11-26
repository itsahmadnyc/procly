package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itsahmadnyc/procly/internal/proc"
)

type api struct {
	t        *testing.T
	h        http.Handler
	shutdown int
}

func newAPI(t *testing.T) *api {
	t.Setenv("PROCLY_HOME", t.TempDir())
	m := proc.NewManager(t.TempDir(), log.New(io.Discard, "", 0))
	t.Cleanup(func() { m.StopAll() })
	a := &api{t: t}
	a.h = newHandler(m, func() { a.shutdown++ }, func() { a.shutdown++ })
	return a
}

func (a *api) do(method, path, body string, out any) int {
	a.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, httptest.NewRequest(method, path, r))
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			a.t.Fatalf("%s %s: bad JSON %q", method, path, rec.Body.String())
		}
	}
	return rec.Code
}

func (a *api) start(name, command string) proc.Info {
	a.t.Helper()
	spec, _ := json.Marshal(proc.Spec{Name: name, Command: command, Cwd: a.t.TempDir()})
	var info proc.Info
	if code := a.do(http.MethodPost, "/v1/processes", string(spec), &info); code != http.StatusCreated {
		a.t.Fatalf("start %s: status %d", name, code)
	}
	return info
}

func TestPing(t *testing.T) {
	a := newAPI(t)
	var p map[string]any
	if code := a.do(http.MethodGet, "/v1/ping", "", &p); code != http.StatusOK || p["version"] == "" || p["pid"] == nil {
		t.Fatalf("ping: %d %v", code, p)
	}
}

func TestStartListStopRestartDelete(t *testing.T) {
	a := newAPI(t)
	started := a.start("api", "sleep 30")
	if started.Status != proc.StatusOnline || started.PID == 0 {
		t.Fatalf("started: %+v", started)
	}

	var list []proc.Info
	if code := a.do(http.MethodGet, "/v1/processes", "", &list); code != http.StatusOK || len(list) != 1 || list[0].Name != "api" {
		t.Fatalf("list: %d %+v", code, list)
	}
	var one proc.Info
	if code := a.do(http.MethodGet, "/v1/processes/0", "", &one); code != http.StatusOK || one.Name != "api" {
		t.Fatalf("get by id: %d %+v", code, one)
	}

	var infos []proc.Info
	if code := a.do(http.MethodPost, "/v1/processes/api/stop", "", &infos); code != http.StatusOK || infos[0].Status != proc.StatusStopped {
		t.Fatalf("stop: %d %+v", code, infos)
	}
	if code := a.do(http.MethodPost, "/v1/processes/api/restart", "", &infos); code != http.StatusOK || infos[0].Status != proc.StatusOnline {
		t.Fatalf("restart: %d %+v", code, infos)
	}
	if code := a.do(http.MethodDelete, "/v1/processes/api", "", &infos); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if code := a.do(http.MethodGet, "/v1/processes", "", &list); code != http.StatusOK || len(list) != 0 {
		t.Fatalf("after delete: %+v", list)
	}
}

func TestErrors(t *testing.T) {
	a := newAPI(t)
	a.start("api", "sleep 30")
	var e map[string]string
	cases := []struct {
		method, path, body string
		want               int
	}{
		{http.MethodGet, "/v1/processes/ghost", "", http.StatusNotFound},
		{http.MethodPost, "/v1/processes/ghost/stop", "", http.StatusNotFound},
		{http.MethodDelete, "/v1/processes/ghost", "", http.StatusNotFound},
		{http.MethodPost, "/v1/processes", "{not json", http.StatusBadRequest},
		{http.MethodPost, "/v1/processes", `{"name":"all","command":"true","cwd":"/"}`, http.StatusBadRequest},
		{http.MethodPost, "/v1/processes", `{"name":"x","command":"true","cwd":"relative"}`, http.StatusBadRequest},
		{http.MethodPost, "/v1/processes", `{"name":"api","command":"sleep 30","cwd":"/"}`, http.StatusBadRequest}, // already running
	}
	for _, c := range cases {
		e = nil
		if code := a.do(c.method, c.path, c.body, &e); code != c.want || e["error"] == "" {
			t.Errorf("%s %s %s: got %d %v, want %d with an error message", c.method, c.path, c.body, code, e, c.want)
		}
	}
	if code := a.do(http.MethodPut, "/v1/processes/api/stop", "", nil); code != http.StatusMethodNotAllowed {
		t.Errorf("wrong method: %d", code)
	}
}

func TestLargeBodyRejected(t *testing.T) {
	a := newAPI(t)
	// A valid spec padded with an unknown field: only the size limit stops it.
	huge := `{"name":"x","command":"sleep 30","cwd":"/","pad":"` + strings.Repeat("a", 2<<20) + `"}`
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/processes", bytes.NewBufferString(huge)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("2 MB spec: %d, want 400 (bodies are capped at 1 MB)", rec.Code)
	}
}

func TestSaveAndResurrect(t *testing.T) {
	a := newAPI(t)
	a.start("keep", "sleep 30")
	var saved map[string]any
	if code := a.do(http.MethodPost, "/v1/save", "", &saved); code != http.StatusOK || saved["saved"] != float64(1) {
		t.Fatalf("save: %d %v", code, saved)
	}
	// Nothing to do: "keep" is already running.
	var infos []proc.Info
	if code := a.do(http.MethodPost, "/v1/resurrect", "", &infos); code != http.StatusOK || len(infos) != 0 {
		t.Fatalf("resurrect: %d %+v", code, infos)
	}
}

func TestKillStopsEverythingAndShutsDown(t *testing.T) {
	a := newAPI(t)
	a.start("a", "sleep 30")
	a.start("b", "sleep 30")
	var infos []proc.Info
	if code := a.do(http.MethodPost, "/v1/kill", "", &infos); code != http.StatusOK || len(infos) != 2 {
		t.Fatalf("kill: %d %+v", code, infos)
	}
	for _, in := range infos {
		if in.Status != proc.StatusStopped {
			t.Fatalf("%s still %s after kill", in.Name, in.Status)
		}
	}
	if a.shutdown != 1 {
		t.Fatalf("shutdown called %d times", a.shutdown)
	}
}
