package envfile

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func parse(t *testing.T, text string) map[string]string {
	t.Helper()
	pairs, err := Parse(".env", []byte(text))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	m := map[string]string{}
	for _, p := range pairs {
		m[p.Key] = p.Value
	}
	return m
}

func TestParseValues(t *testing.T) {
	got := parse(t, strings.Join([]string{
		"PORT=3000",
		"  SPACED = value with spaces  ",
		"export TOKEN=abc123",
		"exportFOO=not-an-export",
		"EMPTY=",
		"EMPTY_QUOTED=\"\"",
		"SINGLE='a $b # not a comment'",
		`DOUBLE="line1\nline2 \"quoted\" back\\slash \q"`,
		"URL=https://example.com/a?b=c#frag",
		"COMMENTED=value # a comment",
		"QUOTED_COMMENT=\"v\" # a comment",
		"TABBED=value\t# another",
		"WITH_EQUALS=a=b=c",
		"# a whole-line comment",
		"",
		"   # an indented comment",
		"LAST=1\r",
	}, "\n"))
	want := map[string]string{
		"PORT":           "3000",
		"SPACED":         "value with spaces",
		"TOKEN":          "abc123",
		"exportFOO":      "not-an-export",
		"EMPTY":          "",
		"EMPTY_QUOTED":   "",
		"SINGLE":         "a $b # not a comment",
		"DOUBLE":         "line1\nline2 \"quoted\" back\\slash \\q",
		"URL":            "https://example.com/a?b=c#frag", // a # without a space before it is part of the value
		"COMMENTED":      "value",
		"QUOTED_COMMENT": "v",
		"TABBED":         "value",
		"WITH_EQUALS":    "a=b=c",
		"LAST":           "1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestParseBOMAndCRLF(t *testing.T) {
	got := parse(t, "\xef\xbb\xbfA=1\r\nB=2\r\n")
	if got["A"] != "1" || got["B"] != "2" {
		t.Errorf("got %q", got)
	}
}

func TestParseErrorsNameTheLine(t *testing.T) {
	cases := []struct{ text, want string }{
		{"OK=1\nnot a variable\n", ".env:2: expected KEY=value"},
		{"1BAD=x", ".env:1: \"1BAD\" is not a valid variable name"},
		{"A=1\n\nBAD-KEY=x", ".env:3: \"BAD-KEY\" is not a valid variable name"},
		{"=value", ".env:1: \"\" is not a valid variable name"},
		{"A='unterminated", ".env:1: missing closing '"},
		{"A=\"unterminated", ".env:1: missing closing \""},
		{"A=\"ends with backslash\\", ".env:1: missing closing \""},
		{"A=\"x\" y", ".env:1: unexpected text after the closing quote"},
		{"export", ".env:1: expected KEY=value"},
	}
	for _, c := range cases {
		_, err := Parse(".env", []byte(c.text))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: error %v, want it to contain %q", c.text, err, c.want)
		}
	}
}

func TestLoadOrderAndNames(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.env"), filepath.Join(dir, "b.env")
	os.WriteFile(a, []byte("ONE=a\nTWO=a\n"), 0o600)
	os.WriteFile(b, []byte("TWO=b\nTHREE=b\n"), 0o600)
	vars, keys, err := Load([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if vars["ONE"] != "a" || vars["TWO"] != "b" || vars["THREE"] != "b" {
		t.Errorf("later files must override earlier ones: %v", vars)
	}
	if !reflect.DeepEqual(keys, []string{"ONE", "TWO", "THREE"}) {
		t.Errorf("names in the order first seen: %v", keys)
	}
	if _, _, err := Load([]string{filepath.Join(dir, "missing.env")}); err == nil || !strings.Contains(err.Error(), "env_file") {
		t.Errorf("a missing file: %v", err)
	}
	os.WriteFile(b, []byte("TWO=b\nbroken\n"), 0o600)
	if _, _, err := Load([]string{a, b}); err == nil || !strings.Contains(err.Error(), "b.env:2:") {
		t.Errorf("a bad line must name the file and line: %v", err)
	}
}
