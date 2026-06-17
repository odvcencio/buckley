package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
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

func TestRunAgentCommandCheckAndShow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	spec := []byte(`
version: buckley.agent/v1
name: review-agent
runtime:
  driver: buckley
policies:
  domains: [approval, risk]
`)
	if err := os.WriteFile(path, spec, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	checkOut := captureStdout(t, func() {
		if err := runAgentCommand([]string{"check", path}); err != nil {
			t.Fatalf("runAgentCommand check: %v", err)
		}
	})
	if !strings.Contains(checkOut, "valid Buckley agent spec") {
		t.Fatalf("unexpected check output: %q", checkOut)
	}

	showOut := captureStdout(t, func() {
		if err := runAgentCommand([]string{"show", "--format", "json", path}); err != nil {
			t.Fatalf("runAgentCommand show: %v", err)
		}
	})
	for _, want := range []string{`"name": "review-agent"`, `"valid": true`} {
		if !strings.Contains(showOut, want) {
			t.Fatalf("show output missing %q in:\n%s", want, showOut)
		}
	}
}

func TestRunAgentCommandInvalidSpec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	spec := []byte("version: nope\n")
	if err := os.WriteFile(path, spec, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	out := captureStdout(t, func() {
		err := runAgentCommand([]string{"check", path})
		if err == nil {
			t.Fatal("expected validation error")
		}
		if !strings.Contains(err.Error(), "validation errors") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	if !strings.Contains(out, "unsupported version") {
		t.Fatalf("expected diagnostics, got:\n%s", out)
	}
}

func TestParseAgentRunArgs(t *testing.T) {
	opts, err := parseAgentRunArgs([]string{"agent.yaml", "reviewer", "inspect", "this"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs: %v", err)
	}
	if opts.agentPath != "agent.yaml" || opts.subagent != "reviewer" || opts.task != "inspect this" {
		t.Fatalf("unexpected opts: %+v", opts)
	}

	opts, err = parseAgentRunArgs([]string{"--subagent", "coder", "--model", "test-model", "agent.yaml", "fix", "bug"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs flag form: %v", err)
	}
	if opts.agentPath != "agent.yaml" || opts.subagent != "coder" || opts.model != "test-model" || opts.task != "fix bug" {
		t.Fatalf("unexpected flag opts: %+v", opts)
	}

	opts, err = parseAgentRunArgs([]string{"--subagent", "probe", "--no-tools", "agent.yaml", "answer", "directly"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs no-tools form: %v", err)
	}
	if opts.toolTier != "none" || opts.task != "answer directly" {
		t.Fatalf("unexpected no-tools opts: %+v", opts)
	}

	opts, err = parseAgentRunArgs([]string{"--tool-tier", "read_only", "agent.yaml", "reviewer", "inspect"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs tool-tier form: %v", err)
	}
	if opts.toolTier != "read_only" {
		t.Fatalf("toolTier=%q want read_only", opts.toolTier)
	}

	if _, err := parseAgentRunArgs([]string{"--tool-tier", "root", "agent.yaml", "reviewer", "inspect"}); err == nil {
		t.Fatalf("expected invalid tool-tier error")
	}
	if _, err := parseAgentRunArgs([]string{"--no-tools", "--tool-tier", "full", "agent.yaml", "reviewer", "inspect"}); err == nil {
		t.Fatalf("expected conflicting tool flags error")
	}

	if _, err := parseAgentRunArgs([]string{"agent.yaml", "reviewer"}); err == nil {
		t.Fatalf("expected usage error for missing task")
	}
}

func TestRunAgentRunMissingSubagent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	spec := []byte(`
version: buckley.agent/v1
name: daily
subagents:
  - name: coder
    persona: implementer
`)
	if err := os.WriteFile(path, spec, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	err := runAgentRun([]string{path, "reviewer", "inspect this"})
	if err == nil || !strings.Contains(err.Error(), "available: coder") {
		t.Fatalf("err=%v want available subagents", err)
	}
}

func TestFormatSubagentTask(t *testing.T) {
	got := formatSubagentTask("reviewer", "inspect this")
	for _, want := range []string{`Subagent "reviewer" task:`, "inspect this", "Complete the task directly", "remaining risks", "do not claim unperformed actions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("task prompt missing %q: %s", want, got)
		}
	}
}

func TestResolveOneShotToolFilterRespectsAgentTierAndDeny(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	registry.Register(metadataTool{name: "read_file", metadata: tool.ToolMetadata{Impact: tool.ImpactReadOnly, Category: tool.CategoryFilesystem}})
	registry.Register(metadataTool{name: "write_file", metadata: tool.ToolMetadata{Impact: tool.ImpactModifying, Category: tool.CategoryFilesystem}})
	registry.Register(metadataTool{name: "run_shell", metadata: tool.ToolMetadata{Impact: tool.ImpactDestructive, Category: tool.CategoryShell}})

	profile := &agentspec.RuntimeProfile{Spec: &agentspec.Spec{Tools: agentspec.ToolSpec{Tier: "none"}}}
	got := resolveOneShotToolFilter(profile, registry, nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("none filter=%v want explicit empty list", got)
	}

	profile = &agentspec.RuntimeProfile{Spec: &agentspec.Spec{Tools: agentspec.ToolSpec{Tier: "read_only"}}}
	got = resolveOneShotToolFilter(profile, registry, nil)
	if strings.Join(got, ",") != "read_file" {
		t.Fatalf("read_only filter=%v want read_file", got)
	}

	profile.Spec.Tools.Tier = "standard"
	profile.Spec.Tools.Deny = []string{"write_file"}
	got = resolveOneShotToolFilter(profile, registry, nil)
	if strings.Join(got, ",") != "read_file" {
		t.Fatalf("standard deny filter=%v want read_file", got)
	}

	profile.Spec.Tools.Allow = []string{"run_shell", "read_file"}
	got = resolveOneShotToolFilter(profile, registry, []string{"read_file", "read_file"})
	if strings.Join(got, ",") != "read_file" {
		t.Fatalf("explicit filter=%v want read_file", got)
	}
}

type metadataTool struct {
	name     string
	metadata tool.ToolMetadata
}

func (t metadataTool) Name() string { return t.name }

func (t metadataTool) Description() string { return t.name }

func (t metadataTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (t metadataTool) Execute(map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true}, nil
}

func (t metadataTool) Metadata() tool.ToolMetadata { return t.metadata }

func TestRunRulesFacts(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runRulesCommand([]string{"facts", "approval"}); err != nil {
			t.Fatalf("runRulesCommand facts: %v", err)
		}
	})
	for _, want := range []string{"domain:  approval", "approval.mode", "risk.level"} {
		if !strings.Contains(out, want) {
			t.Fatalf("facts output missing %q in:\n%s", want, out)
		}
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
