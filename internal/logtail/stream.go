package logtail

import (
	"context"
	"time"
)

// Event is one log line on the wire. Stream is "o" (stdout) or "e" (stderr).
type Event struct {
	Stream string `json:"s"`
	Line   string `json:"l"`
}

// Stream sends the last `history` lines of both files (always, even when
// empty), then new lines as they appear, until ctx ends or send returns
// false. Every 15 seconds without output it calls send(nil) so the caller
// can write a keepalive.
func Stream(ctx context.Context, outPath, errPath string, history int, send func([]Event) bool) {
	outTail, outSize := Tail(outPath, history)
	errTail, errSize := Tail(errPath, history)
	first := make([]Event, 0, len(outTail)+len(errTail))
	for _, l := range outTail {
		first = append(first, Event{"o", l})
	}
	for _, l := range errTail {
		first = append(first, Event{"e", l})
	}
	if !send(first) {
		return
	}
	outF, errF := NewFollower(outPath, outSize), NewFollower(errPath, errSize)

	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	idle := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			var batch []Event
			for _, l := range outF.Read() {
				batch = append(batch, Event{"o", l})
			}
			for _, l := range errF.Read() {
				batch = append(batch, Event{"e", l})
			}
			switch {
			case len(batch) > 0:
				idle = time.Now()
				if !send(batch) {
					return
				}
			case time.Since(idle) > 15*time.Second:
				idle = time.Now()
				if !send(nil) {
					return
				}
			}
		}
	}
}
