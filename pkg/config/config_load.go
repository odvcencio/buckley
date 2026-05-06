package config

import (
	"fmt"
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
