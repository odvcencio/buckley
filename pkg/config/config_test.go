package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
)

func TestDefaultConfig(t *testing.T) {
	cfg := config.DefaultConfig()

	if cfg.Models.Planning == "" || cfg.Models.Execution == "" || cfg.Models.Review == "" {
		t.Fatalf("default models should be populated: %+v", cfg.Models)
	}
	if cfg.Personality.QuirkProbability <= 0 || cfg.Personality.QuirkProbability >= 1 {
		t.Fatalf("unexpected quirk probability: %f", cfg.Personality.QuirkProbability)
	}
	if cfg.Memory.AutoCompactThreshold <= 0 || cfg.Memory.AutoCompactThreshold > 1 {
		t.Fatalf("unexpected compaction threshold: %f", cfg.Memory.AutoCompactThreshold)
	}
}

func TestLoadHierarchy(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	t.Setenv("HOME", home)

	userCfgDir := filepath.Join(home, ".buckley")
	if err := os.MkdirAll(userCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir user config: %v", err)
	}
	userCfg := `
models:
  planning: user/planning
  execution: user/execution
`
	if err := os.WriteFile(filepath.Join(userCfgDir, "config.yaml"), []byte(userCfg), 0o644); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	projectCfgDir := filepath.Join(project, ".buckley")
	if err := os.MkdirAll(projectCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir project config: %v", err)
	}
	projectCfg := `
models:
  planning: project/planning
personality:
  quirk_probability: 0.2
`
	if err := os.WriteFile(filepath.Join(projectCfgDir, "config.yaml"), []byte(projectCfg), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	t.Chdir(project)

	t.Setenv("BUCKLEY_MODEL_REVIEW", "env/review")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}

	if cfg.Models.Planning != "project/planning" {
		t.Fatalf("expected project planning override, got %s", cfg.Models.Planning)
	}
	if cfg.Models.Execution != "user/execution" {
		t.Fatalf("expected user execution override, got %s", cfg.Models.Execution)
	}
	if cfg.Models.Review != "env/review" {
		t.Fatalf("expected env review override, got %s", cfg.Models.Review)
	}
	if cfg.Personality.QuirkProbability != 0.2 {
		t.Fatalf("expected project quirk probability, got %f", cfg.Personality.QuirkProbability)
	}
}

func TestInvalidTrustLevelFailsValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	project := t.TempDir()
	t.Chdir(project)

	t.Setenv("BUCKLEY_TRUST_LEVEL", "chaotic")

	if _, err := config.Load(); err == nil {
		t.Fatalf("expected config.Load to fail for invalid trust level")
	}
}

func TestInvalidExecutionModeFailsValidation(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Execution.Mode = "bad-mode"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation to fail for invalid execution mode")
	}

	cfg = config.DefaultConfig()
	cfg.Oneshot.Mode = "bad-mode"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation to fail for invalid oneshot mode")
	}
}

func TestEnvOverrideBatchEnabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Batch.Enabled = true

	t.Setenv("BUCKLEY_BATCH_ENABLED", "0")
	config.ApplyEnvOverridesForTest(cfg)
	if cfg.Batch.Enabled {
		t.Fatalf("expected BUCKLEY_BATCH_ENABLED=0 to disable batch execution")
	}

	t.Setenv("BUCKLEY_BATCH_ENABLED", "1")
	config.ApplyEnvOverridesForTest(cfg)
	if !cfg.Batch.Enabled {
		t.Fatalf("expected BUCKLEY_BATCH_ENABLED=1 to enable batch execution")
	}
}

func TestIPCRemoteBindRequiresAuthentication(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.IPC.Enabled = true
	cfg.IPC.Bind = "0.0.0.0:4488"
	cfg.IPC.RequireToken = false
	cfg.IPC.BasicAuthEnabled = false
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for remote ipc without auth")
	}

	cfg.IPC.RequireToken = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected remote ipc with require_token to validate, got %v", err)
	}

	cfg = config.DefaultConfig()
	cfg.IPC.Enabled = true
	cfg.IPC.Bind = "0.0.0.0:4488"
	cfg.IPC.BasicAuthEnabled = true
	cfg.IPC.BasicAuthUsername = "user"
	cfg.IPC.BasicAuthPassword = "pass"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected remote ipc with basic auth to validate, got %v", err)
	}

	cfg = config.DefaultConfig()
	cfg.IPC.Enabled = true
	cfg.IPC.Bind = "127.0.0.1:4488"
	cfg.IPC.RequireToken = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected loopback ipc without auth to validate, got %v", err)
	}
}

