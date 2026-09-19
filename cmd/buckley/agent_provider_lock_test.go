package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

func TestProviderLockFromModel(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{model: "openrouter/z-ai/glm-5.2", want: "openrouter"},
		{model: "openai/gpt-5.4", want: "openai"},
		{model: "anthropic/claude-sonnet", want: "anthropic"},
		{model: "google/gemini-3-flash", want: "google"},
		{model: "ollama/llama3", want: "ollama"},
		{model: "openai_compatible/glm-5.3-flash", want: "openai_compatible"},
		{model: "litellm/model", want: "litellm"},
		{model: "codex/gpt-5.4-mini", want: "codex"},
		{model: " z-ai/glm-5.2 ", want: ""},
		{model: "vendor/model", want: ""},
		{model: "glm-5.3-flash", want: ""},
		{model: "OpenAI/gpt-5.4", want: ""},
		{model: "openai/", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := providerLockFromModel(tt.model); got != tt.want {
				t.Fatalf("providerLockFromModel(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestInitAgentRunDependenciesScopesProviderLockAndRestores(t *testing.T) {
	previousInit := initDependenciesFn
	previousOverride := modelOverrideFlag
	t.Cleanup(func() {
		initDependenciesFn = previousInit
		modelOverrideFlag = previousOverride
	})

	modelID := "openai_compatible/glm-5.3-flash"
	t.Run("success", func(t *testing.T) {
		seen := ""
		initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
			seen = activeAgentProviderLock()
			return nil, nil, nil, nil
		}
		if _, _, _, err := initAgentRunDependencies(modelID); err != nil {
			t.Fatalf("initAgentRunDependencies: %v", err)
		}
		if seen != "openai_compatible" {
			t.Fatalf("dependency hook saw provider lock %q, want openai_compatible", seen)
		}
		if got := activeAgentProviderLock(); got != "" {
			t.Fatalf("provider lock after successful init = %q, want empty", got)
		}
	})

	t.Run("error", func(t *testing.T) {
		sentinel := errors.New("dependency init stopped")
		seen := ""
		initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
			seen = activeAgentProviderLock()
			return nil, nil, nil, sentinel
		}
		if _, _, _, err := initAgentRunDependencies(modelID); !errors.Is(err, sentinel) {
			t.Fatalf("initAgentRunDependencies error = %v, want sentinel", err)
		}
		if seen != "openai_compatible" {
			t.Fatalf("dependency hook saw provider lock %q, want openai_compatible", seen)
		}
		if got := activeAgentProviderLock(); got != "" {
			t.Fatalf("provider lock after failed init = %q, want empty", got)
		}
	})

	t.Run("legacy model has no lock", func(t *testing.T) {
		seen := "sentinel"
		initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
			seen = activeAgentProviderLock()
			return nil, nil, nil, nil
		}
		if _, _, _, err := initAgentRunDependencies("z-ai/glm-5.2"); err != nil {
			t.Fatalf("initAgentRunDependencies: %v", err)
		}
		if seen != "" {
			t.Fatalf("legacy dependency hook saw provider lock %q, want empty", seen)
		}
		if got := activeAgentProviderLock(); got != "" {
			t.Fatalf("provider lock after legacy init = %q, want empty", got)
		}
	})
}

func TestRunAgentRunProviderLockIsVisibleToDependencyHook(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(`
version: buckley.agent/v1
name: exact-child
subagents:
  - name: reviewer
    model: openai_compatible/glm-5.3-flash
`), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	previousInit := initDependenciesFn
	previousOverride := modelOverrideFlag
	t.Cleanup(func() {
		initDependenciesFn = previousInit
		modelOverrideFlag = previousOverride
	})
	sentinel := errors.New("stop after inspecting provider lock")
	seen := ""
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		seen = activeAgentProviderLock()
		return nil, nil, nil, sentinel
	}

	err := runAgentRun([]string{path, "reviewer", "inspect this"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("runAgentRun error = %v, want sentinel", err)
	}
	if seen != "openai_compatible" {
		t.Fatalf("dependency hook saw provider lock %q, want openai_compatible", seen)
	}
	if got := activeAgentProviderLock(); got != "" {
		t.Fatalf("provider lock after runAgentRun error = %q, want empty", got)
	}
}

