package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBinary is a script that answers `version` like procly does.
func fakeBinary(version string) []byte {
	return []byte("#!/bin/sh\n[ \"$1\" = version ] && echo 'procly " + version + "'\n")
}

func archiveOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(data)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

type release struct {
	files       map[string][]byte // archive contents
	checksum    string            // overrides the real one
	noChecksums bool
}

func mirror(t *testing.T, r release) Source {
	t.Helper()
	platform, err := Platform()
	if err != nil {
		t.Skip(err)
	}
	asset := "procly_" + platform + ".tar.gz"
	archive := archiveOf(t, r.files)
	sum := sha256.Sum256(archive)
	hexsum := hex.EncodeToString(sum[:])
	if r.checksum != "" {
		hexsum = r.checksum
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/"+asset, func(w http.ResponseWriter, _ *http.Request) { w.Write(archive) })
	if !r.noChecksums {
		mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, "%s  %s\n%s  procly_other_arch.tar.gz\n", hexsum, asset, strings.Repeat("0", 64))
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return Source{Base: srv.URL}
}

func TestDownloadVerifiesAndUnpacks(t *testing.T) {
	src := mirror(t, release{files: map[string][]byte{"procly_x/procly": fakeBinary("2.0.0"), "procly_x/README.md": []byte("hi"), "procly_x/LICENSE": []byte("mit")}})
	dir := t.TempDir()
	bin, got, err := src.Download(context.Background(), "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2.0.0" || bin != filepath.Join(dir, "procly-new") {
		t.Errorf("bin %q version %q", bin, got)
	}
	if fi, _ := os.Stat(bin); fi.Mode().Perm() != 0o755 {
		t.Errorf("the binary must be executable: %v", fi.Mode())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("only the binary is unpacked: %v", entries)
	}
	// Asking for the version it is works; asking for another is refused.
	if _, _, err := src.Download(context.Background(), "2.0.0", t.TempDir()); err != nil {
		t.Errorf("the right version: %v", err)
	}
	if _, _, err := src.Download(context.Background(), "3.0.0", t.TempDir()); err == nil || !strings.Contains(err.Error(), "says 2.0.0") {
		t.Errorf("a binary of another version: %v", err)
	}
}

func TestDownloadRefusesWhatDoesNotCheckOut(t *testing.T) {
	good := map[string][]byte{"procly_x/procly": fakeBinary("2.0.0")}
	for name, c := range map[string]struct {
		r    release
		want string
	}{
		"a wrong checksum":      {release{files: good, checksum: strings.Repeat("a", 64)}, "does not match its checksum"},
		"no checksums file":     {release{files: good, noChecksums: true}, "checksums.txt"},
		"no binary inside":      {release{files: map[string][]byte{"procly_x/README.md": []byte("x")}}, "no procly binary"},
		"a binary that fails":   {release{files: map[string][]byte{"procly_x/procly": []byte("#!/bin/sh\nexit 3\n")}}, "does not run"},
		"output that is not ok": {release{files: map[string][]byte{"procly_x/procly": []byte("#!/bin/sh\necho hello\n")}}, "does not run"},
	} {
		dir := t.TempDir()
		_, _, err := mirror(t, c.r).Download(context.Background(), "", dir)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want it to contain %q", name, err, c.want)
		}
	}
}

func TestLatestVersionReadsTheRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/itsahmadnyc/procly/releases/tag/v1.4.2", http.StatusFound)
	})
	mux.HandleFunc("/broken", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("no redirect")) })
	mux.HandleFunc("/weird", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/vnext", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	v, err := (Source{LatestURL: srv.URL + "/releases/latest"}).LatestVersion(context.Background())
	if err != nil || v != "1.4.2" {
		t.Errorf("latest = %q, %v", v, err)
	}
	for _, path := range []string{"/broken", "/weird", "/nowhere"} {
		if _, err := (Source{LatestURL: srv.URL + path}).LatestVersion(context.Background()); err == nil {
			t.Errorf("%s should not give a version", path)
		}
	}
}

func TestCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0}, {"v1.2.3", "1.2.3", 0}, {"1.2.3", "1.2.4", -1}, {"1.10.0", "1.9.9", 1}, {"2.0.0", "1.99.99", 1},
		{"0.3.2-0.20261126135851-a1a007049912", "0.3.1", 1},  // a build after 0.3.1
		{"0.3.2-0.20261126135851-a1a007049912", "0.3.2", -1}, // but before 0.3.2
		{"0.3.2-rc1", "0.3.2-rc2", -1}, {"0.3.2", "0.3.2-rc2", 1},
		{"1.0.0+build5", "1.0.0", 0}, {"dev", "0.0.1", -1}, {"0.0.1", "dev", 1}, {"dev", "dev", 0}, {"1.2", "1.2.0", -1},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if c.a != c.b && Compare(c.b, c.a) != -c.want && !(c.want == 0) {
			t.Errorf("Compare is not antisymmetric for %q, %q", c.a, c.b)
		}
	}
}

func TestReplaceKeepsABackupAndCanRestore(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "procly")
	os.WriteFile(exe, fakeBinary("1.0.0"), 0o755)
	newBin := filepath.Join(t.TempDir(), "procly-new")
	os.WriteFile(newBin, fakeBinary("2.0.0"), 0o755)

	if err := Replace(exe, newBin); err != nil {
		t.Fatal(err)
	}
	if v, _ := Version(exe); v != "2.0.0" {
		t.Errorf("after Replace the binary says %s", v)
	}
	if v, _ := Version(exe + ".old"); v != "1.0.0" {
		t.Errorf("the old binary is kept as a backup: %s", v)
	}
	if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %v", fi.Mode())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Errorf("no temporary files may be left: %v", entries)
	}
	if err := Restore(exe); err != nil {
		t.Fatal(err)
	}
	if v, _ := Version(exe); v != "1.0.0" {
		t.Errorf("Restore: %s", v)
	}
	// And a backup can be dropped.
	if err := Replace(exe, newBin); err != nil {
		t.Fatal(err)
	}
	RemoveBackup(exe)
	if _, err := os.Stat(exe + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("RemoveBackup: %v", err)
	}
}

func TestReplaceNeedsPrivilegeIsSaidSo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "procly")
	os.WriteFile(exe, fakeBinary("1.0.0"), 0o755)
	newBin := filepath.Join(t.TempDir(), "procly-new")
	os.WriteFile(newBin, fakeBinary("2.0.0"), 0o755)
	os.Chmod(dir, 0o555)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if err := Replace(exe, newBin); !errors.Is(err, ErrNeedsPrivilege) {
		t.Errorf("a folder that cannot be written: %v", err)
	}
	if v, _ := Version(exe); v != "1.0.0" {
		t.Errorf("a failed replace must leave the binary alone: %s", v)
	}
}

func TestOwner(t *testing.T) {
	for exe, want := range map[string]string{
		"/opt/homebrew/Cellar/procly/0.3.1/bin/procly":           "homebrew",
		"/home/linuxbrew/.linuxbrew/Cellar/procly/64/bin/procly": "homebrew",
		"/usr/lib/node_modules/procly-linux-x64/bin/procly":      "npm",
		"/usr/local/bin/procly":                                  "",
		"/home/me/.local/bin/procly":                             "",
	} {
		if got := Owner(exe); got != want {
			t.Errorf("Owner(%s) = %q, want %q", exe, got, want)
		}
	}
}
