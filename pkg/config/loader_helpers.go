package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// loadAndMerge loads a YAML file and merges it into the config.
func loadAndMerge(cfg *Config, path string, projectScope bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return mergeConfigData(cfg, data, path, projectScope)
}

func mergeConfigData(cfg *Config, data []byte, source string, projectScope bool) error {
	var override Config
	if err := yaml.Unmarshal(data, &override); err != nil {
		return fmt.Errorf("parsing YAML from %s: %w", source, err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing YAML from %s: %w", source, err)
	}

	mergeConfigs(cfg, &override, raw, projectScope)
	return nil
}
