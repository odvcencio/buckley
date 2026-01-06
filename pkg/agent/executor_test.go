package agent

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDefaultExecutorConfig(t *testing.T) {
	cfg := DefaultExecutorConfig()

	if cfg.MaxIterations != 50 {
		t.Errorf("Expected 50 max iterations, got %d", cfg.MaxIterations)
	}
	if cfg.ToolTimeout != 5*time.Minute {
		t.Errorf("Expected 5m tool timeout, got %v", cfg.ToolTimeout)
	}
	if cfg.TotalTimeout != 30*time.Minute {
		t.Errorf("Expected 30m total timeout, got %v", cfg.TotalTimeout)
	}
}

func TestTaskResultSerialization(t *testing.T) {
	result := TaskResult{
		TaskID:     "task-123",
		AgentID:    "coder-abc-xyz",
		Success:    true,
		Output:     "Code written successfully",
		Duration:   5 * time.Second,
		TokensUsed: 1500,
		Artifacts: []Artifact{
			{Type: "file", Path: "main.go", Content: "package main"},
		},
		ToolCalls: []ToolCall{
			{Name: "write_file", Arguments: `{"path":"main.go"}`, Result: "success", Duration: time.Second, Success: true},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded TaskResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.TaskID != result.TaskID {
		t.Errorf("Expected TaskID %q, got %q", result.TaskID, decoded.TaskID)
	}
	if decoded.Success != result.Success {
		t.Errorf("Expected Success %v, got %v", result.Success, decoded.Success)
	}
	if decoded.TokensUsed != result.TokensUsed {
		t.Errorf("Expected TokensUsed %d, got %d", result.TokensUsed, decoded.TokensUsed)
	}
	if len(decoded.Artifacts) != 1 {
		t.Errorf("Expected 1 artifact, got %d", len(decoded.Artifacts))
	}
	if len(decoded.ToolCalls) != 1 {
		t.Errorf("Expected 1 tool call, got %d", len(decoded.ToolCalls))
	}
}

func TestArtifactSerialization(t *testing.T) {
	artifact := Artifact{
		Type:    "commit",
		Path:    "abc123",
		Content: "feat: add new feature",
	}

	data, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded Artifact
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Type != artifact.Type {
		t.Errorf("Expected Type %q, got %q", artifact.Type, decoded.Type)
	}
	if decoded.Path != artifact.Path {
		t.Errorf("Expected Path %q, got %q", artifact.Path, decoded.Path)
	}
}

func TestToolCallSerialization(t *testing.T) {
	tc := ToolCall{
		Name:      "run_shell",
		Arguments: `{"command":"go test ./..."}`,
		Result:    `{"exitCode":0,"stdout":"PASS"}`,
		Duration:  2 * time.Second,
		Success:   true,
	}

	data, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded ToolCall
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Name != tc.Name {
		t.Errorf("Expected Name %q, got %q", tc.Name, decoded.Name)
	}
	if decoded.Success != tc.Success {
		t.Errorf("Expected Success %v, got %v", tc.Success, decoded.Success)
	}
}

func TestTaskResultWithError(t *testing.T) {
	result := TaskResult{
		TaskID:     "task-fail",
		AgentID:    "coder-123",
		Success:    false,
		Error:      "compilation failed: undefined variable",
		Duration:   10 * time.Second,
		TokensUsed: 500,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded TaskResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Success {
		t.Error("Expected Success to be false")
	}
	if decoded.Error == "" {
		t.Error("Expected Error to be set")
	}
	if decoded.Error != result.Error {
		t.Errorf("Expected Error %q, got %q", result.Error, decoded.Error)
	}
}
