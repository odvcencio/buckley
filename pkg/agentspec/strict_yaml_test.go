package agentspec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRejectsIgnoredSettings(t *testing.T) {
	const base = "version: buckley.agent/v1\nname: worker\n"
	for _, tc := range []struct{ name, body, want string }{
		{"root", "task_intent: mutation\n", "task_intent"},
		{"model", "models:\n  reasonning: low\n", "reasonning"},
		{"runtime", "runtime:\n  driver: buckley\n  timeot: 2\n", "timeot"},
		{"tool", "tools:\n  alow: [read_file]\n", "alow"},
		{"policy", "policies:\n  max_tool_call: 2\n", "max_tool_call"},
		{"rule pack", "policies:\n  rule_packs:\n    - name: safety\n      scpoe: project\n", "scpoe"},
		{"sandbox", "sandbox:\n  netwrok: false\n", "netwrok"},
		{"subagent", "subagents:\n  - name: edit\n    task_intent: mutation\n", "task_intent"},
		{"nested subagent", "subagents:\n  - name: edit\n    policies:\n      max_tool_call: 2\n", "max_tool_call"},
		{"terminal", "terminals:\n  - name: check\n    command: [go, test]\n    sandbox:\n      netwrok: false\n", "netwrok"},
		{"second document", "---\nname: ignored\n", "single YAML document"},
		{"empty second document", "---\n", "single YAML document"},
		{"malformed second document", "---\n[unterminated\n", "parsing agent spec"},
		{"duplicate key", "name: other\n", "already defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := Parse([]byte(base + tc.body))
			if err == nil || spec != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse = %+v, %v; want nil spec and error containing %q", spec, err, tc.want)
			}
		})
	}
	t.Run("JSON unknown key", func(t *testing.T) {
		if spec, err := Parse([]byte(`{"version":"buckley.agent/v1","name":"worker","tools":{"alow":["read_file"]}}`)); err == nil || spec != nil || !strings.Contains(err.Error(), "alow") {
			t.Fatalf("JSON unknown field accepted: %+v, %v", spec, err)
		}
	})
}

func TestStrictYAMLPreservesSupportedForms(t *testing.T) {
	data := `version: buckley.agent/v1
name: worker
metadata:
  task_intent: arbitrary metadata, not a runtime setting
  vendor.example/custom: extension
labels:
  custom_label: value
runtime:
  driver: buckley
  env:
    CUSTOM_ENV: value
tools: &tools
  tier: read_only
  allow: [read_file]
subagents:
  - name: read
    tools:
      <<: *tools
terminals:
  - name: check
    command: [go, test]
    env:
      CUSTOM_ENV: other
... # explicit end with no second document
`
	spec, err := Parse([]byte(data))
	if err != nil || !spec.Valid() {
		t.Fatalf("supported YAML rejected: %+v, %v", spec, err)
	}
	if spec.Metadata["vendor.example/custom"] != "extension" || spec.Labels["custom_label"] != "value" || spec.Runtime.Env["CUSTOM_ENV"] != "value" || spec.Terminals[0].Env["CUSTOM_ENV"] != "other" || spec.Subagents[0].Tools.Tier != "read_only" {
		t.Fatalf("lost map values or merge: %+v", spec)
	}
	for _, data := range []string{"", "# empty\n", "null\n"} {
		if spec, err := Parse([]byte(data)); err != nil || spec == nil || spec.Valid() {
			t.Fatalf("empty document must remain a semantic validation failure: %+v, %v", spec, err)
		}
	}
}

func TestFilesystemSubagentRejectsIgnoredSettings(t *testing.T) {
	for _, tc := range []struct{ name, data, want string }{
		{"root", "task_intent: mutation\n", "task_intent"},
		{"nested", "policies:\n  max_tool_call: 2\n", "max_tool_call"},
		{"multiple", "model: caller/model\n---\nmodel: ignored/model\n", "single YAML document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "agent.yaml")
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := loadFilesystemSubagentConfig(root)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), path) {
				t.Fatalf("want offending field/document and source path, got %v", err)
			}
		})
	}
}

func TestRuntimeProfileRejectsUnknownFieldBeforeInstructionRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent.yaml")
	data := "version: buckley.agent/v1\nname: worker\ninstructions:\n  files: [missing.md]\nsubagents:\n  - name: edit\n    task_intent: mutation\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	profile, err := LoadRuntimeProfile(path)
	if profile != nil || err == nil || !strings.Contains(err.Error(), "task_intent") || !strings.Contains(err.Error(), path) {
		t.Fatalf("want unknown-field error with source path before instruction I/O, got %+v, %v", profile, err)
	}
}

func TestStrictYAMLLoadsBundledTemplates(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "templates", "agents", "*.yaml"))
	if err != nil || len(paths) < 2 {
		t.Fatalf("locating bundled templates: %v (%v)", paths, err)
	}
	for _, path := range paths {
		if _, err := LoadRuntimeProfile(path); err != nil {
			t.Errorf("template %s: %v", path, err)
		}
	}
}
