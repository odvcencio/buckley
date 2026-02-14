package main

import (
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/ralph"
)

func setControlConfigValue(cfg *ralph.ControlConfig, kv string) error {
	// Split on first '='.
	idx := -1
	for i, c := range kv {
		if c == '=' {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("invalid format: expected KEY=VALUE, got %q", kv)
	}

	key := kv[:idx]
	value := kv[idx+1:]

	if key == "" {
		return fmt.Errorf("empty key in %q", kv)
	}

	// Parse the dot-separated path.
	parts := splitDotPath(key)

	switch parts[0] {
	case "mode":
		if len(parts) != 1 {
			return fmt.Errorf("mode does not support nested keys")
		}
		cfg.Mode = value

	case "rotation":
		return setRotationValue(&cfg.Rotation, parts[1:], value)

	case "memory":
		return setMemoryValue(&cfg.Memory, parts[1:], value)

	case "context_processing":
		return setContextProcessingValue(&cfg.ContextProcessing, parts[1:], value)

	case "override":
		return setOverrideValue(&cfg.Override, parts[1:], value)

	case "backends":
		if len(parts) < 2 {
			return fmt.Errorf("backends requires at least a backend name")
		}
		return setBackendValue(cfg, parts[1], parts[2:], value)

	default:
		return fmt.Errorf("unknown top-level key: %q", parts[0])
	}

	return nil
}

// splitDotPath splits a dot-separated path into parts.
func splitDotPath(path string) []string {
	var parts []string
	var current string
	for _, c := range path {
		if c == '.' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

func splitCSV(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseBool(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "true" || value == "1" || value == "yes"
}
