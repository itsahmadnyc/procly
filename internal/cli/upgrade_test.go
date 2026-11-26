package cli

import "testing"

func TestNeedsBinary(t *testing.T) {
	for _, c := range []struct {
		name                  string
		force                 bool
		want, latest, current string
		need                  bool
	}{
		{"older than the latest", false, "", "0.4.0", "0.3.1", true},
		{"the latest already", false, "", "0.4.0", "0.4.0", false},
		{"newer than the latest (a build in between)", false, "", "0.3.1", "0.3.2-0.20261126-abc", false},
		{"a dev build", false, "", "0.4.0", "dev", true},
		{"latest unknown: the download decides", false, "", "", "0.4.0", true},
		{"force installs even the same version", true, "", "0.4.0", "0.4.0", true},
		{"a version asked for, newer", false, "0.5.0", "", "0.4.0", true},
		{"a version asked for, going back", false, "0.3.0", "0.4.0", "0.4.0", true},
		{"a version asked for that is already installed", false, "0.4.0", "0.4.0", "0.4.0", false},
	} {
		if got := needsBinary(c.force, c.want, c.latest, c.current); got != c.need {
			t.Errorf("%s: needsBinary = %v, want %v", c.name, got, c.need)
		}
	}
}
