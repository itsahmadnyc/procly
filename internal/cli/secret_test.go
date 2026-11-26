package cli

import (
	"strings"
	"testing"
)

func TestSecret(t *testing.T) {
	flags := flagSet{valued: set("token")}
	parseArgs := func(in ...string) args {
		a, err := parse(in, flags)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	t.Setenv("PROCLY_TEST_SECRET", "from-env")

	cases := []struct {
		name  string
		argv  []string
		stdin string
		want  string
		fail  string
	}{
		{"the flag, as before", []string{"--token", "from-flag"}, "", "from-flag", ""},
		{"the flag wins over the environment", []string{"--token", "from-flag"}, "", "from-flag", ""},
		{"no flag, the environment", nil, "", "from-env", ""},
		{"- reads the first line of stdin", []string{"--token", "-"}, "  from-stdin  \nsecond line\n", "from-stdin", ""},
		{"- reads a last line without a newline", []string{"--token", "-"}, "no-newline", "no-newline", ""},
		{"- ignores the environment", []string{"--token", "-"}, "stdin-wins\n", "stdin-wins", ""},
		{"- with nothing on stdin is an error", []string{"--token", "-"}, "", "", "nothing on standard input"},
		{"- with a blank line is an error", []string{"--token", "-"}, "\n", "", "nothing on standard input"},
	}
	for _, c := range cases {
		got, err := secret(parseArgs(c.argv...), "token", "PROCLY_TEST_SECRET", strings.NewReader(c.stdin))
		if c.fail != "" {
			if err == nil || !strings.Contains(err.Error(), c.fail) {
				t.Errorf("%s: error %v, want %q", c.name, err, c.fail)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}

	t.Setenv("PROCLY_TEST_SECRET", "")
	if got, err := secret(parseArgs(), "token", "PROCLY_TEST_SECRET", strings.NewReader("")); err != nil || got != "" {
		t.Errorf("nothing given: %q, %v (the caller prints its usage message)", got, err)
	}
}