func TestWorktreesRootPathAllowsHomeExpansionWhenContainersEnabled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := config.DefaultConfig()
	cfg.Worktrees.UseContainers = true
	cfg.Worktrees.RootPath = "~/.buckley/worktrees"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected ~ expansion to validate, got %v", err)
	}
}

func TestLoadAlignsModelsToOpenAIWhenOpenRouterDisabled(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	t.Setenv("HOME", home)

	t.Chdir(project)

	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "test-openai")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}

	if cfg.Models.Planning != "openai/gpt-5.5-xhigh" {
		t.Fatalf("expected planning model to fall back to openai/gpt-5.5-xhigh, got %s", cfg.Models.Planning)
	}
	if cfg.Models.Execution != "openai/gpt-5.4-xhigh" {
		t.Fatalf("expected execution model to fall back to openai/gpt-5.4-xhigh, got %s", cfg.Models.Execution)
	}
	if cfg.Models.Review != "openai/gpt-5.5-xhigh" {
		t.Fatalf("expected review model to fall back to openai/gpt-5.5-xhigh, got %s", cfg.Models.Review)
	}
	if cfg.Models.Utility.Commit != "openai/gpt-5.4-mini-xhigh" {
		t.Fatalf("expected commit utility model to fall back to openai/gpt-5.4-mini-xhigh, got %s", cfg.Models.Utility.Commit)
	}
	if cfg.Models.DefaultProvider != "openai" {
		t.Fatalf("expected default provider to switch to openai, got %s", cfg.Models.DefaultProvider)
	}
}

func TestLoadPrefersOpenRouterWhenAvailable(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	t.Setenv("HOME", home)

	t.Chdir(project)

	t.Setenv("OPENROUTER_API_KEY", "test-openrouter")
	t.Setenv("OPENAI_API_KEY", "test-openai")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}

	defaultModel := config.DefaultConfig().Models.Planning
	if cfg.Models.Planning != defaultModel {
		t.Fatalf("expected planning model to remain %s, got %s", defaultModel, cfg.Models.Planning)
	}
	if cfg.Models.DefaultProvider != "openrouter" {
		t.Fatalf("expected default provider to remain openrouter, got %s", cfg.Models.DefaultProvider)
	}
}

func TestLoadReadsConfigEnvForOpenRouterKey(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("OPENROUTER_API_KEY", "")

	configDir := filepath.Join(home, ".buckley")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configEnv := "export OPENROUTER_API_KEY=\"env-file-key\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.env"), []byte(configEnv), 0o600); err != nil {
		t.Fatalf("write config.env: %v", err)
	}

	t.Chdir(project)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}

	if cfg.Providers.OpenRouter.APIKey != "env-file-key" {
		t.Fatalf("expected openrouter key from config.env, got %q", cfg.Providers.OpenRouter.APIKey)
	}
}

func TestEnvOverridesEnableProviders(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")
	t.Setenv("GOOGLE_API_KEY", "google-key")

	cfg := config.DefaultConfig()
	config.ApplyEnvOverridesForTest(cfg) // helper to expose env override path

	if !cfg.Providers.OpenAI.Enabled || cfg.Providers.OpenAI.APIKey != "openai-key" {
		t.Fatalf("openai env not applied: %+v", cfg.Providers.OpenAI)
	}
	if !cfg.Providers.Anthropic.Enabled || cfg.Providers.Anthropic.APIKey != "anthropic-key" {
		t.Fatalf("anthropic env not applied: %+v", cfg.Providers.Anthropic)
	}
	if !cfg.Providers.Google.Enabled || cfg.Providers.Google.APIKey != "google-key" {
		t.Fatalf("google env not applied: %+v", cfg.Providers.Google)
	}
}

