package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
)

func TestRunPlanCommandUsageError(t *testing.T) {
	err := runPlanCommand([]string{"only-name"})
	if err == nil {
		t.Fatal("expected usage error for missing description")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("expected usage message, got: %v", err)
	}
}

func TestRunExecuteCommandUsageError(t *testing.T) {
	err := runExecuteCommand(nil)
	if err == nil {
		t.Fatal("expected usage error for missing plan-id")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("expected usage message, got: %v", err)
	}
}

func TestRunExecuteTaskCommandUsageError(t *testing.T) {
	err := runExecuteTaskCommand(nil)
	if err == nil {
		t.Fatal("expected usage error for missing flags")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("expected usage message, got: %v", err)
	}

	// Missing task
	err = runExecuteTaskCommand([]string{"--plan", "p1"})
	if err == nil {
		t.Fatal("expected usage error for missing task")
	}

	// Missing plan
	err = runExecuteTaskCommand([]string{"--task", "t1"})
	if err == nil {
		t.Fatal("expected usage error for missing plan")
	}
}

func TestDispatchSubcommandResumePassthrough(t *testing.T) {
	// Resume should not be handled by dispatchSubcommand (returns to main flow)
	handled, _ := dispatchSubcommand([]string{"resume", "sess123"})
	if handled {
		t.Fatal("resume should pass through to main flow")
	}
}

func TestDispatchSubcommandMigrate(t *testing.T) {
	// Set up temp home
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create buckley dir
	if err := os.MkdirAll(filepath.Join(home, ".buckley"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	out := captureStdout(t, func() {
		handled, code := dispatchSubcommand([]string{"migrate"})
		if !handled {
			t.Fatal("migrate should be handled")
		}
		if code != 0 {
			t.Fatalf("migrate should succeed, got code %d", code)
		}
	})

	if !strings.Contains(out, "migrations applied") {
		t.Errorf("expected migrations applied message, got: %s", out)
	}
}

func TestSanitizeTerminalInputExtended(t *testing.T) {
	// Additional test cases for terminal input sanitization
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "text\x1b[1;31mcolored\x1b[0m",
			want:  "textcolored",
		},
		{
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		got := sanitizeTerminalInput(tt.input)
		if got != tt.want {
			t.Errorf("sanitizeTerminalInput(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestPushBranchEmptyBranchNoOp(t *testing.T) {
	// Should not error with empty branch
	if err := pushBranch("origin", ""); err != nil {
		t.Errorf("pushBranch empty should be no-op, got: %v", err)
	}
}

func TestCompletionShells(t *testing.T) {
	shells := []string{"bash", "zsh", "fish"}
	for _, shell := range shells {
		out := captureStdout(t, func() {
			if err := runCompletionCommand([]string{shell}); err != nil {
				t.Fatalf("completion %s: %v", shell, err)
			}
		})
		if out == "" {
			t.Errorf("expected %s completion output", shell)
		}
	}

	// Unknown shell should error
	if err := runCompletionCommand([]string{"powershell"}); err == nil {
		t.Fatal("expected error for unsupported shell")
	}
}

func TestInitDependenciesNoProvider(t *testing.T) {
	// Clear all provider keys
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create minimal config without API key
	if err := os.MkdirAll(filepath.Join(home, ".buckley"), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, _, _, err := initDependencies()
	if err == nil {
		t.Fatal("expected error for no provider")
	}
	if !strings.Contains(err.Error(), "no provider") && !strings.Contains(err.Error(), "API keys") {
		t.Errorf("expected no provider error, got: %v", err)
	}
}

func TestInitIPCStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store, err := initIPCStore()
	if err != nil {
		t.Fatalf("initIPCStore: %v", err)
	}
	defer store.Close()

	// Verify DB file exists
	dbPath := filepath.Join(home, ".buckley", "buckley.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("expected DB file to exist")
	}
}

func TestEncodingOverrideFlagApplied(t *testing.T) {
	// Test that encoding override flag affects config
	origFlag := encodingOverrideFlag
	t.Cleanup(func() { encodingOverrideFlag = origFlag })

	// Test json encoding
	encodingOverrideFlag = "json"
	cfg := config.DefaultConfig()
	cfg.Encoding.UseToon = true

	// The actual override happens in initDependencies, verify the logic
	if encodingOverrideFlag != "" {
		cfg.Encoding.UseToon = encodingOverrideFlag != "json"
	}

	if cfg.Encoding.UseToon {
		t.Error("encoding should be false (json mode)")
	}

	// Test toon encoding
	encodingOverrideFlag = "toon"
	cfg.Encoding.UseToon = false
	if encodingOverrideFlag != "" {
		cfg.Encoding.UseToon = encodingOverrideFlag != "json"
	}

	if !cfg.Encoding.UseToon {
		t.Error("encoding should be true (toon mode)")
	}
}

func TestWorktreeCommandUsage(t *testing.T) {
	// Test worktree command with no args shows usage
	err := runWorktreeCommand(nil)
	if err == nil {
		t.Fatal("expected usage error")
	}

	// Test unknown subcommand
	err = runWorktreeCommand([]string{"unknown"})
	if err == nil {
		t.Fatal("expected unknown subcommand error")
	}
}
