package tui

import "strings"

// Terminals disagree on how wide an emoji is. A "✅" or "🚀" is two cells
// almost everywhere, but "⏱️" (a text symbol plus a variation selector) and
// "👨‍💻" (two emoji joined by a zero-width joiner) are 1, 2 or 4 cells
// depending on the terminal and its Unicode tables. procly cuts every line
// to the terminal's width using its own idea of the width, so when the
// terminal measures wider, the line wraps, the screen scrolls by a line, and
// each redraw leaves copies of the headings behind.
//
// Text from the apps (logs, commands, error messages) is therefore drawn
// without emoji: each becomes a "·". Plain symbols that every terminal
// draws one cell wide (✓ ✗ → • ─) are left alone.

const emojiStandIn = '·'

// emojiPresentation lists the characters below U+1F000 that are drawn as
// emoji (two cells wide) by default: Unicode's Emoji_Presentation property.
var emojiPresentation = [][2]rune{
	{0x231A, 0x231B}, {0x23E9, 0x23EC}, {0x23F0, 0x23F0}, {0x23F3, 0x23F3},
	{0x25FD, 0x25FE}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267F, 0x267F},
	{0x2693, 0x2693}, {0x26A1, 0x26A1}, {0x26AA, 0x26AB}, {0x26BD, 0x26BE},
	{0x26C4, 0x26C5}, {0x26CE, 0x26CE}, {0x26D4, 0x26D4}, {0x26EA, 0x26EA},
	{0x26F2, 0x26F3}, {0x26F5, 0x26F5}, {0x26FA, 0x26FA}, {0x26FD, 0x26FD},
	{0x2705, 0x2705}, {0x270A, 0x270B}, {0x2728, 0x2728}, {0x274C, 0x274C},
	{0x274E, 0x274E}, {0x2753, 0x2755}, {0x2757, 0x2757}, {0x2795, 0x2797},
	{0x27B0, 0x27B0}, {0x27BF, 0x27BF}, {0x2B1B, 0x2B1C}, {0x2B50, 0x2B50},
	{0x2B55, 0x2B55},
}

func isEmoji(r rune) bool {
	if r >= 0x1F000 && r <= 0x1FAFF { // pictographs, symbols, flags (regional indicators)
		return true
	}
	for _, p := range emojiPresentation {
		if r >= p[0] && r <= p[1] {
			return true
		}
	}
	return false
}

// invisible marks runes that take no cells of their own but change how the
// ones around them are measured: variation selectors, the zero-width joiner,
// the skin tone modifiers and the tag characters of flag sequences.
func invisible(r rune) bool {
	switch {
	case r == 0x200D, r == 0x200B, r == 0x2060, r == 0xFEFF:
		return true
	case r >= 0xFE00 && r <= 0xFE0F:
		return true
	case r >= 0x1F3FB && r <= 0x1F3FF:
		return true
	case r >= 0xE0020 && r <= 0xE007F:
		return true
	}
	return false
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// safe returns s with emoji replaced by a one-cell stand-in. Emoji joined
// into one picture ("👨‍💻", a flag, a hand with a skin tone) become a single
// stand-in, and so does a symbol that asks to be drawn as an emoji ("⏱️").
func safe(s string) string {
	plain := true
	for _, r := range s {
		if r > 0x2000 {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	rs := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	joinNext := false // a joiner was just seen: the next emoji belongs to the same picture
	riOpen := false   // one half of a flag was just written
	for i, r := range rs {
		switch {
		case r == 0x200D:
			joinNext = true
		case invisible(r):
			// dropped
		case isEmoji(r) || (r > 0x2000 && i+1 < len(rs) && rs[i+1] == 0xFE0F):
			switch {
			case joinNext:
				joinNext = false
			case isRegionalIndicator(r) && riOpen:
				riOpen = false
			default:
				b.WriteRune(emojiStandIn)
				riOpen = isRegionalIndicator(r)
			}
		default:
			joinNext, riOpen = false, false
			b.WriteRune(r)
		}
	}
	return b.String()
}
