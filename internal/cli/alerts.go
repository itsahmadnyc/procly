package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/itsahmadnyc/procly/internal/alerts"
)

const alertsUsage = `usage:
  procly alerts                                   show what is set up
  procly alerts telegram --token <bot token> --chat <chat id>
  procly alerts slack --webhook <https://hooks.slack.com/...>
  procly alerts test                              send a test message
  procly alerts off [telegram|slack]              stop sending alerts`

func cmdAlerts(argv []string) error {
	if len(argv) == 0 {
		return alertsStatus()
	}
	switch argv[0] {
	case "telegram":
		a, err := parse(argv[1:], flagSet{valued: set("token", "chat")})
		if err != nil {
			return err
		}
		if a.get("token") == "" || a.get("chat") == "" {
			return errors.New("usage: procly alerts telegram --token <bot token> --chat <chat id>\n  Make a bot with @BotFather, send it a message, then find the chat id at https://api.telegram.org/bot<token>/getUpdates")
		}
		return alertsSet(func(c *alerts.Config) {
			c.Telegram = &alerts.Telegram{Token: a.get("token"), ChatID: a.get("chat")}
		}, "telegram")
	case "slack":
		a, err := parse(argv[1:], flagSet{valued: set("webhook")})
		if err != nil {
			return err
		}
		hook := a.get("webhook")
		if u, err := url.Parse(hook); err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.New("usage: procly alerts slack --webhook <https://hooks.slack.com/services/...>")
		}
		return alertsSet(func(c *alerts.Config) { c.Slack = &alerts.Slack{WebhookURL: hook} }, "slack")
	case "test":
		cfg, err := alerts.Load()
		if err != nil {
			return err
		}
		if cfg.Empty() {
			return errors.New("no alerts set up yet\n" + alertsUsage)
		}
		if err := sendTest(cfg); err != nil {
			return err
		}
		fmt.Printf("  %s test message\n", green("sent"))
		return nil
	case "off":
		cfg, err := alerts.Load()
		if err != nil {
			return err
		}
		which := "all"
		if len(argv) > 1 {
			which = argv[1]
		}
		switch which {
		case "all":
			cfg = alerts.Config{}
		case "telegram":
			cfg.Telegram = nil
		case "slack":
			cfg.Slack = nil
		default:
			return errors.New(alertsUsage)
		}
		if err := alerts.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("  %s %s alerts\n", green("turned off"), which)
		return nil
	default:
		return errors.New(alertsUsage)
	}
}

// alertsSet sends a test message with the new settings and saves them only
// if it arrives, so a typo in a token doesn't silently swallow alerts.
func alertsSet(change func(*alerts.Config), channel string) error {
	cfg, err := alerts.Load()
	if err != nil {
		return err
	}
	only := alerts.Config{}
	change(&only)
	if err := sendTest(only); err != nil {
		return fmt.Errorf("%v\n  Nothing was saved; check the values and try again", err)
	}
	change(&cfg)
	if err := alerts.Save(cfg); err != nil {
		return err
	}
	fmt.Printf("  %s %s alerts (test message sent)\n", green("set up"), channel)
	fmt.Println(dim("  You'll hear about crashes, give-ups and failed health checks. No daemon restart needed."))
	return nil
}

func sendTest(cfg alerts.Config) error {
	host, _ := os.Hostname()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return alerts.Send(ctx, cfg, fmt.Sprintf("procly alerts are working on %s.", host))
}

func alertsStatus() error {
	cfg, err := alerts.Load()
	if err != nil {
		return err
	}
	if cfg.Empty() {
		fmt.Println(dim("\n  No alerts set up. procly can message you when a process crashes:\n"))
		for _, l := range strings.Split(alertsUsage, "\n")[2:4] {
			fmt.Println("  " + strings.TrimSpace(l))
		}
		fmt.Println()
		return nil
	}
	fmt.Println()
	if t := cfg.Telegram; t != nil {
		fmt.Printf("  %s  bot %s, chat %s\n", bold("telegram"), alerts.Mask(t.Token), t.ChatID)
	}
	if s := cfg.Slack; s != nil {
		fmt.Printf("  %s     %s\n", bold("slack"), alerts.Mask(s.WebhookURL))
	}
	fmt.Println(dim("\n  Sent on crashes (at most one per process every 5 minutes), give-ups and failed health checks."))
	fmt.Println()
	return nil
}
