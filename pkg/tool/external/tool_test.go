package external

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewTool(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test_tool",
		Description: "A test tool",
		Executable:  "./test.sh",
		TimeoutMs:   30000,
		Parameters:  map[string]any{},
	}

	tool := NewTool(manifest, "/path/to/test.sh")
	if tool == nil {
		t.Fatal("NewTool returned nil")
	}
	if tool.manifest != manifest {
		t.Error("manifest not set correctly")
	}
	if tool.executable != "/path/to/test.sh" {
		t.Errorf("expected executable '/path/to/test.sh', got %q", tool.executable)
	}
}

func TestExternalTool_Name(t *testing.T) {
	manifest := &ToolManifest{
		Name: "test_tool",
	}
	tool := NewTool(manifest, "")

	if tool.Name() != "test_tool" {
		t.Errorf("expected name 'test_tool', got %q", tool.Name())
	}
}

func TestExternalTool_Description(t *testing.T) {
	manifest := &ToolManifest{
		Description: "A test tool",
	}
	tool := NewTool(manifest, "")

	if tool.Description() != "A test tool" {
		t.Errorf("expected description 'A test tool', got %q", tool.Description())
	}
}

func TestExternalTool_Parameters_Empty(t *testing.T) {
	manifest := &ToolManifest{
		Parameters: map[string]any{},
	}
	tool := NewTool(manifest, "")

	schema := tool.Parameters()
	if schema.Type != "object" {
		t.Errorf("expected type 'object', got %q", schema.Type)
	}
	if len(schema.Properties) != 0 {
		t.Errorf("expected empty properties, got %d", len(schema.Properties))
	}
}

func TestExternalTool_Parameters_WithProperties(t *testing.T) {
	manifest := &ToolManifest{
		Parameters: map[string]any{
			"properties": map[string]any{
				"param1": map[string]any{
					"type":        "string",
					"description": "First parameter",
					"default":     "default_value",
				},
			},
			"required": []any{"param1"},
		},
	}
	tool := NewTool(manifest, "")

	schema := tool.Parameters()
	if len(schema.Properties) != 1 {
		t.Errorf("expected 1 property, got %d", len(schema.Properties))
	}

	param1, ok := schema.Properties["param1"]
	if !ok {
		t.Fatal("param1 not found in properties")
	}
	if param1.Type != "string" {
		t.Errorf("expected type 'string', got %q", param1.Type)
	}
	if param1.Description != "First parameter" {
		t.Errorf("expected description 'First parameter', got %q", param1.Description)
	}
	if param1.Default != "default_value" {
		t.Errorf("expected default 'default_value', got %v", param1.Default)
	}

	if len(schema.Required) != 1 || schema.Required[0] != "param1" {
		t.Errorf("expected required ['param1'], got %v", schema.Required)
	}
}

func TestExternalTool_Execute_Success(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")

	script := `#!/bin/bash
echo '{"success": true, "data": {"output": "hello"}}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	manifest := &ToolManifest{
		Name:       "test_tool",
		TimeoutMs:  5000,
		Parameters: map[string]any{},
	}
	tool := NewTool(manifest, scriptPath)

	result, err := tool.Execute(map[string]any{"test": "param"})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !result.Success {
		t.Errorf("expected success=true, got false. Error: %s", result.Error)
	}
}

func TestExternalTool_Execute_Failure(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")

	script := `#!/bin/bash
echo "error message" >&2
exit 1
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	manifest := &ToolManifest{
		Name:       "test_tool",
		TimeoutMs:  5000,
		Parameters: map[string]any{},
	}
	tool := NewTool(manifest, scriptPath)

	result, err := tool.Execute(map[string]any{})
	if err != nil {
		t.Fatalf("Execute should not return error, got: %v", err)
	}

	if result.Success {
		t.Error("expected success=false")
	}
	if result.Error == "" {
		t.Error("expected error message")
	}
}

func TestExternalTool_Execute_Timeout(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")

	script := `#!/bin/bash
sleep 10
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	manifest := &ToolManifest{
		Name:       "test_tool",
		TimeoutMs:  100, // 100ms timeout
		Parameters: map[string]any{},
	}
	tool := NewTool(manifest, scriptPath)

	result, err := tool.Execute(map[string]any{})
	if err != nil {
		t.Fatalf("Execute should not return error, got: %v", err)
	}

	if result.Success {
		t.Error("expected success=false for timeout")
	}
	if result.Error == "" || result.Error != "tool execution timed out after 100ms" {
		t.Errorf("expected timeout error, got: %s", result.Error)
	}
}

func TestExternalTool_Execute_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")

	script := `#!/bin/bash
echo "not valid json"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	manifest := &ToolManifest{
		Name:       "test_tool",
		TimeoutMs:  5000,
		Parameters: map[string]any{},
	}
	tool := NewTool(manifest, scriptPath)

	result, err := tool.Execute(map[string]any{})
	if err != nil {
		t.Fatalf("Execute should not return error, got: %v", err)
	}

	if result.Success {
		t.Error("expected success=false for invalid JSON")
	}
	if result.Error == "" {
		t.Error("expected error message about JSON parsing")
	}
}

func TestExternalTool_Execute_NonExistentExecutable(t *testing.T) {
	manifest := &ToolManifest{
		Name:       "test_tool",
		TimeoutMs:  5000,
		Parameters: map[string]any{},
	}
	tool := NewTool(manifest, "/nonexistent/script.sh")

	result, err := tool.Execute(map[string]any{})
	if err != nil {
		t.Fatalf("Execute should not return error, got: %v", err)
	}

	if result.Success {
		t.Error("expected success=false for nonexistent executable")
	}
}

func TestExternalTool_Execute_TimeoutHonorsMaxExecTimeSeconds(t *testing.T) {
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "test.sh")

	script := `#!/bin/bash
sleep 2
`
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	manifest := &ToolManifest{
		Name:       "test_tool",
		TimeoutMs:  5000,
		Parameters: map[string]any{},
	}
	tool := NewTool(manifest, scriptPath)
	tool.SetMaxExecTimeSeconds(1)

	start := time.Now()
	result, err := tool.Execute(map[string]any{})
	if err != nil {
		t.Fatalf("Execute should not return error, got: %v", err)
	}
	if result.Success {
		t.Fatalf("expected success=false for timeout")
	}
	if want := "tool execution timed out after 1s"; result.Error != want {
		t.Fatalf("error=%q want %q", result.Error, want)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout took too long")
	}
}
