// Package alerts sends crash and health notifications to Telegram and Slack.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/itsahmadnyc/procly/internal/paths"
	"github.com/itsahmadnyc/procly/internal/proc"
)

// Config is stored in ~/.procly/alerts.json (mode 0600): it holds a bot
// token and a webhook URL, both of which let anyone post as you.
type Config struct {
	Telegram *Telegram `json:"telegram,omitempty"`
	Slack    *Slack    `json:"slack,omitempty"`
}

type Telegram struct {
	Token  string `json:"token"`
	ChatID string `json:"chat_id"`
}

type Slack struct {
	WebhookURL string `json:"webhook_url"`
}

func (c Config) Empty() bool { return c.Telegram == nil && c.Slack == nil }

func Path() string { return filepath.Join(paths.Home(), "alerts.json") }

func Load() (Config, error) {
	var c Config
	data, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("alerts.json is damaged: %w", err)
	}
	return c, nil
}

func Save(c Config) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(c, "", "  ")
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path())
}

// Endpoints, variables so tests can point them at a local server.
var (
	telegramAPI = "https://api.telegram.org"
	httpClient  = &http.Client{Timeout: 10 * time.Second}
)

// Send posts text to every configured channel and returns the errors joined.
func Send(ctx context.Context, c Config, text string) error {
	var errs []error
	if t := c.Telegram; t != nil {
		body, _ := json.Marshal(map[string]any{"chat_id": t.ChatID, "text": text, "disable_web_page_preview": true})
		if err := post(ctx, telegramAPI+"/bot"+t.Token+"/sendMessage", body); err != nil {
			errs = append(errs, fmt.Errorf("telegram: %w", err))
		}
	}
	if s := c.Slack; s != nil {
		body, _ := json.Marshal(map[string]string{"text": text})
		if err := post(ctx, s.WebhookURL, body); err != nil {
			errs = append(errs, fmt.Errorf("slack: %w", err))
		}
	}
	return errors.Join(errs...)
}

func post(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return redact(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return redact(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// redact keeps bot tokens and webhook paths out of error messages and logs:
// Go's URL errors include the full URL.
func redact(err error) error {
	msg := err.Error()
	if i := strings.Index(msg, "/bot"); i >= 0 {
		if j := strings.Index(msg[i:], "/sendMessage"); j > 0 {
			msg = msg[:i] + "/bot<token>" + msg[i+j:]
		}
	}
	if i := strings.Index(msg, "hooks.slack.com/services/"); i >= 0 {
		end := strings.IndexAny(msg[i:], "\" ")
		if end < 0 {
			end = len(msg) - i
		}
		msg = msg[:i] + "hooks.slack.com/services/<redacted>" + msg[i+end:]
	}
	return errors.New(msg)
}

// Throttle windows: a crash loop or a flapping health check sends one alert
// per window per process; giving up is always sent.
var throttle = map[string]time.Duration{
	proc.EventCrashed:   5 * time.Minute,
	proc.EventUnhealthy: 5 * time.Minute,
	proc.EventErrored:   0,
}

// Notifier turns process events into messages. It re-reads alerts.json for
// every event, so `procly alerts ...` takes effect without a daemon restart.
type Notifier struct {
	host   string
	logger *log.Logger
	now    func() time.Time

	mu         sync.Mutex
	last       map[string]time.Time
	suppressed map[string]int
}

func NewNotifier(host string, logger *log.Logger) *Notifier {
	return &Notifier{host: host, logger: logger, now: time.Now, last: map[string]time.Time{}, suppressed: map[string]int{}}
}

// Handle is meant for proc.Manager.OnEvent.
func (n *Notifier) Handle(e proc.Event) {
	cfg, err := Load()
	if err != nil {
		n.logger.Printf("alerts: %v", err)
		return
	}
	if cfg.Empty() {
		return
	}
	key := e.Process + "/" + e.Kind
	n.mu.Lock()
	window := throttle[e.Kind]
	if last, ok := n.last[key]; ok && window > 0 && n.now().Sub(last) < window {
		n.suppressed[key]++
		n.mu.Unlock()
		return
	}
	skipped := n.suppressed[key]
	n.last[key], n.suppressed[key] = n.now(), 0
	n.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := Send(ctx, cfg, Format(n.host, e, skipped)); err != nil {
		n.logger.Printf("alerts: %v", err)
	}
}

// Format writes the message for one event.
func Format(host string, e proc.Event, skipped int) string {
	var b strings.Builder
	switch e.Kind {
	case proc.EventCrashed:
		fmt.Fprintf(&b, "%s crashed on %s (exit %d). Restarting in %s, restart #%d.", e.Process, host, e.ExitCode, e.Retry.Round(time.Second), e.Restarts)
	case proc.EventErrored:
		fmt.Fprintf(&b, "%s stopped on %s (exit %d) and procly gave up restarting it. Check it with: procly logs %s", e.Process, host, e.ExitCode, e.Process)
	case proc.EventUnhealthy:
		fmt.Fprintf(&b, "%s failed its health check on %s and was restarted.", e.Process, host)
	default:
		fmt.Fprintf(&b, "%s: %s on %s", e.Process, e.Kind, host)
	}
	if skipped > 0 {
		fmt.Fprintf(&b, " (%d more like this in the last few minutes)", skipped)
	}
	if d := strings.TrimSpace(e.Detail); d != "" {
		if len(d) > 1500 {
			d = "…" + d[len(d)-1500:]
		}
		b.WriteString("\n\n" + d)
	}
	return b.String()
}

// Mask shows enough of a secret to recognize it.
func Mask(s string) string {
	if len(s) <= 10 {
		return strings.Repeat("•", len(s))
	}
	return s[:4] + "…" + s[len(s)-3:]
}
