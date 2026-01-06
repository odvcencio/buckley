package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFile(t *testing.T) {
	// Create temp dir with test plugin
	tmpDir := t.TempDir()
	pluginPath := filepath.Join(tmpDir, "changelog.yaml")

	yaml := `name: changelog
version: "1.0"
description: Generate a changelog from git commits

tool:
  name: generate_changelog
  description: Generate a structured changelog from git history
  parameters:
    version:
      type: string
      description: The version number for this changelog entry
    changes:
      type: array
      description: List of changes grouped by category
      items:
        type: object
        properties:
          category:
            type: string
            description: Category like Added, Changed, Fixed, Removed
          items:
            type: array
            items:
              type: string
  required:
    - version
    - changes

context:
  - type: git_log
    since: "${FLAG:since|last-tag}"
    format: oneline
  - type: file
    path: CHANGELOG.md
    optional: true
    max_bytes: 8192

flags:
  - name: since
    type: string
    description: Starting point for git log
    default: last-tag
  - name: version
    type: string
    description: Version number for this release
    required: true

output:
  template: simple
  format: "## [${version}] - ${date}\n\n${changes}"
  actions:
    - name: prepend
      description: Add to beginning of CHANGELOG.md
      command: prepend_file
      args:
        path: CHANGELOG.md
`

	if err := os.WriteFile(pluginPath, []byte(yaml), 0644); err != nil {
		t.Fatalf("write test plugin: %v", err)
	}

	loader := NewLoader(tmpDir)
	def, err := loader.LoadFile(pluginPath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}

	// Verify basic fields
	if def.Name != "changelog" {
		t.Errorf("Name = %q, want 'changelog'", def.Name)
	}
	if def.Version != "1.0" {
		t.Errorf("Version = %q, want '1.0'", def.Version)
	}
	if def.Description != "Generate a changelog from git commits" {
		t.Errorf("Description = %q", def.Description)
	}

	// Verify tool
	if def.Tool.Name != "generate_changelog" {
		t.Errorf("Tool.Name = %q, want 'generate_changelog'", def.Tool.Name)
	}
	if len(def.Tool.Parameters) != 2 {
		t.Errorf("Tool.Parameters len = %d, want 2", len(def.Tool.Parameters))
	}
	if len(def.Tool.Required) != 2 {
		t.Errorf("Tool.Required len = %d, want 2", len(def.Tool.Required))
	}

	// Verify context
	if len(def.Context) != 2 {
		t.Errorf("Context len = %d, want 2", len(def.Context))
	}
	if def.Context[0].Type != "git_log" {
		t.Errorf("Context[0].Type = %q, want 'git_log'", def.Context[0].Type)
	}
	if def.Context[1].Optional != true {
		t.Errorf("Context[1].Optional = %v, want true", def.Context[1].Optional)
	}

	// Verify flags
	if len(def.Flags) != 2 {
		t.Errorf("Flags len = %d, want 2", len(def.Flags))
	}
	if def.Flags[0].Name != "since" {
		t.Errorf("Flags[0].Name = %q, want 'since'", def.Flags[0].Name)
	}
	if def.Flags[1].Required != true {
		t.Errorf("Flags[1].Required = %v, want true", def.Flags[1].Required)
	}

	// Verify output
	if def.Output.Template != "simple" {
		t.Errorf("Output.Template = %q, want 'simple'", def.Output.Template)
	}
	if len(def.Output.Actions) != 1 {
		t.Errorf("Output.Actions len = %d, want 1", len(def.Output.Actions))
	}
}

func TestLoadAll(t *testing.T) {
	tmpDir := t.TempDir()

	// Create two plugins
	plugin1 := `name: tool1
version: "1.0"
description: First tool
tool:
  name: tool1
  description: First tool
  parameters: {}
`
	plugin2 := `name: tool2
version: "1.0"
description: Second tool
tool:
  name: tool2
  description: Second tool
  parameters: {}
`

	os.WriteFile(filepath.Join(tmpDir, "tool1.yaml"), []byte(plugin1), 0644)
	os.WriteFile(filepath.Join(tmpDir, "tool2.yml"), []byte(plugin2), 0644) // .yml extension

	loader := NewLoader(tmpDir)
	defs, err := loader.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	if len(defs) != 2 {
		t.Errorf("LoadAll returned %d plugins, want 2", len(defs))
	}

	names := make(map[string]bool)
	for _, def := range defs {
		names[def.Name] = true
	}
	if !names["tool1"] || !names["tool2"] {
		t.Errorf("Missing expected plugins: %v", names)
	}
}

func TestLoadFileMissingName(t *testing.T) {
	tmpDir := t.TempDir()
	pluginPath := filepath.Join(tmpDir, "bad.yaml")

	yaml := `version: "1.0"
description: No name
`
	os.WriteFile(pluginPath, []byte(yaml), 0644)

	loader := NewLoader(tmpDir)
	_, err := loader.LoadFile(pluginPath)
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestToCommand(t *testing.T) {
	def := &Definition{
		Name:        "test",
		Description: "Test command",
		Tool: ToolDef{
			Name:        "test_tool",
			Description: "Test tool",
			Parameters: map[string]ParamDef{
				"input": {
					Type:        "string",
					Description: "Input value",
				},
			},
			Required: []string{"input"},
		},
	}

	cmd := def.ToCommand()
	if cmd.Name != "test" {
		t.Errorf("cmd.Name = %q, want 'test'", cmd.Name)
	}
	if cmd.Builtin {
		t.Error("cmd.Builtin = true, want false")
	}
	if cmd.Tool.Name != "test_tool" {
		t.Errorf("cmd.Tool.Name = %q, want 'test_tool'", cmd.Tool.Name)
	}
}

func TestInterpolateFlags(t *testing.T) {
	tests := []struct {
		template string
		flags    map[string]string
		want     string
	}{
		{
			template: "Hello ${FLAG:name}",
			flags:    map[string]string{"name": "World"},
			want:     "Hello World",
		},
		{
			template: "${FLAG:missing|default}",
			flags:    map[string]string{},
			want:     "default",
		},
		{
			template: "${FLAG:present|default}",
			flags:    map[string]string{"present": "value"},
			want:     "value",
		},
		{
			template: "${FLAG:a} and ${FLAG:b}",
			flags:    map[string]string{"a": "X", "b": "Y"},
			want:     "X and Y",
		},
	}

	for _, tt := range tests {
		got := InterpolateFlags(tt.template, tt.flags)
		if got != tt.want {
			t.Errorf("InterpolateFlags(%q, %v) = %q, want %q",
				tt.template, tt.flags, got, tt.want)
		}
	}
}
