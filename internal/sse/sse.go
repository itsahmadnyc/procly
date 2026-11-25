// Package sse writes and reads the server-sent event stream procly uses
// for logs, between agent and panel and between panel and browser.
package sse

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/itsahmadnyc/procly/internal/logtail"
)

// Writer prepares w for streaming and returns a send func: a nil batch writes
// a keepalive comment, anything else a data event. ok is false if w can't flush.
func Writer(w http.ResponseWriter) (send func([]logtail.Event) bool, ok bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	return func(batch []logtail.Event) bool {
		var err error
		if batch == nil {
			_, err = io.WriteString(w, ": ping\n\n")
		} else {
			data, _ := json.Marshal(batch)
			_, err = fmt.Fprintf(w, "data: %s\n\n", data)
		}
		if err != nil {
			return false
		}
		flusher.Flush()
		return true
	}, true
}

// Read parses an event stream written by Writer and calls fn for every data
// event and (with nil) every keepalive, until the stream ends or fn returns false.
func Read(r io.Reader, fn func([]logtail.Event) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "data: "):
			batch := []logtail.Event{}
			if err := json.Unmarshal([]byte(line[len("data: "):]), &batch); err != nil {
				return fmt.Errorf("bad event: %w", err)
			}
			if !fn(batch) {
				return nil
			}
		case strings.HasPrefix(line, ":"):
			if !fn(nil) {
				return nil
			}
		}
	}
	return sc.Err()
}
