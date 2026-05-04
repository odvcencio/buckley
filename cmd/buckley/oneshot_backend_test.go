package main

import (
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
	if got := resolveCommitModelID("", cfg, oneshot.CLIBackendCodex); got != "" {
		t.Fatalf("CLI model = %q, want empty default", got)
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
