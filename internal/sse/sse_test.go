package sse

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/itsahmadnyc/procly/internal/logtail"
)

func TestWriteThenRead(t *testing.T) {
	rec := httptest.NewRecorder()
	send, ok := Writer(rec)
	if !ok {
		t.Fatal("recorder supports flushing")
	}
	send([]logtail.Event{{Stream: "o", Line: "hello"}, {Stream: "e", Line: `quote " and <tag>`}})
	send(nil) // keepalive
	send([]logtail.Event{})

	if rec.Header().Get("Content-Type") != "text/event-stream" || rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers: %v", rec.Header())
	}
	if !rec.Flushed {
		t.Fatal("events must be flushed as they are written")
	}

	var got [][]logtail.Event
	keepalives := 0
	err := Read(strings.NewReader(rec.Body.String()), func(b []logtail.Event) bool {
		if b == nil {
			keepalives++
		} else {
			got = append(got, b)
		}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if keepalives != 1 || len(got) != 2 || len(got[0]) != 2 || got[0][1].Line != `quote " and <tag>` || len(got[1]) != 0 {
		t.Fatalf("round trip: keepalives=%d got=%v", keepalives, got)
	}
}

func TestReadStopsWhenAsked(t *testing.T) {
	stream := "data: [{\"s\":\"o\",\"l\":\"1\"}]\n\ndata: [{\"s\":\"o\",\"l\":\"2\"}]\n\n"
	calls := 0
	if err := Read(strings.NewReader(stream), func([]logtail.Event) bool { calls++; return false }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("Read continued after fn returned false (%d calls)", calls)
	}
}

func TestReadRejectsBadEvents(t *testing.T) {
	if err := Read(strings.NewReader("data: {not json\n\n"), func([]logtail.Event) bool { return true }); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestReadHandlesLongLines(t *testing.T) {
	long := strings.Repeat("x", 200_000)
	stream := `data: [{"s":"o","l":"` + long + `"}]` + "\n\n"
	var line string
	if err := Read(strings.NewReader(stream), func(b []logtail.Event) bool { line = b[0].Line; return true }); err != nil {
		t.Fatal(err)
	}
	if len(line) != len(long) {
		t.Fatalf("long log line cut to %d bytes", len(line))
	}
}

type noFlush struct{ http.ResponseWriter }

func TestWriterNeedsFlusher(t *testing.T) {
	if _, ok := Writer(noFlush{httptest.NewRecorder()}); ok {
		t.Fatal("a writer that can't flush can't stream")
	}
}
