package tool

import (
	"fmt"
	"os"
	"path/filepath"

	"m31labs.dev/buckley/v2/pkg/tool/external"
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
	return r.LoadExternalFromMultipleDirs(defaultPluginDirs())
}

// defaultPluginDirs returns the standard plugin locations: user
// (~/.buckley/plugins), project (./.buckley/plugins), and built-in
// (./plugins).
func defaultPluginDirs() []string {
	dirs := []string{}

	homeDir, err := os.UserHomeDir()
	if err == nil {
		dirs = append(dirs, filepath.Join(homeDir, ".buckley", "plugins"))
	}

	cwd, err := os.Getwd()
	if err == nil {
		dirs = append(dirs, filepath.Join(cwd, ".buckley", "plugins"))
		dirs = append(dirs, filepath.Join(cwd, "plugins"))
	}

	return dirs
}
