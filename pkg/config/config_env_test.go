package config

import "testing"

// TestApplyEnvOverridesOneDirectionalToggles asserts BUCKLEY_DISABLE_TOON
// and BUCKLEY_DISABLE_NETWORK_LOGS only ever turn their field off: a
// "false" value must not re-enable it, matching envUseToon/envNetworkLogs.
func TestApplyEnvOverridesOneDirectionalToggles(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Encoding.UseToon = true
	cfg.Diagnostics.NetworkLogsEnabled = true

	t.Setenv("BUCKLEY_DISABLE_TOON", "false")
	t.Setenv("BUCKLEY_DISABLE_NETWORK_LOGS", "false")
	ApplyEnvOverridesForTest(cfg)
	if !cfg.Encoding.UseToon {
		t.Errorf("expected BUCKLEY_DISABLE_TOON=false to be a no-op, got UseToon=%v", cfg.Encoding.UseToon)
	}
	if !cfg.Diagnostics.NetworkLogsEnabled {
		t.Errorf("expected BUCKLEY_DISABLE_NETWORK_LOGS=false to be a no-op, got NetworkLogsEnabled=%v", cfg.Diagnostics.NetworkLogsEnabled)
	}

	t.Setenv("BUCKLEY_DISABLE_TOON", "true")
	t.Setenv("BUCKLEY_DISABLE_NETWORK_LOGS", "true")
	ApplyEnvOverridesForTest(cfg)
	if cfg.Encoding.UseToon {
		t.Errorf("expected BUCKLEY_DISABLE_TOON=true to disable toon encoding")
	}
	if cfg.Diagnostics.NetworkLogsEnabled {
		t.Errorf("expected BUCKLEY_DISABLE_NETWORK_LOGS=true to disable network logs")
	}
}

// TestApplyEnvOverridesLiteLLMFallbackAsymmetry asserts the unprefixed
// LITELLM_BASE_URL fallback does not enable the provider, while the
// unprefixed LITELLM_API_KEY fallback does -- matching the asymmetry in
// the pre-reflection envLiteLLMProvider logic.
func TestApplyEnvOverridesLiteLLMFallbackAsymmetry(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Providers.LiteLLM.BaseURL = "" // fallback only applies when unset
	t.Setenv("LITELLM_BASE_URL", "http://legacy:4000")
	ApplyEnvOverridesForTest(cfg)
	if cfg.Providers.LiteLLM.BaseURL != "http://legacy:4000" {
		t.Fatalf("expected LITELLM_BASE_URL to set base_url, got %q", cfg.Providers.LiteLLM.BaseURL)
	}
	if cfg.Providers.LiteLLM.Enabled {
		t.Errorf("expected the unprefixed LITELLM_BASE_URL fallback to NOT enable litellm")
	}

	cfg2 := DefaultConfig()
	t.Setenv("LITELLM_API_KEY", "legacy-key")
	ApplyEnvOverridesForTest(cfg2)
	if cfg2.Providers.LiteLLM.APIKey != "legacy-key" {
		t.Fatalf("expected LITELLM_API_KEY to set api_key, got %q", cfg2.Providers.LiteLLM.APIKey)
	}
	if !cfg2.Providers.LiteLLM.Enabled {
		t.Errorf("expected the unprefixed LITELLM_API_KEY fallback to enable litellm")
	}
}

// TestApplyEnvOverridesExperimentPositiveOnlyGuards asserts a zero or
// negative value for the four guarded experiment env vars is treated as
// unset (the default survives), matching the pre-reflection `n > 0` /
// `d > 0` checks.
func TestApplyEnvOverridesExperimentPositiveOnlyGuards(t *testing.T) {
	cfg := DefaultConfig()
	wantConcurrent := cfg.Experiment.MaxConcurrent
	wantTimeout := cfg.Experiment.DefaultTimeout
	wantCost := cfg.Experiment.MaxCostPerRun
	wantTokens := cfg.Experiment.MaxTokensPerRun

	t.Setenv("BUCKLEY_EXPERIMENT_MAX_CONCURRENT", "0")
	t.Setenv("BUCKLEY_EXPERIMENT_DEFAULT_TIMEOUT", "0s")
	t.Setenv("BUCKLEY_EXPERIMENT_MAX_COST_PER_RUN", "-1")
	t.Setenv("BUCKLEY_EXPERIMENT_MAX_TOKENS_PER_RUN", "-5")
	ApplyEnvOverridesForTest(cfg)

	if cfg.Experiment.MaxConcurrent != wantConcurrent {
		t.Errorf("expected max_concurrent=0 to be a no-op, got %d", cfg.Experiment.MaxConcurrent)
	}
	if cfg.Experiment.DefaultTimeout != wantTimeout {
		t.Errorf("expected default_timeout=0s to be a no-op, got %v", cfg.Experiment.DefaultTimeout)
	}
	if cfg.Experiment.MaxCostPerRun != wantCost {
		t.Errorf("expected negative max_cost_per_run to be a no-op, got %v", cfg.Experiment.MaxCostPerRun)
	}
	if cfg.Experiment.MaxTokensPerRun != wantTokens {
		t.Errorf("expected negative max_tokens_per_run to be a no-op, got %d", cfg.Experiment.MaxTokensPerRun)
	}

	t.Setenv("BUCKLEY_EXPERIMENT_MAX_CONCURRENT", "9")
	t.Setenv("BUCKLEY_EXPERIMENT_DEFAULT_TIMEOUT", "1h")
	t.Setenv("BUCKLEY_EXPERIMENT_MAX_COST_PER_RUN", "3.5")
	t.Setenv("BUCKLEY_EXPERIMENT_MAX_TOKENS_PER_RUN", "12345")
	ApplyEnvOverridesForTest(cfg)

	if cfg.Experiment.MaxConcurrent != 9 {
		t.Errorf("expected max_concurrent=9 to apply, got %d", cfg.Experiment.MaxConcurrent)
	}
	if cfg.Experiment.DefaultTimeout.String() != "1h0m0s" {
		t.Errorf("expected default_timeout=1h to apply, got %v", cfg.Experiment.DefaultTimeout)
	}
	if cfg.Experiment.MaxCostPerRun != 3.5 {
		t.Errorf("expected max_cost_per_run=3.5 to apply, got %v", cfg.Experiment.MaxCostPerRun)
	}
	if cfg.Experiment.MaxTokensPerRun != 12345 {
		t.Errorf("expected max_tokens_per_run=12345 to apply, got %d", cfg.Experiment.MaxTokensPerRun)
	}
}

