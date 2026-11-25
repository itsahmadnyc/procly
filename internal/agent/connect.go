package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/net/http2"

	"github.com/itsahmadnyc/procly/internal/client"
	"github.com/itsahmadnyc/procly/internal/hub"
	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/remote"
	"github.com/itsahmadnyc/procly/internal/tlsid"
	"github.com/itsahmadnyc/procly/internal/version"
)

// JoinConfig is what `procly agent join` saves (agent-connect.json, mode
// 0600), so `procly agent connect` runs without secrets on its command line.
type JoinConfig struct {
	Hub         string `json:"hub"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Token       string `json:"token"`
}

func joinPath() string { return filepath.Join(paths.Home(), "agent-connect.json") }

// ErrRejected means the hub answered but refused this agent.
type ErrRejected struct{ Reason string }

func (e *ErrRejected) Error() string { return "the hub refused this agent: " + e.Reason }

// Join checks that the hub accepts cfg, then saves it.
func Join(cfg JoinConfig) error {
	fp, err := tlsid.Normalize(cfg.Fingerprint)
	if err != nil {
		return err
	}
	cfg.Fingerprint = fp
	if err := remote.ValidName(cfg.Name); err != nil {
		return err
	}
	if _, _, err := net.SplitHostPort(cfg.Hub); err != nil {
		return fmt.Errorf("hub address must look like host:%s, got %q", hub.DefaultPort, cfg.Hub)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialHub(ctx, cfg, true)
	if err != nil {
		return err
	}
	conn.Close()

	if err := paths.Ensure(); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	tmp := joinPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, joinPath())
}

func loadJoin() (JoinConfig, error) {
	var cfg JoinConfig
	data, err := os.ReadFile(joinPath())
	if errors.Is(err, os.ErrNotExist) {
		return cfg, errors.New("this server hasn't joined a hub yet; run 'procly agent join' with the command 'procly server invite' printed on the hub")
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("%s is damaged: %w", joinPath(), err)
	}
	return cfg, nil
}

// dialHub opens TLS to the hub (pinned), introduces this agent and waits
// for the hub's OK. The returned connection is ready for HTTP/2.
func dialHub(ctx context.Context, cfg JoinConfig, probe bool) (*tls.Conn, error) {
	d := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second},
		Config:    tlsid.Pinned(cfg.Fingerprint),
	}
	c, err := d.DialContext(ctx, "tcp", cfg.Hub)
	if err != nil {
		if errors.Is(err, tlsid.ErrMismatch) {
			return nil, tlsid.ErrMismatch
		}
		return nil, fmt.Errorf("can't reach the hub at %s: %w", cfg.Hub, err)
	}
	conn := c.(*tls.Conn)
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	host, _ := os.Hostname()
	hello, _ := json.Marshal(hub.Hello{Name: cfg.Name, Token: cfg.Token, Host: host, Version: version.Version, Probe: probe})
	if _, err := conn.Write(append(hello, '\n')); err != nil {
		conn.Close()
		return nil, err
	}
	line, err := readLine(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("the hub closed the connection: %w", err)
	}
	var reply hub.Reply
	if err := json.Unmarshal(line, &reply); err != nil {
		conn.Close()
		return nil, errors.New("the hub sent something unexpected; is that a procly hub port?")
	}
	if !reply.OK {
		conn.Close()
		return nil, &ErrRejected{Reason: reply.Error}
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func readLine(r io.Reader) ([]byte, error) {
	var line []byte
	b := make([]byte, 1)
	for len(line) < 4096 {
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		if b[0] == '\n' {
			return line, nil
		}
		line = append(line, b[0])
	}
	return nil, errors.New("reply too long")
}

// Connect keeps this agent connected to its hub, reconnecting with backoff,
// until SIGINT/SIGTERM.
func Connect(out io.Writer) error {
	cfg, err := loadJoin()
	if err != nil {
		return err
	}
	if out == nil {
		out = os.Stdout
	}
	logger := log.New(out, "", log.LstdFlags)
	host, _ := os.Hostname()
	a := &agent{c: client.New(), host: host, logger: logger}
	handler := a.handlers()
	srv := &http2.Server{
		// Pings notice a hub that disappeared without closing the connection.
		ReadIdleTimeout: 30 * time.Second,
		PingTimeout:     15 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		cancel()
	}()

	logger.Printf("procly agent %s connecting to hub %s", cfg.Name, cfg.Hub)
	delay := time.Second
	for ctx.Err() == nil {
		// Re-read settings every attempt, so a new 'procly agent join' takes
		// effect without restarting this process.
		if fresh, err := loadJoin(); err == nil && fresh != cfg {
			cfg = fresh
			logger.Printf("join settings changed; now connecting to %s as %s", cfg.Hub, cfg.Name)
		}
		dctx, dcancel := context.WithTimeout(ctx, 15*time.Second)
		conn, err := dialHub(dctx, cfg, false)
		dcancel()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			wait := delay
			var rej *ErrRejected
			if errors.As(err, &rej) || errors.Is(err, tlsid.ErrMismatch) {
				wait = 30 * time.Second // needs a human; don't hammer the hub
			}
			logger.Printf("%v; retrying in %s", err, wait)
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
			delay = min(delay*2, 30*time.Second)
			continue
		}
		logger.Printf("connected to hub %s", cfg.Hub)
		delay = time.Second
		connected := time.Now()

		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				conn.Close()
			case <-done:
			}
		}()
		srv.ServeConn(conn, &http2.ServeConnOpts{Context: ctx, Handler: handler, BaseConfig: &http.Server{}})
		close(done)
		conn.Close()
		if ctx.Err() == nil {
			logger.Printf("disconnected from hub after %s; reconnecting", time.Since(connected).Round(time.Second))
			time.Sleep(time.Second)
		}
	}
	logger.Printf("stopped")
	return nil
}
