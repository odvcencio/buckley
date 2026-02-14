package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/ralph"
)

// setOverrideValue sets a value in the OverrideConfig.
func setOverrideValue(override *ralph.OverrideConfig, parts []string, value string) error {
	if len(parts) == 0 {
		return fmt.Errorf("override requires a field name")
	}

	switch parts[0] {
	case "paused":
		if len(parts) != 1 {
			return fmt.Errorf("paused does not support nested keys")
		}
		override.Paused = value == "true" || value == "1" || value == "yes"

	case "next_action":
		if len(parts) != 1 {
			return fmt.Errorf("next_action does not support nested keys")
		}
		override.NextAction = value

	case "backend_options":
		if len(parts) < 3 {
			return fmt.Errorf("backend_options requires backend.option path")
		}
		backendName := parts[1]
		optionName := parts[2]
		if override.BackendOptions == nil {
			override.BackendOptions = make(map[string]map[string]string)
		}
		if override.BackendOptions[backendName] == nil {
			override.BackendOptions[backendName] = make(map[string]string)
		}
		override.BackendOptions[backendName][optionName] = value

	default:
		return fmt.Errorf("unknown override field: %q", parts[0])
	}

	return nil
}

func setRotationValue(rotation *ralph.RotationConfig, parts []string, value string) error {
	if len(parts) == 0 {
		return fmt.Errorf("rotation requires a field name")
	}

	switch parts[0] {
	case "mode":
		if len(parts) != 1 {
			return fmt.Errorf("rotation.mode does not support nested keys")
		}
		rotation.Mode = value
	case "interval":
		if len(parts) != 1 {
			return fmt.Errorf("rotation.interval does not support nested keys")
		}
		interval, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid rotation interval: %w", err)
		}
		rotation.Interval = interval
	case "order":
		if len(parts) != 1 {
			return fmt.Errorf("rotation.order does not support nested keys")
		}
		rotation.Order = splitCSV(value)
	default:
		return fmt.Errorf("unknown rotation field: %q", parts[0])
	}

	return nil
}

func setMemoryValue(memory *ralph.MemoryConfig, parts []string, value string) error {
	if len(parts) == 0 {
		return fmt.Errorf("memory requires a field name")
	}

	switch parts[0] {
	case "enabled":
		if len(parts) != 1 {
			return fmt.Errorf("memory.enabled does not support nested keys")
		}
		memory.Enabled = parseBool(value)
	case "summary_interval":
		if len(parts) != 1 {
			return fmt.Errorf("memory.summary_interval does not support nested keys")
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid summary_interval: %w", err)
		}
		memory.SummaryInterval = parsed
	case "summary_model":
		if len(parts) != 1 {
			return fmt.Errorf("memory.summary_model does not support nested keys")
		}
		memory.SummaryModel = value
	case "retention_days":
		if len(parts) != 1 {
			return fmt.Errorf("memory.retention_days does not support nested keys")
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid retention_days: %w", err)
		}
		memory.RetentionDays = parsed
	case "max_raw_turns":
		if len(parts) != 1 {
			return fmt.Errorf("memory.max_raw_turns does not support nested keys")
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid max_raw_turns: %w", err)
		}
		memory.MaxRawTurns = parsed
	default:
		return fmt.Errorf("unknown memory field: %q", parts[0])
	}

	return nil
}

func setContextProcessingValue(cfg *ralph.ContextProcessingConfig, parts []string, value string) error {
	if len(parts) == 0 {
		return fmt.Errorf("context_processing requires a field name")
	}

	switch parts[0] {
	case "enabled":
		if len(parts) != 1 {
			return fmt.Errorf("context_processing.enabled does not support nested keys")
		}
		cfg.Enabled = parseBool(value)
	case "model":
		if len(parts) != 1 {
			return fmt.Errorf("context_processing.model does not support nested keys")
		}
		cfg.Model = value
	case "max_output_tokens":
		if len(parts) != 1 {
			return fmt.Errorf("context_processing.max_output_tokens does not support nested keys")
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid max_output_tokens: %w", err)
		}
		cfg.MaxOutputTokens = parsed
	case "budget_pct":
		if len(parts) != 1 {
			return fmt.Errorf("context_processing.budget_pct does not support nested keys")
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid budget_pct: %w", err)
		}
		cfg.BudgetPct = parsed
	default:
		return fmt.Errorf("unknown context_processing field: %q", parts[0])
	}

	return nil
}