func TestApplyAgentProviderLock_NoLockPreservesLegacyConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.APIKey = "ambient-openrouter"
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.BaseURL = "https://particle.example/v1"
	cfg.Providers.OpenAICompatible.APIKey = "particle-key"
	providersBefore := cfg.Providers
	routingBefore := map[string]string{}
	for key, value := range cfg.Providers.ModelRouting {
		routingBefore[key] = value
	}

	applyAgentProviderLock(cfg)
	if !reflect.DeepEqual(cfg.Providers.OpenRouter, providersBefore.OpenRouter) ||
		!reflect.DeepEqual(cfg.Providers.OpenAICompatible, providersBefore.OpenAICompatible) ||
		!reflect.DeepEqual(cfg.Providers.ReadyProviders(), providersBefore.ReadyProviders()) {
		t.Fatalf("no-lock provider config changed: before=%+v after=%+v", providersBefore, cfg.Providers)
	}
	if !reflect.DeepEqual(cfg.Providers.ModelRouting, routingBefore) {
		t.Fatalf("no-lock model routing changed: before=%v after=%v", routingBefore, cfg.Providers.ModelRouting)
	}
}

func TestInitAgentRunDependencies_LocksCatalogInitializationToExactProvider(t *testing.T) {
	openRouterCalls := 0
	openRouter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openRouterCalls++
		http.Error(w, "unrelated provider must not be initialized", http.StatusInternalServerError)
	}))
	t.Cleanup(openRouter.Close)

	compatible := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data":[{"id":"glm-5.3-flash"}]}`)
	}))
	t.Cleanup(compatible.Close)

	configDir := t.TempDir()
	configPathForTest := filepath.Join(configDir, "config.yaml")
	configText := fmt.Sprintf(`
providers:
  openrouter:
    base_url: %q
  openai_compatible:
    enabled: true
    base_url: %q
    api_key: compatible-key
    models:
      - glm-5.3-flash
models:
  planning: openai_compatible/glm-5.3-flash
  execution: openai_compatible/glm-5.3-flash
  review: openai_compatible/glm-5.3-flash
  default_provider: openrouter
`, openRouter.URL, compatible.URL)
	if err := os.WriteFile(configPathForTest, []byte(configText), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	previousInit := initDependenciesFn
	previousConfigPath := configPath
	previousOverride := modelOverrideFlag
	previousAgentProfile := agentProfileFlag
	previousEncoding := encodingOverrideFlag
	previousTerminal := stdinIsTerminalFn
	t.Cleanup(func() {
		initDependenciesFn = previousInit
		configPath = previousConfigPath
		modelOverrideFlag = previousOverride
		agentProfileFlag = previousAgentProfile
		encodingOverrideFlag = previousEncoding
		stdinIsTerminalFn = previousTerminal
	})

	dataDir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BUCKLEY_DB_PATH", filepath.Join(dataDir, "buckley.db"))
	t.Setenv("OPENROUTER_API_KEY", "ambient-openrouter-key")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("BUCKLEY_OLLAMA_ENABLED", "")
	t.Setenv("BUCKLEY_OPENAI_COMPATIBLE_ENABLED", "")
	t.Setenv("BUCKLEY_OPENAI_COMPATIBLE_BASE_URL", "")
	t.Setenv("BUCKLEY_OPENAI_COMPATIBLE_API_KEY", "")
	t.Chdir(configDir)
	stdinIsTerminalFn = func() bool { return false }
	configPath = configPathForTest
	modelOverrideFlag = "openai_compatible/glm-5.3-flash"
	agentProfileFlag = ""
	encodingOverrideFlag = ""
	initDependenciesFn = initDependencies
	loadedCfg, err := config.LoadFromPath(configPathForTest)
	if err != nil {
		t.Fatalf("load pre-lock config: %v", err)
	}
	if got := loadedCfg.Providers.ReadyProviders(); !reflect.DeepEqual(got, []string{"openrouter", "openai_compatible"}) {
		t.Fatalf("pre-lock ready providers = %v, want openrouter and openai_compatible", got)
	}

	cfg, mgr, store, err := initAgentRunDependencies(modelOverrideFlag)
	if err != nil {
		t.Fatalf("initAgentRunDependencies: %v", err)
	}
	if store == nil {
		t.Fatal("initAgentRunDependencies returned nil store")
	}
	t.Cleanup(func() { _ = store.Close() })

	if got := cfg.Providers.ReadyProviders(); !reflect.DeepEqual(got, []string{"openai_compatible"}) {
		t.Fatalf("ready providers = %v, want only openai_compatible", got)
	}
	if cfg.Models.DefaultProvider != "openai_compatible" {
		t.Fatalf("default provider = %q, want openai_compatible", cfg.Models.DefaultProvider)
	}
	if got := mgr.ProviderIDForModel("openai_compatible/glm-5.3-flash"); got != "openai_compatible" {
		t.Fatalf("provider for exact model = %q, want openai_compatible", got)
	}
	if openRouterCalls != 0 {
		t.Fatalf("OpenRouter catalog requests = %d, want 0", openRouterCalls)
	}
	if got := activeAgentProviderLock(); got != "" {
		t.Fatalf("provider lock after initialized run = %q, want empty", got)
	}
}
