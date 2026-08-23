package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Load loads configuration from default locations with proper precedence
func Load() (*Config, error) {
	// Start with defaults
	cfg := DefaultConfig()

	configEnv := loadConfigEnvVars()

	// Load user config (~/.buckley/config.yaml)
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to HOME env var if UserHomeDir fails
		home = os.Getenv("HOME")
	}
	if home != "" {
		userConfigPath := filepath.Join(home, ".buckley", "config.yaml")
		if err := loadAndMerge(cfg, userConfigPath, false); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("loading user config: %w", err)
		}
	}

	// Load project config (./.buckley/config.yaml)
	projectConfigPath := filepath.Join(".", ".buckley", "config.yaml")
	if err := loadAndMerge(cfg, projectConfigPath, true); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("loading project config: %w", err)
	}

	// Apply environment variable overrides
	applyEnvOverrides(cfg, configEnv)
	cfg.normalizeReasoningModelIDs()
	cfg.applyConfiguredProviderHints()
	cfg.alignModelDefaultsWithProviders()
	cfg.normalizeReasoningModelIDs()
	cfg.applyProviderReasoningDefaults()

	// Validate
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return cfg, nil
}

// LoadForWorkspace loads the ordinary user configuration plus the project
// configuration rooted at workspaceRoot without changing the process working
// directory. It performs no writes.
func LoadForWorkspace(workspaceRoot string) (*Config, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	abs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory")
	}

	cfg := DefaultConfig()
	configEnv := loadConfigEnvVars()
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		home = os.Getenv("HOME")
	}
	if home != "" {
		userConfigPath := filepath.Join(home, ".buckley", "config.yaml")
		if err := loadAndMerge(cfg, userConfigPath, false); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("loading user config: %w", err)
		}
	}
	if err := loadWorkspaceProjectConfig(cfg, resolved); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("loading project config: %w", err)
	}
	applyEnvOverrides(cfg, configEnv)
	cfg.normalizeReasoningModelIDs()
	cfg.applyConfiguredProviderHints()
	cfg.alignModelDefaultsWithProviders()
	cfg.normalizeReasoningModelIDs()
	cfg.applyProviderReasoningDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}
	return cfg, nil
}

const maxWorkspaceProjectConfigBytes = 1 << 20

func loadWorkspaceProjectConfig(cfg *Config, workspaceRoot string) error {
	root, err := os.OpenRoot(workspaceRoot)
	if err != nil {
		return err
	}
	defer root.Close()

	dir, err := root.Lstat(".buckley")
	if err != nil {
		return err
	}
	if dir.Mode()&os.ModeSymlink != 0 || !dir.IsDir() {
		return fmt.Errorf("project config directory is not a regular directory")
	}
	const relative = ".buckley/config.yaml"
	before, err := root.Lstat(relative)
	if err != nil {
		return err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > maxWorkspaceProjectConfigBytes {
		return fmt.Errorf("project config is not a bounded regular file")
	}
	file, err := root.Open(relative)
	if err != nil {
		return err
	}
	opened, statErr := file.Stat()
	data, readErr := io.ReadAll(io.LimitReader(file, maxWorkspaceProjectConfigBytes+1))
	afterRead, afterReadErr := file.Stat()
	closeErr := file.Close()
	after, lstatErr := root.Lstat(relative)
	if statErr != nil || readErr != nil || afterReadErr != nil || closeErr != nil || lstatErr != nil || len(data) > maxWorkspaceProjectConfigBytes {
		return fmt.Errorf("project config stable read failed")
	}
	for _, current := range []os.FileInfo{opened, afterRead, after} {
		if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(before, current) || current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) {
			return fmt.Errorf("project config changed during read")
		}
	}
	return mergeConfigData(cfg, data, relative, true)
}

// LoadFromPath loads configuration from a specific file path
func LoadFromPath(path string) (*Config, error) {
	cfg := DefaultConfig()

	configEnv := loadConfigEnvVars()

	// Load from the specified path
	if err := loadAndMerge(cfg, path, false); err != nil {
		return nil, fmt.Errorf("loading config from %s: %w", path, err)
	}

	// Apply environment variable overrides
	applyEnvOverrides(cfg, configEnv)
	cfg.normalizeReasoningModelIDs()
	cfg.applyConfiguredProviderHints()
	cfg.alignModelDefaultsWithProviders()
	cfg.normalizeReasoningModelIDs()
	cfg.applyProviderReasoningDefaults()

	// Validate
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return cfg, nil
}

// ApplyEnvOverridesForTest exposes env override logic for tests without file I/O.
func ApplyEnvOverridesForTest(cfg *Config) {
	applyEnvOverrides(cfg, nil)
	cfg.normalizeReasoningModelIDs()
	cfg.applyConfiguredProviderHints()
	cfg.alignModelDefaultsWithProviders()
	cfg.normalizeReasoningModelIDs()
	cfg.applyProviderReasoningDefaults()
}
func loadConfigEnvVars() map[string]string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}

	path := filepath.Join(home, ".buckley", "config.env")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	vars := make(map[string]string)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}
		value = strings.Trim(value, "\"'")
		vars[key] = value
	}
	return vars
}
