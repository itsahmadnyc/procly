// Package client talks to the procly daemon over its unix socket.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/proc"
)

type Client struct{ hc *http.Client }

type Ping struct {
	Version string `json:"version"`
	PID     int    `json:"pid"`
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
	return &Client{hc: &http.Client{Transport: tr, Timeout: 2 * time.Minute}}
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
