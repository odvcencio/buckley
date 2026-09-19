package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
)

type oneshotProfileReasoningChecker map[string]bool

func (c oneshotProfileReasoningChecker) SupportsReasoning(modelID string) bool {
	return c[modelID]
}

func TestResolveOneshotBackendPrecedence(t *testing.T) {
	t.Setenv(envOneshotBackend, "claude")
	t.Setenv(envCommitBackend, "codex")

	got, err := resolveOneshotBackend("commit", "")
	if err != nil {
		t.Fatalf("resolveOneshotBackend: %v", err)
	}
	if got != oneshot.CLIBackendCodex {
		t.Fatalf("backend = %q, want codex", got)
	}

	got, err = resolveOneshotBackend("pr", "")
	if err != nil {
		t.Fatalf("resolveOneshotBackend: %v", err)
	}
	if got != oneshot.CLIBackendClaude {
		t.Fatalf("backend = %q, want claude", got)
	}

	got, err = resolveOneshotBackend("commit", "api")
	if err != nil {
		t.Fatalf("resolveOneshotBackend: %v", err)
	}
	if got != oneshotBackendAPI {
		t.Fatalf("backend = %q, want api", got)
	}
}

func TestResolveOneshotBackendRejectsInvalid(t *testing.T) {
	if _, err := resolveOneshotBackend("commit", "wat"); err == nil {
		t.Fatal("expected invalid backend error")
	}
}

func TestResolveCommitModelIDUsesUtilityOnlyForAPI(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.Utility.Commit = "openai/gpt-test"

	if got := resolveCommitModelID("", cfg, oneshotBackendAPI); got != "openai/gpt-test" {
		t.Fatalf("API model = %q, want utility commit model", got)
	}
	if got := resolveCommitModelID("", cfg, oneshot.CLIBackendCodex); got != "gpt-5.4-mini" {
		t.Fatalf("CLI model = %q, want gpt-5.4-mini default", got)
	}
	if got := resolveCommitModelID("openai/gpt-explicit", cfg, oneshot.CLIBackendCodex); got != "gpt-explicit" {
		t.Fatalf("explicit CLI model = %q, want stripped provider prefix", got)
	}
}

func TestCLICommandForBackendUsesEnvOverride(t *testing.T) {
	t.Setenv(envCodexCommand, "/opt/bin/codex")
	t.Setenv(envClaudeCommand, "/opt/bin/claude")

	if got := cliCommandForBackend(oneshot.CLIBackendCodex); got != "/opt/bin/codex" {
		t.Fatalf("codex command = %q", got)
	}
	if got := cliCommandForBackend(oneshot.CLIBackendClaude); got != "/opt/bin/claude" {
		t.Fatalf("claude command = %q", got)
	}
}

func TestResolveOneshotRequestProfileCommit(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.Reasoning = "auto"

	profile := resolveOneshotRequestProfile("commit", cfg, oneshotProfileReasoningChecker{"reasoning-model": true}, "reasoning-model")
	assertOneshotProfile(t, profile, 4096, true, 0.2, &model.ReasoningConfig{Effort: "low"})
}

func TestResolveOneshotRequestProfilePR(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.Reasoning = "auto"

	profile := resolveOneshotRequestProfile("pr", cfg, oneshotProfileReasoningChecker{"reasoning-model": true}, "reasoning-model")
	assertOneshotProfile(t, profile, 8192, true, 0.2, &model.ReasoningConfig{Effort: "medium"})
}

func TestResolveOneshotRequestProfileDisablesReasoningWhenConfiguredOff(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.Reasoning = "off"

	profile := resolveOneshotRequestProfile("commit", cfg, oneshotProfileReasoningChecker{"reasoning-model": true}, "reasoning-model")
	if profile.Reasoning == nil || profile.Reasoning.Enabled == nil || *profile.Reasoning.Enabled {
		t.Fatalf("Reasoning = %+v, want explicit enabled=false", profile.Reasoning)
	}
	if profile.Reasoning.Effort != "" {
		t.Fatalf("Reasoning.Effort = %q, want empty when disabled", profile.Reasoning.Effort)
	}
}

func TestResolveOneshotRequestProfileOmitsReasoningWhenUnsupported(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.Reasoning = "auto"

	profile := resolveOneshotRequestProfile("commit", cfg, oneshotProfileReasoningChecker{}, "reasoning-model")
	if profile.Reasoning != nil {
		t.Fatalf("Reasoning = %+v, want nil for unsupported model", profile.Reasoning)
	}
	if profile.MaxOutputTokens != 4096 || !profile.RequireTool {
		t.Fatalf("commit transport profile was not preserved without reasoning: %+v", profile)
	}
}

