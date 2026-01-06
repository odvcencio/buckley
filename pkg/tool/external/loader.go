package external

import (
	"fmt"
	"os"
	"path/filepath"
)

// DiscoverPlugins scans a directory for plugin manifests and returns loaded tools
func DiscoverPlugins(pluginDir string) ([]*ExternalTool, error) {
	// Check if directory exists
	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		// Directory doesn't exist, return empty list (not an error)
		return []*ExternalTool{}, nil
	}

	tools := []*ExternalTool{}

	// Walk the plugin directory
	err := filepath.Walk(pluginDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Look for tool.yaml files
		if !info.IsDir() && filepath.Base(path) == "tool.yaml" {
			// Load the manifest
			manifest, err := LoadManifest(path)
			if err != nil {
				// Log warning but continue discovering other tools
				fmt.Fprintf(os.Stderr, "Warning: failed to load manifest %s: %v\n", path, err)
				return nil
			}

			// Resolve executable path relative to manifest directory
			manifestDir := filepath.Dir(path)
			execPath := filepath.Join(manifestDir, manifest.Executable)

			// Validate executable exists
			if _, err := os.Stat(execPath); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: executable not found for %s: %s\n", manifest.Name, execPath)
				return nil
			}

			// Check if executable is actually executable
			if err := checkExecutable(execPath); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: file is not executable for %s: %s (%v)\n", manifest.Name, execPath, err)
				return nil
			}

			// Create the tool
			tool := NewTool(manifest, execPath)
			tools = append(tools, tool)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to discover plugins: %w", err)
	}

	return tools, nil
}

// checkExecutable verifies that a file is executable
func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	// Check if file has execute permissions
	mode := info.Mode()
	if mode&0111 == 0 {
		return fmt.Errorf("file is not executable (mode: %v)", mode)
	}

	return nil
}

// DiscoverFromMultipleDirs discovers plugins from multiple directories
func DiscoverFromMultipleDirs(dirs []string) ([]*ExternalTool, error) {
	allTools := []*ExternalTool{}
	seen := make(map[string]bool)

	for _, dir := range dirs {
		tools, err := DiscoverPlugins(dir)
		if err != nil {
			return nil, err
		}

		// Deduplicate tools by name (first one wins)
		for _, tool := range tools {
			if !seen[tool.Name()] {
				allTools = append(allTools, tool)
				seen[tool.Name()] = true
			}
		}
	}

	return allTools, nil
}
