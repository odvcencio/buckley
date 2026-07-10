package model

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestManagerReviewSandboxCommandUsesConfiguredCodexBinary(t *testing.T) {
	dir := t.TempDir()
	name := "codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	command := filepath.Join(dir, name)
	if err := os.WriteFile(command, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}

	manager := &Manager{
		providers: map[string]Provider{
			codexProviderID: &CodexCLIProvider{command: command},
		},
	}
	if got := manager.ReviewSandboxCommand(); got != command {
		t.Fatalf("ReviewSandboxCommand() = %q, want %q", got, command)
	}
}

func TestManagerReviewSandboxCommandFailsClosedWhenUnavailable(t *testing.T) {
	manager := &Manager{
		providers: map[string]Provider{
			codexProviderID: &CodexCLIProvider{command: filepath.Join(t.TempDir(), "missing-codex")},
		},
	}
	if got := manager.ReviewSandboxCommand(); got != "" {
		t.Fatalf("ReviewSandboxCommand() = %q, want empty", got)
	}
}
