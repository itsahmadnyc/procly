package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"

	"github.com/itsahmadnyc/procly/internal/domains"
	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/sse"
	"github.com/itsahmadnyc/procly/internal/sysinfo"
	"github.com/itsahmadnyc/procly/internal/tlsid"
)

// ErrHubDown means a tunnel server was asked for but `procly hub` isn't running here.
var ErrHubDown = errors.New("procly hub is not running on this machine, start it with 'procly hub'")

func isDialError(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial" &&
		(errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED))
}

// Client talks to one agent. It trusts exactly one certificate, the one
// whose fingerprint was given to `procly server add`.
type Client struct {
	s      Server
	base   string
	api    *http.Client // short requests
	stream *http.Client // log streams, no overall timeout
}

type Ping struct {
	Version string `json:"version"`
	Host    string `json:"host"`
	Daemon  bool   `json:"daemon"`
}

var ErrFingerprint = tlsid.ErrMismatch

func NewClient(s Server) *Client {
	if s.Mode == ModeTunnel {
		return newTunnelClient(s)
	}
	tr := &http.Transport{TLSClientConfig: tlsid.Pinned(s.Fingerprint), ForceAttemptHTTP2: true, IdleConnTimeout: 90 * time.Second}
	return &Client{
		s:      s,
		base:   s.URL,
		api:    &http.Client{Transport: tr, Timeout: 90 * time.Second},
		stream: &http.Client{Transport: tr},
	}
}

// newTunnelClient reaches an agent that dialed in to `procly hub`, through
// the hub's local socket. The hub already authenticated that agent.
func newTunnelClient(s Server) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", paths.HubSocket())
		},
	}
	return &Client{
		s:      s,
		base:   "http://hub/servers/" + url.PathEscape(s.Name),
		api:    &http.Client{Transport: tr, Timeout: 90 * time.Second},
		stream: &http.Client{Transport: tr},
	}
}

func (c *Client) Name() string { return c.s.Name }

func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, out any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(nil))
	if err != nil {
		return nil, err
	}
	if c.s.Mode != ModeTunnel {
		req.Header.Set("Authorization", "Bearer "+c.s.Token)
	}
	resp, err := hc.Do(req)
	if err != nil {
		if c.s.Mode == ModeTunnel && isDialError(err) {
			return nil, ErrHubDown
		}
		var uerr *url.Error
		if errors.As(err, &uerr) && errors.Is(uerr.Err, ErrFingerprint) {
			return nil, ErrFingerprint
		}
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return nil, &StatusError{Code: resp.StatusCode, Message: e.Error}
	}
	if out == nil {
		return resp, nil
	}
	defer resp.Body.Close()
	return resp, json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(out)
}

// StatusError is an error response from the agent.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string { return e.Message }

func (c *Client) Ping(ctx context.Context) (Ping, error) {
	var p Ping
	_, err := c.do(ctx, c.api, http.MethodGet, "/v1/ping", &p)
	return p, err
}

func (c *Client) List(ctx context.Context) ([]proc.View, error) {
	var out []proc.View
	_, err := c.do(ctx, c.api, http.MethodGet, "/v1/processes", &out)
	return out, err
}

func (c *Client) Action(ctx context.Context, name, verb string) error {
	_, err := c.do(ctx, c.api, http.MethodPost, "/v1/processes/"+url.PathEscape(name)+"/"+url.PathEscape(verb), &struct{}{})
	return err
}

func (c *Client) Save(ctx context.Context) (int, error) {
	var out struct {
		Saved int `json:"saved"`
	}
	_, err := c.do(ctx, c.api, http.MethodPost, "/v1/save", &out)
	return out.Saved, err
}

// System returns the agent machine's load, memory, disk and uptime.
// Agents older than 0.3 don't have it and answer 404.
func (c *Client) System(ctx context.Context) (sysinfo.System, error) {
	var out sysinfo.System
	_, err := c.do(ctx, c.api, http.MethodGet, "/v1/system", &out)
	return out, err
}

// Domains returns which names reach each process on the agent's machine.
// Agents older than this feature answer 404. An agent keeps its answer for a
// few seconds and has no way to be told to look again, so refresh is ignored.
func (c *Client) Domains(ctx context.Context, _ bool) (domains.Report, error) {
	var out domains.Report
	_, err := c.do(ctx, c.api, http.MethodGet, "/v1/domains", &out)
	return out, err
}

// Logs relays the agent's log stream to send until ctx ends.
func (c *Client) Logs(ctx context.Context, name string, send func([]logtail.Event) bool) error {
	resp, err := c.do(ctx, c.stream, http.MethodGet, "/v1/processes/"+url.PathEscape(name)+"/logs", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := sse.Read(resp.Body, send); err != nil && ctx.Err() == nil {
		return fmt.Errorf("log stream from %s: %w", c.s.Name, err)
	}
	return nil
}
