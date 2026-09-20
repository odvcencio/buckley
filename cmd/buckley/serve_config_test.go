package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setServeConfigPathForTest(t *testing.T, path string) {
	t.Helper()
	previous := configPath
	configPath = path
	t.Cleanup(func() { configPath = previous })
}

func TestServeLoadConfig_ExplicitPathWins(t *testing.T) {
	t.Chdir(t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	userConfigDir := filepath.Join(home, ".buckley")
	if err := os.MkdirAll(userConfigDir, 0o700); err != nil {
		t.Fatalf("create user config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userConfigDir, "config.yaml"), []byte("models:\n  execution: anthropic/claude-sonnet-4-5\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	explicitConfigPath := filepath.Join(t.TempDir(), "explicit.yaml")
	explicitYAML := `models:
  planning: openai_compatible/glm-5.3-flash
  execution: openai_compatible/glm-5.3-flash
  review: openai_compatible/glm-5.3-flash
  utility:
    commit: openai_compatible/glm-5.3-flash
    pr: openai_compatible/glm-5.3-flash
    compaction: openai_compatible/glm-5.3-flash
    todo_plan: openai_compatible/glm-5.3-flash
providers:
  openrouter:
    enabled: false
  openai_compatible:
    enabled: true
    base_url: http://127.0.0.1:9/v1
    api_key: dummy-key
    models:
      - openai_compatible/glm-5.3-flash
`
	if err := os.WriteFile(explicitConfigPath, []byte(explicitYAML), 0o600); err != nil {
		t.Fatalf("write explicit config: %v", err)
	}
	setServeConfigPathForTest(t, explicitConfigPath)

	cfg, err := serveLoadConfigFn()
	if err != nil {
		t.Fatalf("serveLoadConfigFn() error = %v", err)
	}
	if cfg.Models.Execution != "openai_compatible/glm-5.3-flash" {
		t.Fatalf("models.execution = %q, want explicit openai_compatible model", cfg.Models.Execution)
	}
	if cfg.Providers.OpenRouter.Enabled {
		t.Fatal("providers.openrouter.enabled = true, want false from explicit config")
	}
	if !cfg.Providers.OpenAICompatible.Enabled {
		t.Fatal("providers.openai_compatible.enabled = false, want true from explicit config")
	}
}

func TestServeLoadConfig_ExplicitPathErrorsWithoutFallback(t *testing.T) {
	t.Chdir(t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	userConfigDir := filepath.Join(home, ".buckley")
	if err := os.MkdirAll(userConfigDir, 0o700); err != nil {
		t.Fatalf("create user config dir: %v", err)
	}
	// A loadable hierarchy config exists; an explicit --config failure must
	// not silently fall back to it.
	if err := os.WriteFile(filepath.Join(userConfigDir, "config.yaml"), []byte("models:\n  curated:\n    - hierarchy-curated-model\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	brokenConfigPath := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(brokenConfigPath, []byte("models: [unclosed\n"), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}

	tests := []struct {
		name string
		path string
	}{
		{name: "missing file", path: filepath.Join(t.TempDir(), "absent.yaml")},
		{name: "invalid yaml", path: brokenConfigPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setServeConfigPathForTest(t, tt.path)
			cfg, err := serveLoadConfigFn()
			if err == nil {
				t.Fatalf("serveLoadConfigFn() = %+v, want error for %q", cfg, tt.path)
			}
			if !strings.Contains(err.Error(), "loading config from") {
				t.Fatalf("error = %v, want explicit path failure without hierarchy fallback", err)
			}
		})
	}
}

func TestServeLoadConfig_EmptyPathUsesDefaultHierarchy(t *testing.T) {
	t.Chdir(t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	userConfigDir := filepath.Join(home, ".buckley")
	if err := os.MkdirAll(userConfigDir, 0o700); err != nil {
		t.Fatalf("create user config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userConfigDir, "config.yaml"), []byte("models:\n  curated:\n    - hierarchy-curated-model\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}
	setServeConfigPathForTest(t, "")

	cfg, err := serveLoadConfigFn()
	if err != nil {
		t.Fatalf("serveLoadConfigFn() error = %v", err)
	}
	found := false
	for _, modelID := range cfg.Models.Curated {
		if modelID == "hierarchy-curated-model" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("models.curated = %v, want value from default hierarchy user config", cfg.Models.Curated)
	}
}
