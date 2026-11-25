package tui

import "testing"

func TestSpark(t *testing.T) {
	const mb = 1 << 20
	cases := []struct {
		name     string
		vals     []float64
		fromZero bool
		minSpan  float64
		want     string
	}{
		{"cpu idle stays low", []float64{0, 0.5, 0}, true, 10, "   ▁▁▁"},
		{"cpu spike", []float64{0, 10, 5}, true, 10, "   ▁█▅"},
		{"memory wobble stays low", []float64{48.2 * mb, 46.9 * mb, 47.9 * mb}, false, 8 * mb, "   ▂▁▂"},
		{"memory growth fills", []float64{40 * mb, 50 * mb, 60 * mb}, false, 8 * mb, "   ▁▅█"},
	}
	for _, c := range cases {
		if got := spark(c.vals, 6, c.fromZero, c.minSpan); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
