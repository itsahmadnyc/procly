package cli

import (
	"fmt"
	"strings"
)

type flagSet struct {
	valued map[string]bool
	bools  map[string]bool
	alias  map[string]string
}

type args struct {
	values map[string][]string
	bools  map[string]bool
	pos    []string
	rest   []string // everything after --
}

func (a args) get(name string) string {
	if v := a.values[name]; len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// parse accepts flags anywhere among the positional arguments, so
// `procly start app.js --name api` works. Script arguments go after `--`.
func parse(in []string, fs flagSet) (args, error) {
	a := args{values: map[string][]string{}, bools: map[string]bool{}}
	for i := 0; i < len(in); i++ {
		s := in[i]
		if s == "--" {
			a.rest = in[i+1:]
			break
		}
		if len(s) < 2 || s[0] != '-' {
			a.pos = append(a.pos, s)
			continue
		}
		key := strings.TrimLeft(s, "-")
		val, hasVal := "", false
		if k, v, ok := strings.Cut(key, "="); ok {
			key, val, hasVal = k, v, true
		}
		if full, ok := fs.alias[key]; ok {
			key = full
		}
		switch {
		case fs.valued[key]:
			if !hasVal {
				if i+1 >= len(in) {
					return a, fmt.Errorf("flag --%s needs a value", key)
				}
				i++
				val = in[i]
			}
			a.values[key] = append(a.values[key], val)
		case fs.bools[key]:
			a.bools[key] = true
		default:
			return a, fmt.Errorf("unknown flag %s (script arguments go after --)", s)
		}
	}
	return a, nil
}
