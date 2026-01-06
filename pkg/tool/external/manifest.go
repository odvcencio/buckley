package external

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ToolManifest represents the structure of a tool.yaml file
type ToolManifest struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Parameters  map[string]any `yaml:"parameters"`
	Executable  string         `yaml:"executable"`
	TimeoutMs   int            `yaml:"timeout_ms"`
}

// LoadManifest reads and parses a tool manifest file
func LoadManifest(path string) (*ToolManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	var manifest ToolManifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	// Validate required fields
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	return &manifest, nil
}

// Validate checks that all required fields are present and valid
func (tm *ToolManifest) Validate() error {
	if tm.Name == "" {
		return fmt.Errorf("name is required")
	}
	if tm.Description == "" {
		return fmt.Errorf("description is required")
	}
	if tm.Executable == "" {
		return fmt.Errorf("executable is required")
	}
	if tm.TimeoutMs <= 0 {
		// Default to 2 minutes if not specified
		tm.TimeoutMs = 120000
	}
	if tm.Parameters == nil {
		// Initialize empty parameters if not provided
		tm.Parameters = make(map[string]any)
	}
	return nil
}
