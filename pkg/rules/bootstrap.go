package rules

import (
	"os"
	"path/filepath"
)

// DefaultOverrideDir returns the standard user override directory for .arb files.
func DefaultOverrideDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".buckley", "rules")
}

// NewDefaultEngine loads the embedded rules plus the standard user overrides.
func NewDefaultEngine() (*Engine, error) {
	overrideDir := DefaultOverrideDir()
	if overrideDir == "" {
		return NewEngine()
	}
	return NewEngine(WithUserOverrides(overrideDir))
}