// TestApplyEnvOverridesDockerNetworkPointer asserts
// BUCKLEY_DOCKER_SANDBOX_NETWORK sets a distinct *bool, not sharing
// storage with other test runs' values.
func TestApplyEnvOverridesDockerNetworkPointer(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Sandbox.DockerSandbox.NetworkEnabled != nil {
		t.Fatalf("expected NetworkEnabled nil by default")
	}
	t.Setenv("BUCKLEY_DOCKER_SANDBOX_NETWORK", "false")
	ApplyEnvOverridesForTest(cfg)
	if cfg.Sandbox.DockerSandbox.NetworkEnabled == nil || *cfg.Sandbox.DockerSandbox.NetworkEnabled {
		t.Fatalf("expected NetworkEnabled=false to be set explicitly, got %v", cfg.Sandbox.DockerSandbox.NetworkEnabled)
	}
}

// TestApplyEnvOverridesIPCBasicAuthImplicitEnable asserts setting both
// basic-auth env vars enables basic auth even without
// BUCKLEY_BASIC_AUTH_ENABLED, and that a lone username does not.
func TestApplyEnvOverridesIPCBasicAuthImplicitEnable(t *testing.T) {
	cfg := DefaultConfig()
	t.Setenv("BUCKLEY_BASIC_AUTH_USER", "u")
	ApplyEnvOverridesForTest(cfg)
	if cfg.IPC.BasicAuthEnabled {
		t.Errorf("expected a lone basic-auth username to not enable basic auth")
	}

	cfg2 := DefaultConfig()
	t.Setenv("BUCKLEY_BASIC_AUTH_PASSWORD", "p")
	ApplyEnvOverridesForTest(cfg2)
	if !cfg2.IPC.BasicAuthEnabled {
		t.Errorf("expected username+password (username from prior config layer) to enable basic auth")
	}
}

// TestApplyEnvOverridesGitCloneCSVLists asserts the three git_clone list
// env vars split on commas and trim each element.
func TestApplyEnvOverridesGitCloneCSVLists(t *testing.T) {
	cfg := DefaultConfig()
	t.Setenv("BUCKLEY_GIT_ALLOWED_SCHEMES", "https, ssh ,git")
	t.Setenv("BUCKLEY_GIT_ALLOWED_HOSTS", "github.com,gitlab.com")
	t.Setenv("BUCKLEY_GIT_DENIED_HOSTS", " evil.example ")
	ApplyEnvOverridesForTest(cfg)

	wantSchemes := []string{"https", "ssh", "git"}
	if len(cfg.GitClone.AllowedSchemes) != len(wantSchemes) {
		t.Fatalf("expected allowed_schemes %v, got %v", wantSchemes, cfg.GitClone.AllowedSchemes)
	}
	for i, want := range wantSchemes {
		if cfg.GitClone.AllowedSchemes[i] != want {
			t.Errorf("allowed_schemes[%d] = %q, want %q", i, cfg.GitClone.AllowedSchemes[i], want)
		}
	}
	if len(cfg.GitClone.AllowedHosts) != 2 || cfg.GitClone.AllowedHosts[0] != "github.com" {
		t.Errorf("expected allowed_hosts to split, got %v", cfg.GitClone.AllowedHosts)
	}
	if len(cfg.GitClone.DeniedHosts) != 1 || cfg.GitClone.DeniedHosts[0] != "evil.example" {
		t.Errorf("expected denied_hosts to trim, got %v", cfg.GitClone.DeniedHosts)
	}
}

// TestApplyEnvOverridesOpenRouterHasNoEnabledVar asserts OPENROUTER_API_KEY
// only ever sets the API key, never toggling Enabled (unlike the other
// three cloud providers), matching envOpenRouterProvider.
func TestApplyEnvOverridesOpenRouterHasNoEnabledVar(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Providers.OpenRouter.Enabled = false
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	ApplyEnvOverridesForTest(cfg)
	if cfg.Providers.OpenRouter.APIKey != "or-key" {
		t.Fatalf("expected OPENROUTER_API_KEY to set api_key, got %q", cfg.Providers.OpenRouter.APIKey)
	}
	if cfg.Providers.OpenRouter.Enabled {
		t.Errorf("expected OPENROUTER_API_KEY to leave Enabled untouched")
	}
}
