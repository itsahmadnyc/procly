package proc

import "time"

func init() {
	// Switches wait to see that the new setup works; tests do not need the full wait.
	settleQuiet = 1500 * time.Millisecond
	settleTick = 100 * time.Millisecond
}
