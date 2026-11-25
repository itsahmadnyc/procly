// Package remote is the hub side of multi-server support: the list of
// connected agents and a client that talks to them.
package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/itsahmadnyc/procly/internal/paths"
)

// LocalName is reserved for the machine the hub itself runs on.
const LocalName = "local"

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)

// Server is one connected agent. The token is stored in plain text because
// the hub has to send it; servers.json is owner-only (0600).
type Server struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Token       string `json:"token"`
	Fingerprint string `json:"fingerprint"`
}

func (s Server) Validate() error {
	if !nameRe.MatchString(s.Name) || s.Name == LocalName {
		return fmt.Errorf("invalid server name %q: lowercase letters, digits, '.', '_', '-' (and not %q)", s.Name, LocalName)
	}
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("url must look like https://host:4748, got %q", s.URL)
	}
	if s.Token == "" {
		return errors.New("token is required")
	}
	if _, err := normalizeFingerprint(s.Fingerprint); err != nil {
		return err
	}
	return nil
}

func registryPath() string { return filepath.Join(paths.Home(), "servers.json") }

// Load returns the saved servers sorted by name. A missing file means none.
func Load() ([]Server, error) {
	data, err := os.ReadFile(registryPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Server
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("servers.json is damaged: %w", err)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list, nil
}

func save(list []Server) error {
	if err := paths.Ensure(); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	tmp := registryPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, registryPath())
}

// Add saves s, replacing a server with the same name.
func Add(s Server) error {
	s.URL = strings.TrimRight(s.URL, "/")
	fp, err := normalizeFingerprint(s.Fingerprint)
	if err != nil {
		return err
	}
	s.Fingerprint = fp
	if err := s.Validate(); err != nil {
		return err
	}
	list, err := Load()
	if err != nil {
		return err
	}
	out := list[:0]
	for _, x := range list {
		if x.Name != s.Name {
			out = append(out, x)
		}
	}
	return save(append(out, s))
}

// Remove deletes a server by name.
func Remove(name string) error {
	list, err := Load()
	if err != nil {
		return err
	}
	out := list[:0]
	found := false
	for _, x := range list {
		if x.Name == name {
			found = true
			continue
		}
		out = append(out, x)
	}
	if !found {
		return fmt.Errorf("no server named %q", name)
	}
	return save(out)
}

// normalizeFingerprint accepts "sha256:ab12...", "AB:12:...", or plain hex.
func normalizeFingerprint(fp string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(fp))
	h = strings.TrimPrefix(h, "sha256:")
	h = strings.ReplaceAll(h, ":", "")
	if len(h) != 64 || strings.Trim(h, "0123456789abcdef") != "" {
		return "", fmt.Errorf("fingerprint must be the sha256 shown by 'procly agent', got %q", fp)
	}
	return "sha256:" + h, nil
}
