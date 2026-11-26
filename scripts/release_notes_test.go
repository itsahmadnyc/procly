// Package scripts only holds a test for release-notes.sh, so the release
// notes logic runs with `go test ./...` on every CI platform.
package scripts

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func notes(t *testing.T, version string) (string, error) {
	t.Helper()
	out, err := exec.Command("sh", "release-notes.sh", version).Output()
	return string(out), err
}

func mustNotes(t *testing.T, version string) string {
	t.Helper()
	out, err := notes(t, version)
	if err != nil {
		t.Fatalf("release-notes.sh %s: %v", version, err)
	}
	return out
}

func TestEachSectionStaysInItsOwnVersion(t *testing.T) {
	v3, v2 := mustNotes(t, "0.3.0"), mustNotes(t, "0.2.0")
	if !strings.Contains(v3, "keepalive-ring") || strings.Contains(v3, "Log rotation") {
		t.Errorf("0.3.0 notes mix versions:\n%s", v3)
	}
	if !strings.Contains(v2, "Log rotation") || strings.Contains(v2, "keepalive-ring") {
		t.Errorf("0.2.0 notes mix versions:\n%s", v2)
	}
}

func TestHeadingWithATitleAndSubsections(t *testing.T) {
	v1 := mustNotes(t, "0.1.0") // "## v0.1.0 — first release", with ### subsections inside
	if !strings.Contains(v1, "### Run processes") || strings.Contains(v1, "Log rotation") {
		t.Errorf("0.1.0 notes are wrong:\n%s", v1)
	}
}

func TestInstallBlockReplacesTheUpgradingParagraph(t *testing.T) {
	for _, v := range []string{"0.3.0", "0.2.0"} {
		out := mustNotes(t, v)
		if !strings.Contains(out, "### Install") || !strings.Contains(out, "install.sh | sh") {
			t.Errorf("%s: no install block", v)
		}
		if n := strings.Count(out, "Upgrading:"); n != 1 {
			t.Errorf("%s: %d \"Upgrading:\" paragraphs, want only the one in the install block", v, n)
		}
		if strings.HasPrefix(out, "\n") {
			t.Errorf("%s: notes start with a blank line", v)
		}
	}
}

func TestVersionSpellings(t *testing.T) {
	if mustNotes(t, "v0.3.0") != mustNotes(t, "0.3.0") {
		t.Error("a leading v changes the notes")
	}
}

func TestLatestIsTheTopSection(t *testing.T) {
	b, err := os.ReadFile("../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^## v(\S+)`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no version heading in CHANGELOG.md")
	}
	if mustNotes(t, "latest") != mustNotes(t, string(m[1])) {
		t.Errorf("latest is not v%s", m[1])
	}
}

func TestMissingVersionFails(t *testing.T) {
	out, err := notes(t, "9.9.9")
	if err == nil || out != "" {
		t.Fatalf("a version with no notes must fail without output, got %q, %v", out, err)
	}
}
