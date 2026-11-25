package cli

import (
	"testing"

	"github.com/itsahmadnyc/procly/internal/logtail"
	"github.com/itsahmadnyc/procly/internal/proc"
)

func TestLastPerStream(t *testing.T) {
	batch := []logtail.Event{
		{Stream: "o", Line: "o1"}, {Stream: "o", Line: "o2"}, {Stream: "o", Line: "o3"},
		{Stream: "e", Line: "e1"}, {Stream: "e", Line: "e2"},
	}
	got := lastPerStream(batch, 2)
	want := []string{"o2", "o3", "e1", "e2"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i, e := range got {
		if e.Line != want[i] {
			t.Fatalf("line %d: got %q want %q", i, e.Line, want[i])
		}
	}
	if len(lastPerStream(batch, 0)) != 0 {
		t.Fatal("n=0 should keep nothing")
	}
}

func TestFindView(t *testing.T) {
	views := []proc.View{{ID: 0, Name: "api"}, {ID: 1, Name: "2"}, {ID: 2, Name: "web"}}
	cases := map[string]string{
		"api": "api",
		"web": "web",
		"2":   "2", // a process literally named "2" wins over id 2
		"0":   "api",
	}
	for target, want := range cases {
		v, ok := findView(views, target)
		if !ok || v.Name != want {
			t.Errorf("%q: got %q, %v; want %q", target, v.Name, ok, want)
		}
	}
	if _, ok := findView(views, "ghost"); ok {
		t.Error("ghost should not match")
	}
}

func TestPickServer(t *testing.T) {
	t.Setenv("PROCLY_SERVER", "")
	a, _ := parse([]string{"--server", "prod-2"}, withServer(flagSet{}))
	if pickServer(a) != "prod-2" {
		t.Fatal("--server not picked up")
	}
	a, _ = parse([]string{"-S", "local"}, withServer(flagSet{}))
	if pickServer(a) != "" {
		t.Fatal("local should mean this machine")
	}
	t.Setenv("PROCLY_SERVER", "prod-3")
	a, _ = parse(nil, withServer(flagSet{}))
	if pickServer(a) != "prod-3" {
		t.Fatal("PROCLY_SERVER not used as default")
	}
	a, _ = parse([]string{"--server=prod-2"}, withServer(flagSet{}))
	if pickServer(a) != "prod-2" {
		t.Fatal("flag should beat PROCLY_SERVER")
	}
}
