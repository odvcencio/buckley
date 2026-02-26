package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/odvcencio/buckley/pkg/ralph"
	"gopkg.in/yaml.v3"
)

// setBackendValue sets a value for a backend in the config.
func setBackendValue(cfg *ralph.ControlConfig, backendName string, parts []string, value string) error {
	if cfg.Backends == nil {
		cfg.Backends = make(map[string]ralph.BackendConfig)
	}

	backend, exists := cfg.Backends[backendName]
	if !exists {
		// Create new backend if it doesn't exist.
		backend = ralph.BackendConfig{}
	}

	if len(parts) == 0 {
		return fmt.Errorf("backend %q requires a field name", backendName)
	}

	switch parts[0] {
	case "type":
		if len(parts) != 1 {
			return fmt.Errorf("type does not support nested keys")
		}
		backend.Type = value

	case "command":
		if len(parts) != 1 {
			return fmt.Errorf("command does not support nested keys")
		}
		backend.Command = value

	case "enabled":
		if len(parts) != 1 {
			return fmt.Errorf("enabled does not support nested keys")
		}
		backend.Enabled = value == "true" || value == "1" || value == "yes"

	case "options":
		if len(parts) < 2 {
			return fmt.Errorf("options requires an option name")
		}
		optionName := parts[1]
		if backend.Options == nil {
			backend.Options = make(map[string]string)
		}
		backend.Options[optionName] = value

	case "thresholds":
		if len(parts) < 2 {
			return fmt.Errorf("thresholds requires a field name")
		}
		if err := setBackendThreshold(&backend.Thresholds, parts[1:], value); err != nil {
			return err
		}

	case "models":
		if len(parts) < 2 {
			return fmt.Errorf("models requires a field name")
		}
		if err := setBackendModels(&backend.Models, parts[1:], value); err != nil {
			return err
		}

	default:
		return fmt.Errorf("unknown backend field: %q", parts[0])
	}

	cfg.Backends[backendName] = backend
	return nil
}

func setBackendThreshold(thresholds *ralph.BackendThresholds, parts []string, value string) error {
	switch parts[0] {
	case "max_requests_per_window":
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid max_requests_per_window: %w", err)
		}
		thresholds.MaxRequestsPerWindow = parsed
	case "max_cost_per_hour":
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return fmt.Errorf("invalid max_cost_per_hour: %w", err)
		}
		thresholds.MaxCostPerHour = parsed
	case "max_context_pct":
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid max_context_pct: %w", err)
		}
		thresholds.MaxContextPct = parsed
	case "max_consecutive_errors":
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid max_consecutive_errors: %w", err)
		}
		thresholds.MaxConsecutiveErrors = parsed
	default:
		return fmt.Errorf("unknown threshold field: %q", parts[0])
	}
	return nil
}

func setBackendModels(models *ralph.BackendModels, parts []string, value string) error {
	switch parts[0] {
	case "default":
		if len(parts) != 1 {
			return fmt.Errorf("models.default does not support nested keys")
		}
		models.Default = value
	case "rules":
		if len(parts) != 1 {
			return fmt.Errorf("models.rules does not support nested keys")
		}
		var rules []ralph.ModelRule
		if err := yaml.Unmarshal([]byte(value), &rules); err != nil {
			return fmt.Errorf("parsing model rules: %w", err)
		}
		models.Rules = rules
	default:
		return fmt.Errorf("unknown models field: %q", parts[0])
	}
	return nil
}
