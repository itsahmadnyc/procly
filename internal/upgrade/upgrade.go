// Package upgrade finds, downloads, checks and installs a newer procly binary.
// It does not touch a running daemon; the CLI does that, see cmd upgrade.
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
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is where releases are published.
const Repo = "itsahmadnyc/procly"

const (
	maxChecksums = 64 << 10
	maxArchive   = 200 << 20
	maxBinary    = 300 << 20
)

// Source says where releases come from. The zero value is GitHub; tests and
// mirrors set the fields (PROCLY_DOWNLOAD_BASE and PROCLY_LATEST_URL in the CLI).
type Source struct {
	// Base is a folder that holds procly_<os>_<arch>.tar.gz and checksums.txt.
	// Empty means GitHub: the release of the version asked for, or the latest.
	Base string
	// LatestURL is a page that redirects to .../tag/v<version>. Empty means GitHub's.
	LatestURL string
	Client    *http.Client
}

func (s Source) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (s Source) baseFor(version string) string {
	if s.Base != "" {
		return strings.TrimRight(s.Base, "/")
	}
	if version == "" {
		return "https://github.com/" + Repo + "/releases/latest/download"
	}
	return "https://github.com/" + Repo + "/releases/download/v" + strings.TrimPrefix(version, "v")
}

// LatestVersion asks where "latest" points, without downloading anything: a
// redirect from .../releases/latest to .../releases/tag/v1.2.3.
func (s Source) LatestVersion(ctx context.Context) (string, error) {
	url := s.LatestURL
	if url == "" {
		url = "https://github.com/" + Repo + "/releases/latest"
	}
	c := *s.client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("looking for the latest version: %w", err)
	}
	res.Body.Close()
	loc := res.Header.Get("Location")
	i := strings.LastIndex(loc, "/tag/v")
	if res.StatusCode/100 != 3 || i < 0 {
		return "", fmt.Errorf("looking for the latest version: %s did not point at a release (HTTP %d)", url, res.StatusCode)
	}
	v := loc[i+len("/tag/v"):]
	if _, ok := parse(v); !ok {
		return "", fmt.Errorf("looking for the latest version: %q is not a version", v)
	}
	return v, nil
}

// Platform is the release asset suffix for this machine, like linux_amd64.
func Platform() (string, error) {
	var os, arch string
	switch runtime.GOOS {
	case "linux", "darwin":
		os = runtime.GOOS
	default:
		return "", fmt.Errorf("procly releases are for Linux and macOS, not %s", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64", "arm64":
		arch = runtime.GOARCH
	default:
		return "", fmt.Errorf("procly releases are for amd64 and arm64, not %s", runtime.GOARCH)
	}
	return os + "_" + arch, nil
}

// Download fetches the release archive for this machine (version "" is the
// latest), checks it against checksums.txt, and unpacks the binary into dir. It
// returns the binary's path and the version the binary reports, which has to be
// the one asked for when one was.
func (s Source) Download(ctx context.Context, version, dir string) (bin, got string, err error) {
	platform, err := Platform()
	if err != nil {
		return "", "", err
	}
	base := s.baseFor(version)
	asset := "procly_" + platform + ".tar.gz"

	sums, err := s.fetch(ctx, base+"/checksums.txt", maxChecksums)
	if err != nil {
		return "", "", fmt.Errorf("downloading checksums.txt: %w", err)
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == asset {
			want = f[0]
		}
	}
	if want == "" {
		return "", "", fmt.Errorf("checksums.txt has no entry for %s", asset)
	}

	archive, err := s.fetch(ctx, base+"/"+asset, maxArchive)
	if err != nil {
		return "", "", fmt.Errorf("downloading %s: %w", asset, err)
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != want {
		return "", "", fmt.Errorf("%s does not match its checksum in checksums.txt; nothing was installed", asset)
	}

	bin = filepath.Join(dir, "procly-new")
	if err := extractBinary(archive, bin); err != nil {
		return "", "", fmt.Errorf("unpacking %s: %w", asset, err)
	}
	got, err = Version(bin)
	if err != nil {
		return "", "", fmt.Errorf("the downloaded binary does not run: %w", err)
	}
	if version != "" && got != strings.TrimPrefix(version, "v") {
		return "", "", fmt.Errorf("asked for %s but the downloaded binary says %s; nothing was installed", version, got)
	}
	return bin, got, nil
}

func (s Source) fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", url, limit)
	}
	return data, nil
}

