package logtail

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, s string, appendTo bool) {
	t.Helper()
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendTo {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(path, flag, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.log")
	write(t, path, "one\ntwo\nthree\nfour\n", false)

	lines, size := Tail(path, 2)
	if strings.Join(lines, ",") != "three,four" || size != int64(len("one\ntwo\nthree\nfour\n")) {
		t.Fatalf("got %v size %d", lines, size)
	}
	if all, _ := Tail(path, 100); len(all) != 4 {
		t.Fatalf("asking for more than exists: %v", all)
	}
	if none, _ := Tail(path, 0); len(none) != 0 {
		t.Fatalf("n=0: %v", none)
	}
	if missing, size := Tail(filepath.Join(t.TempDir(), "nope"), 5); missing != nil || size != 0 {
		t.Fatalf("missing file: %v %d", missing, size)
	}
	empty := filepath.Join(t.TempDir(), "empty.log")
	write(t, empty, "", false)
	if lines, _ := Tail(empty, 5); len(lines) != 0 {
		t.Fatalf("empty file: %v", lines)
	}
}

// Big files are only read from the end; the line cut in half at the start
// of that window must not be returned.
func TestTailOfBigFileDropsPartialFirstLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.log")
	var b strings.Builder
	for i := 0; i < 40000; i++ {
		b.WriteString("line-")
		b.WriteString(strings.Repeat("x", 10))
		b.WriteString("\n")
	}
	b.WriteString("last\n")
	write(t, path, b.String(), false)

	lines, _ := Tail(path, 1_000_000)
	if lines[len(lines)-1] != "last" {
		t.Fatalf("last line: %q", lines[len(lines)-1])
	}
	for _, l := range lines {
		if l != "last" && l != "line-"+strings.Repeat("x", 10) {
			t.Fatalf("a cut-off line came back: %q", l)
		}
	}
}

func TestFollower(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.log")
	write(t, path, "old\n", false)
	_, size := Tail(path, 10)
	f := NewFollower(path, size)

	if got := f.Read(); len(got) != 0 {
		t.Fatalf("nothing new yet: %v", got)
	}
	write(t, path, "a\nb", true) // "b" has no newline yet
	if got := f.Read(); strings.Join(got, ",") != "a" {
		t.Fatalf("complete lines only: %v", got)
	}
	write(t, path, "c\n", true)
	if got := f.Read(); strings.Join(got, ",") != "bc" {
		t.Fatalf("partial line should be joined: %v", got)
	}

	// Truncation (log rotation) starts over from the top.
	write(t, path, "fresh\n", false)
	if got := f.Read(); strings.Join(got, ",") != "fresh" {
		t.Fatalf("after truncation: %v", got)
	}

	gone := NewFollower(filepath.Join(t.TempDir(), "nope"), 0)
	if got := gone.Read(); got != nil {
		t.Fatalf("missing file: %v", got)
	}
}

func TestStream(t *testing.T) {
	dir := t.TempDir()
	out, errPath := filepath.Join(dir, "out.log"), filepath.Join(dir, "err.log")
	write(t, out, "o1\no2\no3\n", false)
	write(t, errPath, "e1\n", false)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	batches := make(chan []Event, 10)
	done := make(chan struct{})
	go func() {
		Stream(ctx, out, errPath, 2, func(b []Event) bool { batches <- b; return true })
		close(done)
	}()

	first := <-batches
	got := []string{}
	for _, e := range first {
		got = append(got, e.Stream+":"+e.Line)
	}
	if strings.Join(got, ",") != "o:o2,o:o3,e:e1" {
		t.Fatalf("history: %v", got)
	}

	write(t, errPath, "e2\n", true)
	select {
	case b := <-batches:
		if len(b) != 1 || b[0] != (Event{"e", "e2"}) {
			t.Fatalf("new line: %v", b)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("new line not streamed")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not stop when the context ended")
	}
}

func TestStreamSendsEmptyHistoryAndStopsWhenSendFails(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	var first []Event
	done := make(chan struct{})
	go func() {
		Stream(context.Background(), filepath.Join(dir, "none.out"), filepath.Join(dir, "none.err"), 10, func(b []Event) bool {
			calls++
			first = b
			return false // the client went away
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stream kept going after send returned false")
	}
	if calls != 1 || first == nil || len(first) != 0 {
		t.Fatalf("history must be sent once, as an empty (not nil) batch: calls=%d first=%#v", calls, first)
	}
}
