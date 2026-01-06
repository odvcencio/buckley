package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateSkillTool_Execute_Project(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateSkillTool{}
	tool.SetWorkDir(tmp)

	result, err := tool.Execute(map[string]any{
		"name":        "Creative Writing",
		"description": "Creative prose guidance.",
		"body":        "# Steps\n\nWrite with vivid detail.",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected success, got %+v", result)
	}

	expectedPath := filepath.Join(tmp, ".buckley", "skills", "creative-writing", "SKILL.md")
	content, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("expected skill file to exist: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "name: creative-writing") {
		t.Fatalf("expected normalized name in frontmatter, got:\n%s", text)
	}
	if !strings.Contains(text, "description: Creative prose guidance.") {
		t.Fatalf("expected description in frontmatter, got:\n%s", text)
	}
	if !strings.Contains(text, "# Steps") {
		t.Fatalf("expected body in content, got:\n%s", text)
	}
}

func TestCreateSkillTool_Execute_Overwrite(t *testing.T) {
	tmp := t.TempDir()
	tool := &CreateSkillTool{}
	tool.SetWorkDir(tmp)

	params := map[string]any{
		"name":        "sample-skill",
		"description": "First description.",
		"body":        "Initial body.",
	}
	if _, err := tool.Execute(params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	params["description"] = "Second description."
	result, err := tool.Execute(params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure when overwrite is false")
	}

	params["overwrite"] = true
	result, err = tool.Execute(params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected success with overwrite, got %+v", result)
	}
}
