// Package client talks to the procly daemon over its unix socket.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/itsahmadnyc/procly/internal/domains"
	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/proc"
)

type Client struct{ hc *http.Client }

type Ping struct {
	Version    string `json:"version"`
	PID        int    `json:"pid"`
	Supervisor string `json:"supervisor,omitempty"` // "systemd" when a service manager runs the daemon
}

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

func New() *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", paths.Socket())
		},
	}
	// Stopping many processes can take a while (kill timeout is 5s each, in parallel).
	return &Client{hc: &http.Client{Transport: tr, Timeout: 6 * time.Minute}} // a mode switch waits to see the new setup work
}

func (c *Client) Ping() (Ping, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var p Ping
	err := c.call(ctx, http.MethodGet, "/v1/ping", nil, &p)
	return p, err
}

func (c *Client) List() ([]proc.Info, error) {
	var out []proc.Info
	return out, c.call(context.Background(), http.MethodGet, "/v1/processes", nil, &out)
}

// Domains asks which names reach each running process. The daemon keeps an
// answer for a few seconds; refresh asks for a new look.
func (c *Client) Domains(refresh bool) (domains.Report, error) {
	var out domains.Report
	path := "/v1/domains"
	if refresh {
		path += "?refresh=1"
	}
	return out, c.call(context.Background(), http.MethodGet, path, nil, &out)
}

func (c *Client) Get(target string) (proc.Info, error) {
	var out proc.Info
	return out, c.call(context.Background(), http.MethodGet, "/v1/processes/"+url.PathEscape(target), nil, &out)
}

func (c *Client) Start(spec proc.Spec) (proc.Info, error) {
	var out proc.Info
	return out, c.call(context.Background(), http.MethodPost, "/v1/processes", spec, &out)
}

func (c *Client) Stop(target string) ([]proc.Info, error) {
	return c.many(http.MethodPost, "/v1/processes/"+url.PathEscape(target)+"/stop")
}

// SetMode changes how an app runs: "fork" (separate processes) or "cluster"
// (workers sharing the app's ports). instances is the worker count in cluster
// mode, or how many separate processes in fork mode; 0 picks a default.
func (c *Client) SetMode(target, mode string, instances int) ([]proc.Info, error) {
	var out []proc.Info
	body := map[string]any{"mode": mode, "instances": instances}
	return out, c.call(context.Background(), http.MethodPost, "/v1/processes/"+url.PathEscape(target)+"/mode", body, &out)
}

// Handover asks the daemon for the list of processes to pass on and to exit
// without stopping them. The daemon is gone a moment after this returns.
func (c *Client) Handover() ([]proc.HandoverEntry, error) {
	var out []proc.HandoverEntry
	err := c.call(context.Background(), http.MethodPost, "/v1/handover", nil, &out)
	return out, err
}

// Add registers a process without starting it.
func (c *Client) Add(spec proc.Spec) (proc.Info, error) {
	var out proc.Info
	err := c.call(context.Background(), http.MethodPost, "/v1/add", spec, &out)
	return out, err
}

func (c *Client) Reload(target string) ([]proc.Info, error) {
	return c.many(http.MethodPost, "/v1/processes/"+url.PathEscape(target)+"/reload")
}

func (c *Client) Restart(target string) ([]proc.Info, error) {
	return c.many(http.MethodPost, "/v1/processes/"+url.PathEscape(target)+"/restart")
}

func (c *Client) Delete(target string) ([]proc.Info, error) {
	return c.many(http.MethodDelete, "/v1/processes/"+url.PathEscape(target))
}

func (c *Client) Resurrect() ([]proc.Info, error) { return c.many(http.MethodPost, "/v1/resurrect") }
func (c *Client) Kill() ([]proc.Info, error)      { return c.many(http.MethodPost, "/v1/kill") }

func (c *Client) Save() (int, string, error) {
	var out struct {
		Saved int    `json:"saved"`
		Path  string `json:"path"`
	}
	err := c.call(context.Background(), http.MethodPost, "/v1/save", nil, &out)
	return out.Saved, out.Path, err
}

func (c *Client) many(method, path string) ([]proc.Info, error) {
	var out []proc.Info
	return out, c.call(context.Background(), method, path, nil, &out)
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://procly"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return &APIError{Status: resp.StatusCode, Message: e.Error}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// Scale changes the workers of a cluster app: to n, or by n when relative.
func (c *Client) Scale(target string, n int, relative bool) (proc.Info, error) {
	var out proc.Info
	return out, c.call(context.Background(), http.MethodPost, "/v1/processes/"+url.PathEscape(target)+"/scale", map[string]any{"workers": n, "relative": relative}, &out)
}

// Preflight says what to know before running an app in a mode.
func (c *Client) Preflight(target, mode string, workers int) ([]proc.Warning, error) {
	var out []proc.Warning
	return out, c.call(context.Background(), http.MethodGet, fmt.Sprintf("/v1/processes/%s/preflight?mode=%s&workers=%d", url.PathEscape(target), url.QueryEscape(mode), workers), nil, &out)
}
