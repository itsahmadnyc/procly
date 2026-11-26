package proc

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"time"
)

// Health of a process that has a health check. Empty means no check.
type Health string

const (
	HealthStarting  Health = "starting" // inside the grace period, not checked yet
	HealthHealthy   Health = "healthy"
	HealthUnhealthy Health = "unhealthy"
)

// Event is something worth telling a person about, such as a crash.
type Event struct {
	Kind     string        `json:"kind"` // crashed, errored, unhealthy
	Process  string        `json:"process"`
	ExitCode int           `json:"exit_code,omitempty"`
	Restarts int           `json:"restarts"`
	Retry    time.Duration `json:"retry,omitempty"`  // crashed: when the next start happens
	Detail   string        `json:"detail,omitempty"` // last stderr lines, or the failed check
}

const (
	EventCrashed   = "crashed"
	EventErrored   = "errored"
	EventUnhealthy = "unhealthy"
)

func validHealthTarget(target string) error {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return fmt.Errorf("health must look like http://127.0.0.1:3000/health or tcp://127.0.0.1:6379, got %q", target)
	}
	switch u.Scheme {
	case "http", "https", "tcp":
		return nil
	}
	return fmt.Errorf("health check scheme must be http, https or tcp, got %q", u.Scheme)
}

var healthClient = &http.Client{
	// Don't follow redirects to other hosts; a 3xx already means "up".
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// checkHealth runs one check against target.
func checkHealth(ctx context.Context, target string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	u, err := url.Parse(target)
	if err != nil {
		return err
	}
	if u.Scheme == "tcp" {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", u.Host)
		if err != nil {
			return fmt.Errorf("tcp %s: %w", u.Host, err)
		}
		return conn.Close()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "procly-health")
	resp, err := healthClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", target, err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("GET %s: HTTP %d", target, resp.StatusCode)
	}
	return nil
}

// healthLoop checks one run of the process until it exits. After the grace
// period it checks every interval; `retries` failures in a row restart it.
func (p *Process) healthLoop(cmd *exec.Cmd, exited chan struct{}, spec Spec) {
	grace, interval, timeout, retries := spec.healthSettings()
	select {
	case <-exited:
		return
	case <-time.After(grace):
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		err := checkHealth(context.Background(), spec.Health, timeout)

		p.mu.Lock()
		if p.cmd != cmd || p.stopping {
			p.mu.Unlock()
			return
		}
		if err == nil {
			if p.health == HealthUnhealthy {
				p.logger.Printf("%s: health check passing again", spec.Name)
			}
			p.health, p.healthFails, p.healthErr = HealthHealthy, 0, ""
		} else {
			p.healthFails++
			p.healthErr = err.Error()
			if p.healthFails >= retries {
				p.health = HealthUnhealthy
				ev := Event{Kind: EventUnhealthy, Process: spec.Name, Restarts: p.restarts + 1,
					Detail: fmt.Sprintf("%d failed checks in a row, last: %v", p.healthFails, err)}
				p.mu.Unlock()
				p.logger.Printf("%s: unhealthy after %d failed checks (%v), restarting", spec.Name, retries, err)
				p.emit(ev)
				go func() { _ = p.Restart() }()
				return
			}
		}
		p.mu.Unlock()

		select {
		case <-exited:
			return
		case <-tick.C:
		}
	}
}

func (p *Process) emit(e Event) {
	if p.events != nil {
		go p.events(e)
	}
}
