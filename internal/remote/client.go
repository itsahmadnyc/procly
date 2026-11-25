package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/proc"
	"github.com/itsahmadnyc/procly/internal/sse"
)

// Client talks to one agent. It trusts exactly one certificate, the one
// whose fingerprint was given to `procly server add`.
type Client struct {
	s      Server
	api    *http.Client // short requests
	stream *http.Client // log streams, no overall timeout
}

type Ping struct {
	Version string `json:"version"`
	Host    string `json:"host"`
	Daemon  bool   `json:"daemon"`
}

var ErrFingerprint = errors.New("certificate fingerprint does not match; the agent was reinstalled or someone is in the middle")

func NewClient(s Server) *Client {
	want, _ := normalizeFingerprint(s.Fingerprint)
	tlsConf := &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Normal CA checks are replaced by pinning the agent's own certificate.
		InsecureSkipVerify: true, //nolint:gosec
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return ErrFingerprint
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			got := "sha256:" + hex.EncodeToString(sum[:])
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				return ErrFingerprint
			}
			return nil
		},
	}
	tr := &http.Transport{TLSClientConfig: tlsConf, ForceAttemptHTTP2: true, IdleConnTimeout: 90 * time.Second}
	return &Client{
		s:      s,
		api:    &http.Client{Transport: tr, Timeout: 90 * time.Second},
		stream: &http.Client{Transport: tr},
	}
}

func (c *Client) Name() string { return c.s.Name }

func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, out any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.s.URL+path, bytes.NewReader(nil))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.s.Token)
	resp, err := hc.Do(req)
	if err != nil {
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
