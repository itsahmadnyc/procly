// Package config loads procly.yml / procly.json app definitions.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"procly/internal/proc"
)

type File struct {
	Apps []proc.Spec `yaml:"apps" json:"apps"`
}

// IsConfig reports whether path looks like a procly config file.
func IsConfig(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yml", ".yaml", ".json":
		return true
	}
	return false
}

// Load reads the apps from a config file. Relative cwd values resolve
// against the config file's directory.
func Load(path string) ([]proc.Spec, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	var f File
	// YAML is a superset of JSON, so one decoder handles both.
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if len(f.Apps) == 0 {
		return nil, fmt.Errorf("%s: no apps defined (expected an `apps:` list)", filepath.Base(path))
	}
	dir := filepath.Dir(abs)
	seen := map[string]bool{}
	for i := range f.Apps {
		a := &f.Apps[i]
		if a.Name == "" {
			return nil, fmt.Errorf("%s: app #%d has no name", filepath.Base(path), i+1)
		}
		if seen[a.Name] {
			return nil, fmt.Errorf("%s: duplicate app name %q", filepath.Base(path), a.Name)
		}
		seen[a.Name] = true
		switch {
		case a.Cwd == "":
			a.Cwd = dir
		case !filepath.IsAbs(a.Cwd):
			a.Cwd = filepath.Join(dir, a.Cwd)
		}
	}
	return f.Apps, nil
}