func TestNewOneshotToolInvokerWarnsOnceForUnknownReasoningMetadata(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Models.Reasoning = "auto"
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.BaseURL = "https://example.invalid/v1"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	stderr := captureOneshotStderr(t, func() {
		invoker, err := newOneshotToolInvoker(oneshotBackendAPI, "commit", "future\nmodel", cfg, mgr, nil)
		if err != nil {
			t.Fatalf("newOneshotToolInvoker: %v", err)
		}
		if invoker == nil {
			t.Fatal("newOneshotToolInvoker returned nil invoker")
		}
	})
	if !strings.Contains(stderr, "buckley: reasoning disabled") || !strings.Contains(stderr, "capability metadata is unavailable") {
		t.Fatalf("stderr = %q, want unknown metadata diagnostic", stderr)
	}
	if strings.Contains(stderr, "\nmodel") {
		t.Fatalf("stderr contains unsanitized model label: %q", stderr)
	}
	if strings.Count(stderr, "reasoning disabled") != 1 {
		t.Fatalf("stderr = %q, want one diagnostic", stderr)
	}
}

func TestNewOneshotToolInvokerDoesNotWarnWhenReasoningExplicitlyOff(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Models.Reasoning = "off"
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.BaseURL = "https://example.invalid/v1"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	stderr := captureOneshotStderr(t, func() {
		invoker, err := newOneshotToolInvoker(oneshotBackendAPI, "commit", "future-model", cfg, mgr, nil)
		if err != nil {
			t.Fatalf("newOneshotToolInvoker: %v", err)
		}
		if invoker == nil {
			t.Fatal("newOneshotToolInvoker returned nil invoker")
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q, want no diagnostic", stderr)
	}
}

func captureOneshotStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	restored := false
	restore := func() {
		if restored {
			return
		}
		os.Stderr = old
		_ = w.Close()
		_ = r.Close()
		restored = true
	}
	defer restore()
	t.Cleanup(restore)
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("close stderr pipe writer: %v", err)
	}
	os.Stderr = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr pipe: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close stderr pipe reader: %v", err)
	}
	restored = true
	return string(out)
}

func assertOneshotProfile(t *testing.T, profile oneshot.RequestProfile, wantMaxOutput int, wantRequireTool bool, wantTemperature float64, wantReasoning *model.ReasoningConfig) {
	t.Helper()
	if profile.MaxOutputTokens != wantMaxOutput {
		t.Fatalf("MaxOutputTokens = %d, want %d", profile.MaxOutputTokens, wantMaxOutput)
	}
	if profile.RequireTool != wantRequireTool {
		t.Fatalf("RequireTool = %v, want %v", profile.RequireTool, wantRequireTool)
	}
	if profile.Temperature == nil || *profile.Temperature != wantTemperature {
		t.Fatalf("Temperature = %v, want %g", profile.Temperature, wantTemperature)
	}
	if wantReasoning == nil {
		if profile.Reasoning != nil {
			t.Fatalf("Reasoning = %+v, want nil", profile.Reasoning)
		}
		return
	}
	if profile.Reasoning == nil {
		t.Fatalf("Reasoning = nil, want %+v", wantReasoning)
	}
	if profile.Reasoning.Effort != wantReasoning.Effort || profile.Reasoning.MaxTokens != wantReasoning.MaxTokens {
		t.Fatalf("Reasoning = %+v, want %+v", profile.Reasoning, wantReasoning)
	}
}

func TestInitOneshotDependenciesAppliesProjectTrustForCLIBackend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyDataDir, "")
	t.Setenv("BUCKLEY_APPROVAL_MODE", "yolo")
	t.Setenv("BUCKLEY_TRUST_LEVEL", "autonomous")
	t.Setenv("BUCKLEY_TOOL_SANDBOX_ALLOW_NETWORK", "true")

	repoRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(repoRoot); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	storePath, err := resolveProjectTrustPath()
	if err != nil {
		t.Fatalf("resolveProjectTrustPath: %v", err)
	}
	store, err := loadProjectTrustStore(storePath)
	if err != nil {
		t.Fatalf("loadProjectTrustStore: %v", err)
	}
	if err := store.Set(repoRoot, projectTrustRestricted); err != nil {
		t.Fatalf("store.Set: %v", err)
	}

	cfg, mgr, storeHandle, err := initOneshotDependencies(oneshot.CLIBackendCodex)
	if err != nil {
		t.Fatalf("initOneshotDependencies: %v", err)
	}
	if mgr != nil {
		t.Fatal("CLI backend should not initialize model manager")
	}
	if storeHandle != nil {
		t.Fatal("CLI backend should not initialize storage")
	}
	if cfg.Approval.Mode != "safe" {
		t.Fatalf("approval mode=%q want safe", cfg.Approval.Mode)
	}
	if cfg.Orchestrator.TrustLevel != "conservative" {
		t.Fatalf("trust level=%q want conservative", cfg.Orchestrator.TrustLevel)
	}
	if cfg.Sandbox.AllowNetwork {
		t.Fatal("sandbox network should be disabled for restricted project")
	}
}
