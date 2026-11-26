package web

import (
	"os/exec"
	"testing"
)

// The panel's h() helper must never turn a value into a javascript:, data: or
// off-site link. The check runs the real helper from assets/app.js in node.
func TestPanelHrefGuard(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/href_guard.js", "assets/app.js").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
