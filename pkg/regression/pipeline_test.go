package regression

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/gitwatcher"
)

func TestNewPipeline(t *testing.T) {
	cfg := config.GitEventsConfig{
		RegressionCommand: "echo test",
	}

	p := NewPipeline(cfg)
	if p == nil {
		t.Fatal("NewPipeline returned nil")
	}
	if p.cfg.RegressionCommand != "echo test" {
		t.Errorf("expected RegressionCommand 'echo test', got %q", p.cfg.RegressionCommand)
	}
}

func TestHandleMerge_EmptyCommand(t *testing.T) {
	cfg := config.GitEventsConfig{
		RegressionCommand: "",
	}

	p := NewPipeline(cfg)
	event := gitwatcher.MergeEvent{
		Repository: "owner/repo",
		Branch:     "main",
		SHA:        "abc123",
	}

	// Should not panic with empty command
	p.HandleMerge(event)
}

func TestHandleMerge_WhitespaceCommand(t *testing.T) {
	cfg := config.GitEventsConfig{
		RegressionCommand: "   ",
	}

	p := NewPipeline(cfg)
	event := gitwatcher.MergeEvent{
		Repository: "owner/repo",
		Branch:     "main",
		SHA:        "abc123",
	}

	// Should not panic with whitespace-only command
	p.HandleMerge(event)
}

func TestHandleMerge_Success(t *testing.T) {
	cfg := config.GitEventsConfig{
		RegressionCommand: "echo regression",
		ReleaseCommand:    "echo release",
	}

	p := NewPipeline(cfg)
	event := gitwatcher.MergeEvent{
		Repository: "owner/repo",
		Branch:     "main",
		SHA:        "abc123",
	}

	// Should execute successfully
	p.HandleMerge(event)
}

func TestHandleMerge_RegressionFails(t *testing.T) {
	cfg := config.GitEventsConfig{
		RegressionCommand: "exit 1",
		FailureCommand:    "echo failure",
		ReleaseCommand:    "echo release",
	}

	p := NewPipeline(cfg)
	event := gitwatcher.MergeEvent{
		Repository: "owner/repo",
		Branch:     "main",
		SHA:        "abc123",
	}

	// Should execute failure command when regression fails
	p.HandleMerge(event)
}

func TestHandleMerge_NoReleaseCommand(t *testing.T) {
	cfg := config.GitEventsConfig{
		RegressionCommand: "echo test",
		ReleaseCommand:    "",
	}

	p := NewPipeline(cfg)
	event := gitwatcher.MergeEvent{
		Repository: "owner/repo",
		Branch:     "main",
		SHA:        "abc123",
	}

	// Should not panic when release command is empty
	p.HandleMerge(event)
}

func TestHandleMerge_NoFailureCommand(t *testing.T) {
	cfg := config.GitEventsConfig{
		RegressionCommand: "exit 1",
		FailureCommand:    "",
	}

	p := NewPipeline(cfg)
	event := gitwatcher.MergeEvent{
		Repository: "owner/repo",
		Branch:     "main",
		SHA:        "abc123",
	}

	// Should not panic when failure command is empty
	p.HandleMerge(event)
}

func TestRunCommand_EmptyCommand(t *testing.T) {
	p := &Pipeline{}
	err := p.runCommand("test", "", nil)
	if err != nil {
		t.Errorf("empty command should not return error, got: %v", err)
	}
}

func TestRunCommand_WhitespaceCommand(t *testing.T) {
	p := &Pipeline{}
	err := p.runCommand("test", "   ", nil)
	if err != nil {
		t.Errorf("whitespace command should not return error, got: %v", err)
	}
}

func TestRunCommand_Success(t *testing.T) {
	p := &Pipeline{}
	err := p.runCommand("test", "echo hello", nil)
	if err != nil {
		t.Errorf("command should succeed, got: %v", err)
	}
}

func TestRunCommand_Failure(t *testing.T) {
	p := &Pipeline{}
	err := p.runCommand("test", "exit 1", nil)
	if err == nil {
		t.Error("command should fail")
	}
}

func TestRunCommand_WithEnv(t *testing.T) {
	p := &Pipeline{}
	env := map[string]string{
		"TEST_VAR": "test_value",
	}
	// Command that checks environment variable
	err := p.runCommand("test", "test -n \"$TEST_VAR\"", env)
	if err != nil {
		t.Errorf("command with env should succeed, got: %v", err)
	}
}

func TestFormatEnv(t *testing.T) {
	env := map[string]string{
		"KEY1": "value1",
		"KEY2": "value2",
	}

	result := formatEnv(env)
	if len(result) != 2 {
		t.Errorf("expected 2 env vars, got %d", len(result))
	}

	// Check that both keys are present (order doesn't matter)
	found := make(map[string]bool)
	for _, e := range result {
		if e == "KEY1=value1" {
			found["KEY1"] = true
		}
		if e == "KEY2=value2" {
			found["KEY2"] = true
		}
	}

	if !found["KEY1"] || !found["KEY2"] {
		t.Errorf("expected KEY1 and KEY2 in result, got %v", result)
	}
}

func TestFormatEnv_Empty(t *testing.T) {
	result := formatEnv(nil)
	if len(result) != 0 {
		t.Errorf("expected empty slice for nil env, got %v", result)
	}

	result = formatEnv(map[string]string{})
	if len(result) != 0 {
		t.Errorf("expected empty slice for empty env, got %v", result)
	}
}
