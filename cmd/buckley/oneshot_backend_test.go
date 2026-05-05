package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/oneshot"
)

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
