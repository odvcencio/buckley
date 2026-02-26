package tool

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/odvcencio/buckley/pkg/tool/external"
)

// LoadExternal loads external plugin tools from a directory
func (r *Registry) LoadExternal(pluginDir string) error {
	tools, err := external.DiscoverPlugins(pluginDir)
	if err != nil {
		return fmt.Errorf("failed to discover plugins in %s: %w", pluginDir, err)
	}

	for _, tool := range tools {
		r.Register(tool)
	}

	return nil
}

// LoadExternalFromMultipleDirs loads external plugins from multiple directories
func (r *Registry) LoadExternalFromMultipleDirs(dirs []string) error {
	tools, err := external.DiscoverFromMultipleDirs(dirs)
	if err != nil {
		return fmt.Errorf("failed to discover plugins: %w", err)
	}

	for _, tool := range tools {
		r.Register(tool)
	}

	return nil
}

// LoadDefaultPlugins loads plugins from standard locations
func (r *Registry) LoadDefaultPlugins() error {
	dirs := []string{}

	// User plugin directory: ~/.buckley/plugins/
	homeDir, err := os.UserHomeDir()
	if err == nil {
		userPluginDir := filepath.Join(homeDir, ".buckley", "plugins")
		dirs = append(dirs, userPluginDir)
	}

	// Project plugin directory: ./.buckley/plugins/
	cwd, err := os.Getwd()
	if err == nil {
		projectPluginDir := filepath.Join(cwd, ".buckley", "plugins")
		dirs = append(dirs, projectPluginDir)
	}

	// Built-in plugin directory: ./plugins/
	if cwd != "" {
		builtinPluginDir := filepath.Join(cwd, "plugins")
		dirs = append(dirs, builtinPluginDir)
	}

	return r.LoadExternalFromMultipleDirs(dirs)
}
