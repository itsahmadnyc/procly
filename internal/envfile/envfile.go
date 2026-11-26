// Package envfile reads environment variables from .env style files.
//
// The syntax is deliberately small: one KEY=value per line, an optional
// "export " in front, # comments, and single or double quotes. Values are never
// expanded (no $VAR) and cannot span lines.
package envfile

import (
	"fmt"
	"os"
	"strings"
)

// Pair is one variable, in file order.
type Pair struct{ Key, Value string }

// Parse reads the contents of one file. name is only used in error messages,
// which carry the line number: "name:3: ...".
func Parse(name string, data []byte) ([]Pair, error) {
	text := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
	var out []Pair
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		p, err := parseLine(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", name, i+1, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func parseLine(line string) (Pair, error) {
	if rest, ok := strings.CutPrefix(line, "export"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
		line = strings.TrimSpace(rest)
	}
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return Pair{}, fmt.Errorf("expected KEY=value, got %q", shorten(line))
	}
	key := strings.TrimSpace(line[:eq])
	if !validKey(key) {
		return Pair{}, fmt.Errorf("%q is not a valid variable name (letters, digits and _, not starting with a digit)", shorten(key))
	}
	value, err := parseValue(strings.TrimLeft(line[eq+1:], " \t"))
	if err != nil {
		return Pair{}, err
	}
	return Pair{key, value}, nil
}

func parseValue(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	switch s[0] {
	case '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", fmt.Errorf("missing closing ' (values cannot span lines)")
		}
		return s[1 : 1+end], afterQuote(s[2+end:])
	case '"':
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			switch c := s[i]; c {
			case '"':
				return b.String(), afterQuote(s[i+1:])
			case '\\':
				if i+1 >= len(s) {
					return "", fmt.Errorf("missing closing \" (values cannot span lines)")
				}
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				case 'r':
					b.WriteByte('\r')
				case 't':
					b.WriteByte('\t')
				case '"', '\\':
					b.WriteByte(s[i])
				default:
					// Not an escape procly knows: keep it as written.
					b.WriteByte('\\')
					b.WriteByte(s[i])
				}
			default:
				b.WriteByte(c)
			}
		}
		return "", fmt.Errorf("missing closing \" (values cannot span lines)")
	}
	// Unquoted: ends at " #" (a comment) or the end of the line.
	if i := strings.Index(s, " #"); i >= 0 {
		s = s[:i]
	} else if i := strings.Index(s, "\t#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimRight(s, " \t"), nil
}

// afterQuote allows only a comment after a closing quote.
func afterQuote(rest string) error {
	rest = strings.TrimSpace(rest)
	if rest == "" || strings.HasPrefix(rest, "#") {
		return nil
	}
	return fmt.Errorf("unexpected text after the closing quote: %q", shorten(rest))
}

func validKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func shorten(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// Load reads the files in order and returns the variables, later files
// overriding earlier ones, together with the names in the order first seen.
func Load(paths []string) (map[string]string, []string, error) {
	vars := map[string]string{}
	var keys []string
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, fmt.Errorf("env_file: %v", err)
		}
		pairs, err := Parse(p, data)
		if err != nil {
			return nil, nil, err
		}
		for _, kv := range pairs {
			if _, seen := vars[kv.Key]; !seen {
				keys = append(keys, kv.Key)
			}
			vars[kv.Key] = kv.Value
		}
	}
	return vars, keys, nil
}
