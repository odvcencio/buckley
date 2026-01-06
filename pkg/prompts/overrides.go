package prompts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var supportedPrompts = map[string]struct{}{
	"planning":  {},
	"execution": {},
	"review":    {},
	"commit":    {},
	"pr":        {},
}

// PromptInfo describes the default/override state of a prompt template.
type PromptInfo struct {
	Kind         string   `json:"kind"`
	Default      string   `json:"default"`
	Override     string   `json:"override"`
	Effective    string   `json:"effective"`
	Overridden   bool     `json:"overridden"`
	Placeholders []string `json:"placeholders"`
}

func resolvePrompt(kind string, defaultPrompt string, now time.Time) string {
	override := resolveOverride(kind)
	if override == "" {
		return defaultPrompt
	}
	return applyPlaceholders(override, defaultPrompt, now)
}

func resolveOverride(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return ""
	}

	envKey := promptEnvKey(kind)
	if override := strings.TrimSpace(os.Getenv(envKey)); override != "" {
		return override
	}
	if path := strings.TrimSpace(os.Getenv(envKey + "_FILE")); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			override := string(data)
			if strings.TrimSpace(override) != "" {
				return override
			}
		}
	}

	override, err := readOverride(kind)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(override)
}

func promptEnvKey(kind string) string {
	return "BUCKLEY_PROMPT_" + strings.ToUpper(strings.TrimSpace(kind))
}

func applyPlaceholders(template string, defaultPrompt string, now time.Time) string {
	content := strings.ReplaceAll(template, "{{CURRENT_TIME}}", now.Format(time.RFC3339))
	content = strings.ReplaceAll(content, "{{DEFAULT_PROMPT}}", defaultPrompt)
	return content
}

func readOverride(kind string) (string, error) {
	if !isSupportedPrompt(kind) {
		return "", fmt.Errorf("unknown prompt kind: %s", kind)
	}
	path, err := overridePath(kind)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SaveOverride persists the override content for a prompt kind.
func SaveOverride(kind string, content string) error {
	if !isSupportedPrompt(kind) {
		return fmt.Errorf("unknown prompt kind: %s", kind)
	}
	dir, err := overrideDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, kind+".md")
	return os.WriteFile(path, []byte(content), 0o644)
}

// DeleteOverride removes a stored override.
func DeleteOverride(kind string) error {
	if !isSupportedPrompt(kind) {
		return fmt.Errorf("unknown prompt kind: %s", kind)
	}
	path, err := overridePath(kind)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ListPromptInfo returns prompt metadata for UI/API consumption.
func ListPromptInfo(now time.Time) ([]PromptInfo, error) {
	kinds := []string{"planning", "execution", "review", "commit", "pr"}
	result := make([]PromptInfo, 0, len(kinds))
	for _, kind := range kinds {
		info, err := PromptInfoFor(kind, now)
		if err != nil {
			return nil, err
		}
		result = append(result, info)
	}
	return result, nil
}

// PromptInfoFor returns prompt metadata for a single kind.
func PromptInfoFor(kind string, now time.Time) (PromptInfo, error) {
	if !isSupportedPrompt(kind) {
		return PromptInfo{}, fmt.Errorf("unknown prompt kind: %s", kind)
	}
	defaultPrompt, err := defaultPromptFor(kind, now)
	if err != nil {
		return PromptInfo{}, err
	}

	override := resolveOverride(kind)
	effective := resolvePrompt(kind, defaultPrompt, now)
	overridden := strings.TrimSpace(override) != ""
	return PromptInfo{
		Kind:         kind,
		Default:      defaultPrompt,
		Override:     override,
		Effective:    effective,
		Overridden:   overridden,
		Placeholders: []string{"{{CURRENT_TIME}}", "{{DEFAULT_PROMPT}}"},
	}, nil
}

func defaultPromptFor(kind string, now time.Time) (string, error) {
	switch kind {
	case "planning":
		return planningDefault(now, nil), nil
	case "execution":
		return executionDefault(now, nil), nil
	case "review":
		return reviewDefault(now, nil), nil
	case "commit":
		return commitDefault(now), nil
	case "pr":
		return prDefault(now), nil
	default:
		return "", fmt.Errorf("unknown prompt kind: %s", kind)
	}
}

func overridePath(kind string) (string, error) {
	dir, err := overrideDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, kind+".md"), nil
}

func overrideDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".buckley", "prompts"), nil
}

func isSupportedPrompt(kind string) bool {
	_, ok := supportedPrompts[kind]
	return ok
}
