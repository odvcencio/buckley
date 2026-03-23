package rules

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed rules/*.arb
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
func (l *Loader) Load(domain string) ([]byte, error) {
	filename := domain + ".arb"

	// Check user override
	if l.overrideDir != "" {
		path := filepath.Join(l.overrideDir, filename)
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
func (l *Loader) Domains() ([]string, error) {
	entries, err := defaultRules.ReadDir("rules")
	if err != nil {
		return nil, err
	}
	var domains []string
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) == ".arb" {
			domains = append(domains, name[:len(name)-4])
		}
	}
	return domains, nil
}
