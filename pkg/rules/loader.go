package rules

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed all:rules
var defaultRules embed.FS

// Loader loads .arb rule files from embedded defaults or user overrides.
type Loader struct {
	overrideDir string
}

// NewLoader creates a loader. If overrideDir is empty, only embedded defaults are used.
func NewLoader(overrideDir string) *Loader {
	return &Loader{overrideDir: overrideDir}
}

// Load returns the .arb source for the given domain.
// Checks user override directory first, falls back to embedded default.
// Domain may be slash-separated (e.g. "permissions/escalation").
func (l *Loader) Load(domain string) ([]byte, error) {
	filename := domain + ".arb"

	// Check user override
	if l.overrideDir != "" {
		path := filepath.Join(l.overrideDir, filepath.FromSlash(filename))
		if data, err := os.ReadFile(path); err == nil {
			return data, nil
		}
	}

	// Fall back to embedded default
	data, err := defaultRules.ReadFile("rules/" + filename)
	if err != nil {
		return nil, fmt.Errorf("rule domain %q not found: %w", domain, err)
	}
	return data, nil
}

// Domains returns all available domain names from embedded defaults.
// Subdirectory domains are returned as slash-separated paths (e.g. "permissions/escalation").
func (l *Loader) Domains() ([]string, error) {
	var domains []string
	err := fs.WalkDir(defaultRules, "rules", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if filepath.Ext(d.Name()) != ".arb" {
			return nil
		}
		rel, _ := filepath.Rel("rules", path)
		domain := strings.TrimSuffix(rel, ".arb")
		domains = append(domains, domain)
		return nil
	})
	return domains, err
}
