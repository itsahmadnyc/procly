package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/itsahmadnyc/procly/internal/proc"
)

func TestSafeReplacesWhatTerminalsMeasureDifferently(t *testing.T) {
	cases := map[string]string{
		"plain text":                        "plain text",
		"✅ done":                            "· done",
		"🚀 Starting job scheduler...":       "· Starting job scheduler...",
		"👨‍💻 Developed by: Muhammad":        "· Developed by: Muhammad",
		"⏱️ron heartbeat":                   "·ron heartbeat",
		"⏱ text form stays":                 "⏱ text form stays",
		"👍🏽 skin tone":                      "· skin tone",
		"🇵🇰 flag":                           "· flag",
		"a ✓ b ✗ c → d • e ─":               "a ✓ b ✗ c → d • e ─",
		"📊 Connection Pool: Max 5, Min 0":   "· Connection Pool: Max 5, Min 0",
		"⚡ Ready to handle requests!":       "· Ready to handle requests!",
		"❤️ heart":                          "· heart",
		"日本語 stays":                         "日本語 stays",
		"🚀🚀 two rockets stay two stand-ins": "·· two rockets stay two stand-ins",
	}
	for in, want := range cases {
		if got := safe(in); got != want {
			t.Errorf("safe(%q) = %q, want %q", in, got, want)
		}
	}
}

// Whatever an app prints, every line of the screen is no wider than the
// terminal under both ways of counting an emoji (1 or 2 cells, 4 for a ZWJ
// sequence), which is what makes the screen wrap and scroll.
func TestEmojiLogsNeverOverflowTheScreen(t *testing.T) {
	list := infos(t)
	writeLog(t, list[0].OutLog,
		"🚀 Production-Ready Express API 👨‍💻 Developed by: Muhammad Ahmad 📦 Features: Auth • Upload",
		"✅ SQL Database Connected... 📊 Connection Pool: Max 5, Min 0 ⏱️ cron heartbeat ⚡ ready",
		strings.Repeat("🇵🇰👍🏽 ", 40),
	)
	list[0].Spec.Command = "node 🚀.js --name 👨‍💻"
	for _, size := range [][2]int{{60, 16}, {100, 30}, {140, 40}} {
		m := loadedWith(t, list, size[0], size[1])
		m, _ = send(m, key("z"))
		for _, mode := range []string{"normal", "zoom"} {
			if mode == "normal" {
				m, _ = send(m, key("esc"))
			}
			for i, l := range strings.Split(m.View(), "\n") {
				plain := ansi.Strip(l)
				if strings.ContainsAny(plain, "‍️🚀✅📊⚡👨💻") {
					t.Errorf("%dx%d %s line %d still has an emoji: %q", size[0], size[1], mode, i, plain)
				}
				if w := ansi.StringWidth(plain); w > size[0] {
					t.Errorf("%dx%d %s line %d is %d wide", size[0], size[1], mode, i, w)
				}
			}
		}
	}
	// App-supplied text elsewhere is cleaned too.
	m := loadedWith(t, list, 120, 40)
	m.views[0].Events = []proc.ViewEvent{{Kind: "crashed", Detail: "💥 boom ⏱️"}}
	if v := ansi.Strip(m.View()); strings.Contains(v, "💥") || strings.Contains(v, "️") {
		t.Errorf("event details should be cleaned:\n%s", v)
	}
}

// The case that was reported: the start of an app's log is a banner with
// figlet art, emoji and a joined emoji, and the end is plain. Following the
// tail looked fine; scrolling up to the banner broke the screen, because the
// terminal wrapped lines that procly thought fit, and the header and footer
// scrolled away.
func TestScrollingUpToABannerKeepsTheScreenIntact(t *testing.T) {
	list := infos(t)
	banner := []string{
		" █████╗ ██████╗ ██╗    ██╗  ██╗██╗████████╗",
		"██╔══██╗██╔══██╗██║    ██║ ██╔╝██║╚══██╔══╝",
		"███████║██████╔╝██║    █████╔╝ ██║   ██║   ",
		"================================================================",
		"🚀 Production-Ready Express API with Advanced Features",
		"👨‍💻 Developed by: Muhammad Ahmad",
		"📦 Features: Auth • Upload • Cron Jobs • Pooling • Security",
		"✅ SQL Database Connected...\x07\x08 ⏱️ ⏰ 🕐 🧹",
		"\x1b[2J\x1b]0;title\x07colored \x9b31m C1 control",
	}
	writeLog(t, list[0].OutLog, banner...)
	for i := 0; i < 100; i++ {
		writeLog(t, list[0].OutLog, "2026-11-27T05:00:00Z Declaring auction winner at: 🕐 "+string(rune('A'+i%26)))
	}
	writeLog(t, list[0].ErrLog, "Fri, 27 Nov 2026 04:56:58 GMT morgan deprecated undefined format")

	for _, size := range [][2]int{{70, 18}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		m := loadedWith(t, list, w, h)
		m, _ = send(m, key("z")) // logs full screen
		check := func(step string) {
			t.Helper()
			view := m.View()
			rows := strings.Split(view, "\n")
			if len(rows) != h {
				t.Fatalf("%dx%d %s: %d lines for a %d-line terminal", w, h, step, len(rows), h)
			}
			if !strings.Contains(ansi.Strip(rows[0]), "procly") || !strings.Contains(ansi.Strip(rows[h-1]), "esc back") {
				t.Fatalf("%dx%d %s: header or footer is gone:\n%s", w, h, step, ansi.Strip(view))
			}
			for i, l := range rows {
				plain := ansi.Strip(l)
				if strings.ContainsAny(plain, "\x07\x08\x1b\x9b‍️") {
					t.Fatalf("%dx%d %s: line %d has a control or joiner character: %q", w, h, step, i, plain)
				}
				// Pessimistic width: any emoji left would count as 2 cells each.
				pess := 0
				for _, r := range plain {
					switch {
					case isEmoji(r):
						pess += 2
					case ansi.StringWidth(string(r)) > 0:
						pess += ansi.StringWidth(string(r))
					}
				}
				if pess > w {
					t.Fatalf("%dx%d %s: line %d may be %d wide: %q", w, h, step, i, pess, plain)
				}
			}
		}
		check("following")
		for i := 0; i < 400; i++ { // up with the arrow key, all the way to the banner
			m, _ = send(m, tea.KeyMsg{Type: tea.KeyUp})
			if i%40 == 0 {
				check("scrolling up")
			}
		}
		check("at the banner")
		blank := 0
		for _, l := range strings.Split(ansi.Strip(m.View()), "\n")[1 : h-2] {
			if strings.TrimSpace(strings.Trim(l, "│╭╮╰╯─ ")) == "" {
				blank++
			}
		}
		if blank > 0 {
			t.Errorf("%dx%d: scrolled to the top, the pane has %d empty rows; it should stay full:\n%s", w, h, blank, ansi.Strip(m.View()))
		}
		if !strings.Contains(m.View(), "Production-Ready") && !strings.Contains(m.View(), "Developed by") {
			t.Errorf("%dx%d: the banner should be on screen at the top of the history:\n%s", w, h, ansi.Strip(m.View()))
		}
		for i := 0; i < 400; i++ {
			m, _ = send(m, tea.KeyMsg{Type: tea.KeyDown})
		}
		check("back at the end")
	}
}