// extractBinary writes the file called procly in the archive (whatever folder it
// sits in) to dest, executable. Nothing else is unpacked.
func extractBinary(archive []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("no procly binary in the archive")
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || path.Base(h.Name) != "procly" {
			continue
		}
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, io.LimitReader(tr, maxBinary+1))
		if err == nil && n > maxBinary {
			err = errors.New("the binary in the archive is implausibly large")
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(dest)
			return err
		}
		return os.Chmod(dest, 0o755)
	}
}

// Version runs bin and returns the version it reports ("procly 1.2.3" gives 1.2.3).
func Version(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(out))
	if len(f) != 2 || f[0] != "procly" {
		return "", fmt.Errorf("unexpected output %q from %s version", strings.TrimSpace(string(out)), bin)
	}
	return f[1], nil
}

// Owner says who manages this binary when it is not procly's job to replace it:
// "homebrew" or "npm". Empty means it can be replaced here.
func Owner(exe string) string {
	switch {
	case strings.Contains(exe, "/Cellar/") || strings.Contains(exe, "/homebrew/"):
		return "homebrew"
	case strings.Contains(exe, "/node_modules/"):
		return "npm"
	}
	return ""
}

// ErrNeedsPrivilege means the folder of the binary is not writable by this user.
var ErrNeedsPrivilege = errors.New("the folder the binary is in is not writable by you")

// Replace puts newBin where exe is, keeping the old one as exe+".old" until the
// caller is sure (RemoveBackup) or wants it back (Restore). It writes next to
// exe and renames, so the file at exe is always a whole binary.
func Replace(exe, newBin string) error {
	dir := filepath.Dir(exe)
	tmp := filepath.Join(dir, ".procly-upgrade-"+strconv.Itoa(os.Getpid()))
	if err := copyFile(newBin, tmp); err != nil {
		os.Remove(tmp)
		if errors.Is(err, os.ErrPermission) {
			return ErrNeedsPrivilege
		}
		return err
	}
	backup := exe + ".old"
	_ = os.Remove(backup)
	if err := os.Rename(exe, backup); err != nil {
		os.Remove(tmp)
		if errors.Is(err, os.ErrPermission) {
			return ErrNeedsPrivilege
		}
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Rename(backup, exe) // put it back
		os.Remove(tmp)
		return err
	}
	return nil
}

// Restore puts the backup made by Replace back.
func Restore(exe string) error { return os.Rename(exe+".old", exe) }

// RemoveBackup deletes the backup made by Replace.
func RemoveBackup(exe string) { _ = os.Remove(exe + ".old") }

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, 0o755)
}

// Compare orders two versions: -1, 0 or 1. A build made between releases
// ("0.3.2-0.2026...-abc", a pre-release of 0.3.2) is older than 0.3.2 and newer
// than 0.3.1. Text that is not a version sorts before any that is.
func Compare(a, b string) int {
	pa, oka := parse(a)
	pb, okb := parse(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return -1
	case !okb:
		return 1
	}
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			if pa.n[i] < pb.n[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case pa.pre == pb.pre:
		return 0
	case pa.pre == "":
		return 1 // a release is newer than its own pre-releases
	case pb.pre == "":
		return -1
	case pa.pre < pb.pre:
		return -1
	}
	return 1
}

type semver struct {
	n   [3]int
	pre string
}

func parse(v string) (semver, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	var s semver
	core := v
	if i := strings.IndexByte(v, '-'); i >= 0 {
		core, s.pre = v[:i], v[i+1:]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		s.n[i] = n
	}
	return s, true
}
