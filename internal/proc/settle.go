package proc

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// settleTick is how often a switch looks at the app it just started; tests
// shorten it.
var settleTick = 500 * time.Millisecond

// settleQuiet is how long a started app must stay up, with no worker or
// process dying, before a switch counts it as working.
var settleQuiet = 6 * time.Second

// settle watches an app that was just started in a new setup and says whether
// it works: it must stay up (and, in cluster mode, have all its workers up and
// listening) for settleQuiet without anything dying. It returns the reason when
// it does not, which is what makes a switch that "started" but then crashed
// an error that is rolled back, instead of a crash loop nobody asked for.
func (m *Manager) settle(name string, s Spec) error {
	limit := 20 * time.Second
	if s.isCluster() {
		lt := time.Duration(s.ListenTimeoutMs) * time.Millisecond
		if lt <= 0 {
			lt = 15 * time.Second
		}
		limit = min(lt*time.Duration(s.Workers())+15*time.Second, 3*time.Minute)
	}
	deadline := time.Now().Add(limit)
	var okSince time.Time
	lastRestarts := -1
	first := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(settleTick)
		in, err := m.Get(name)
		if err != nil {
			return fmt.Errorf("it disappeared while starting: %w", err)
		}
		if in.Status != StatusOnline {
			return fmt.Errorf("it %s right after starting%s", map[Status]string{StatusWaiting: "crashed", StatusErrored: "crashed over and over", StatusStopped: "stopped"}[in.Status]+"", m.whyFailed(in))
		}
		restarts := in.Restarts
		good := true
		if s.isCluster() {
			if in.Cluster == nil {
				good = false // the launcher has not reported yet
			} else {
				want := s.Workers()
				listening, up := in.Cluster.live()
				for _, w := range in.Cluster.Workers {
					restarts += w.Restarts
				}
				good = up == want && (in.Cluster.Ready && listening > 0 || time.Since(first) > 10*time.Second)
			}
		}
		if lastRestarts >= 0 && restarts > lastRestarts {
			return fmt.Errorf("it keeps crashing: a worker died after starting%s", m.whyFailed(in))
		}
		lastRestarts = restarts
		if !good {
			okSince = time.Time{}
			continue
		}
		if okSince.IsZero() {
			okSince = time.Now()
		}
		if time.Since(okSince) >= settleQuiet {
			return nil
		}
	}
	in, _ := m.Get(name)
	if s.isCluster() && in.Cluster != nil {
		_, up := in.Cluster.live()
		return fmt.Errorf("only %d of %d workers were up after %s%s", up, s.Workers(), limit.Round(time.Second), m.whyFailed(in))
	}
	return fmt.Errorf("it did not settle within %s%s", limit.Round(time.Second), m.whyFailed(in))
}

// whyFailed is the last few lines the app wrote to its error log, to show a
// person what went wrong.
func (m *Manager) whyFailed(in Info) string {
	lines := tailLines(in.ErrLog, 4, 4096)
	if len(lines) == 0 {
		return ""
	}
	return ". Its last error output: " + strings.Join(lines, " | ")
}

// tailLines reads the last n non-empty lines from the end of a file.
func tailLines(path string, n int, maxBytes int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	off := max(fi.Size()-maxBytes, 0)
	buf := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && len(buf) == 0 {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(buf), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}
