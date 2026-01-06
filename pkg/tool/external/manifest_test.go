package external

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifest_Success(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "tool.yaml")

	content := `
name: test_tool
description: A test tool
parameters:
  type: object
  properties:
    param1:
      type: string
      description: First parameter
  required:
    - param1
executable: ./test.sh
timeout_ms: 30000
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest failed: %v", err)
	}

	if manifest.Name != "test_tool" {
		t.Errorf("expected name 'test_tool', got %q", manifest.Name)
	}
	if manifest.Description != "A test tool" {
		t.Errorf("expected description 'A test tool', got %q", manifest.Description)
	}
	if manifest.Executable != "./test.sh" {
		t.Errorf("expected executable './test.sh', got %q", manifest.Executable)
	}
	if manifest.TimeoutMs != 30000 {
		t.Errorf("expected timeout 30000, got %d", manifest.TimeoutMs)
	}
}

func TestLoadManifest_FileNotFound(t *testing.T) {
	_, err := LoadManifest("/nonexistent/tool.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestLoadManifest_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "tool.yaml")

	content := `
name: test
invalid yaml: {{{
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadManifest(manifestPath)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadManifest_ValidationFails(t *testing.T) {
	tmpDir := t.TempDir()
	manifestPath := filepath.Join(tmpDir, "tool.yaml")

	content := `
name: test_tool
# Missing description and executable
`
	if err := os.WriteFile(manifestPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadManifest(manifestPath)
	if err == nil {
		t.Error("expected validation error")
	}
}

func TestToolManifest_Validate_MissingName(t *testing.T) {
	manifest := &ToolManifest{
		Description: "test",
		Executable:  "./test.sh",
	}

	err := manifest.Validate()
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestToolManifest_Validate_MissingDescription(t *testing.T) {
	manifest := &ToolManifest{
		Name:       "test",
		Executable: "./test.sh",
	}

	err := manifest.Validate()
	if err == nil {
		t.Error("expected error for missing description")
	}
}

func TestToolManifest_Validate_MissingExecutable(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test",
		Description: "test",
	}

	err := manifest.Validate()
	if err == nil {
		t.Error("expected error for missing executable")
	}
}

func TestToolManifest_Validate_DefaultTimeout(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test",
		Description: "test",
		Executable:  "./test.sh",
		TimeoutMs:   0,
	}

	err := manifest.Validate()
	if err != nil {
		t.Errorf("validation should succeed, got: %v", err)
	}

	if manifest.TimeoutMs != 120000 {
		t.Errorf("expected default timeout 120000, got %d", manifest.TimeoutMs)
	}
}

func TestToolManifest_Validate_NilParameters(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test",
		Description: "test",
		Executable:  "./test.sh",
		TimeoutMs:   30000,
		Parameters:  nil,
	}

	err := manifest.Validate()
	if err != nil {
		t.Errorf("validation should succeed, got: %v", err)
	}

	if manifest.Parameters == nil {
		t.Error("expected parameters to be initialized to empty map")
	}
}

func TestToolManifest_Validate_Success(t *testing.T) {
	manifest := &ToolManifest{
		Name:        "test",
		Description: "test",
		Executable:  "./test.sh",
		TimeoutMs:   30000,
		Parameters:  map[string]any{"type": "object"},
	}

	err := manifest.Validate()
	if err != nil {
		t.Errorf("validation should succeed, got: %v", err)
	}
}