func TestEnvOverridesConfigureCodexProvider(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("BUCKLEY_CODEX_ENABLED", "1")
	t.Setenv("BUCKLEY_CODEX_MODEL", "gpt-5.4-mini-xhigh")
	t.Setenv("BUCKLEY_CODEX_COMMAND", "/opt/bin/codex")

	cfg := config.DefaultConfig()
	config.ApplyEnvOverridesForTest(cfg)

	if !cfg.Providers.Codex.Enabled {
		t.Fatalf("codex provider should be enabled: %+v", cfg.Providers.Codex)
	}
	if cfg.Providers.Codex.Command != "/opt/bin/codex" {
		t.Fatalf("codex command=%q want /opt/bin/codex", cfg.Providers.Codex.Command)
	}
	if len(cfg.Providers.Codex.Models) != 1 || cfg.Providers.Codex.Models[0] != "codex/gpt-5.4-mini-xhigh" {
		t.Fatalf("codex models=%v want codex/gpt-5.4-mini-xhigh", cfg.Providers.Codex.Models)
	}
	if cfg.Models.DefaultProvider != "codex" {
		t.Fatalf("default provider=%q want codex", cfg.Models.DefaultProvider)
	}
	if cfg.Models.Execution != "codex/gpt-5.4-mini-xhigh" {
		t.Fatalf("execution model=%q want codex/gpt-5.4-mini-xhigh", cfg.Models.Execution)
	}
}

func TestLoadEnablesCodexFromDefaultProviderConfig(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("OPENROUTER_API_KEY", "test-openrouter")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	projectCfgDir := filepath.Join(project, ".buckley")
	if err := os.MkdirAll(projectCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir project config: %v", err)
	}
	projectCfg := `
models:
  default_provider: codex
`
	if err := os.WriteFile(filepath.Join(projectCfgDir, "config.yaml"), []byte(projectCfg), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	t.Chdir(project)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}

	if !cfg.Providers.Codex.Enabled {
		t.Fatalf("codex provider should be enabled from models.default_provider")
	}
	if cfg.Models.DefaultProvider != "codex" {
		t.Fatalf("default provider=%q want codex", cfg.Models.DefaultProvider)
	}
	if cfg.Models.Execution != "codex/gpt-5.4-mini-xhigh" {
		t.Fatalf("execution model=%q want codex/gpt-5.4-mini-xhigh", cfg.Models.Execution)
	}
}

func TestLoadEnablesCodexFromExecutionModelConfig(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	projectCfgDir := filepath.Join(project, ".buckley")
	if err := os.MkdirAll(projectCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir project config: %v", err)
	}
	projectCfg := `
models:
  execution: codex/gpt-5.4-mini-xhigh
`
	if err := os.WriteFile(filepath.Join(projectCfgDir, "config.yaml"), []byte(projectCfg), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	t.Chdir(project)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}

	if !cfg.Providers.Codex.Enabled {
		t.Fatalf("codex provider should be enabled from models.execution")
	}
	if cfg.Models.DefaultProvider != "codex" {
		t.Fatalf("default provider=%q want codex", cfg.Models.DefaultProvider)
	}
	if cfg.Models.Execution != "codex/gpt-5.4-mini-xhigh" {
		t.Fatalf("execution model=%q want codex/gpt-5.4-mini-xhigh", cfg.Models.Execution)
	}
	if cfg.Models.Planning != "codex/gpt-5.4-mini-xhigh" || cfg.Models.Review != "codex/gpt-5.4-mini-xhigh" {
		t.Fatalf("planning/review should fall back to codex/gpt-5.4-mini-xhigh, got %q/%q", cfg.Models.Planning, cfg.Models.Review)
	}
}

func TestReadyProvidersOrdering(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.Enabled = false
	cfg.Providers.OpenRouter.APIKey = ""
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "k1"
	cfg.Providers.Anthropic.Enabled = true
	cfg.Providers.Anthropic.APIKey = "k2"
	cfg.Providers.Codex.Enabled = true

	providers := cfg.Providers.ReadyProviders()
	if len(providers) != 3 || providers[0] != "openai" || providers[1] != "anthropic" || providers[2] != "codex" {
		t.Fatalf("unexpected ready providers: %v", providers)
	}
	if !cfg.Providers.HasReadyProvider() {
		t.Fatalf("expected HasReadyProvider to be true")
	}
}

func TestLoadProjectConfigCanDisableNetworkLogs(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()

	t.Setenv("HOME", home)

	projectCfgDir := filepath.Join(project, ".buckley")
	if err := os.MkdirAll(projectCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir project config: %v", err)
	}
	projectCfg := `
diagnostics:
  network_logs_enabled: false
`
	if err := os.WriteFile(filepath.Join(projectCfgDir, "config.yaml"), []byte(projectCfg), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	t.Chdir(project)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load returned error: %v", err)
	}
	if cfg.Diagnostics.NetworkLogsEnabled {
		t.Fatalf("expected network logs disabled from project config")
	}
}
