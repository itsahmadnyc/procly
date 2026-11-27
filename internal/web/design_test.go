package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// rule returns the declarations of every CSS rule whose selector is exactly
// sel, joined, or "" when there is none.
func rule(css, sel string) string {
	re := regexp.MustCompile(`(?m)(?:^|\})\s*` + regexp.QuoteMeta(sel) + `\s*\{([^}]*)\}`)
	var all []string
	for _, m := range re.FindAllStringSubmatch(css, -1) {
		all = append(all, m[1])
	}
	return strings.Join(all, "\n")
}

// docs/web-design.md says no page has a maximum width: the panel uses the
// whole window, and wide screens get more columns, not a strip on the left.
func TestPanelUsesTheWholeWidth(t *testing.T) {
	b, err := os.ReadFile("assets/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	for _, sel := range []string{".page", ".main", ".view", ".cols", ".settings", ".stats", ".band", ".charts", ".card", ".table"} {
		if strings.Contains(rule(css, sel), "max-width") {
			t.Errorf("%s has a max-width; pages must use the whole width", sel)
		}
	}
	if rule(css, ".page") == "" {
		t.Error("no .page rule found; update this test with the stylesheet")
	}
}

// The things the design decided against stay out: small uppercase
// letter-spaced labels, gradients on surfaces, and glow.
func TestPanelAvoidsTheTemplateTells(t *testing.T) {
	b, err := os.ReadFile("assets/style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	if strings.Contains(css, "text-transform: uppercase") {
		t.Error("uppercase labels are out: labels are sentence case")
	}
	for _, bad := range []string{"radial-gradient(", "conic-gradient(", "backdrop-filter", "text-shadow"} {
		if strings.Contains(css, bad) {
			t.Errorf("%s is out: no gradients, glass or glow", bad)
		}
	}
	// the one gradient is the hatching of an orphan's bar, a pattern for state
	if strings.Count(css, "gradient(") != 1 || !strings.Contains(css, "repeating-linear-gradient(") {
		t.Errorf("only the orphan hatching may use a gradient, found %d", strings.Count(css, "gradient("))
	}
	js, err := os.ReadFile("assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"ev-icon", "kpis", "server-grid"} {
		if strings.Contains(string(js), bad) {
			t.Errorf("app.js still uses %q, a pattern the redesign removed", bad)
		}
	}
}
