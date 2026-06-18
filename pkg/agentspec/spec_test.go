package agentspec

import (
	"os"
	"strings"
	"testing"
)

func TestParseValidateValidSpec(t *testing.T) {
	spec, err := Parse([]byte(`
version: buckley.agent/v1
name: reviewer
summary: Reviews branches before merge
persona: senior-reviewer
instructions:
  prompt: Review for correctness first.
models:
  chat: z-ai/glm-5.2
runtime:
  driver: buckley
skills: [code-review, systematic-debugging]
tools:
  tier: standard
  allow: [read_file, search_text]
policies:
  approval_mode: safe
  max_tool_calls: 40
  domains: [approval, risk, tool_budget]
  rule_packs:
    - name: project-safety
      scope: project
      domains: [risk]
sandbox:
  mode: workspace
subagents:
  - name: verifier
    tool_tier: none
    tools:
      allow: [read_file]
      deny: [run_shell]
terminals:
  - name: tests
    command: [go, test, ./...]
labels:
  sensitivity: public
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if diagnostics := spec.Validate(); len(diagnostics) != 0 {
		t.Fatalf("expected no diagnostics, got %+v", diagnostics)
	}
	if !spec.Valid() {
		t.Fatal("expected spec to be valid")
	}
}

func TestValidateInvalidSpec(t *testing.T) {
	spec, err := Parse([]byte(`
version: other.agent/v1
name: ""
runtime:
  driver: external
tools:
  tier: root
policies:
  approval_mode: maybe
  max_tool_calls: -1
  rule_packs:
    - scope: org
sandbox:
  mode: host
skills: [review, review]
subagents:
  - name: worker
    tools:
      tier: root
      allow: [read_file, read_file]
  - name: worker
terminals:
  - name: shell
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	diagnostics := spec.Validate()
	if len(diagnostics) < 10 {
		t.Fatalf("expected many diagnostics, got %+v", diagnostics)
	}
	joined := diagnosticsText(diagnostics)
	for _, want := range []string{
		"unsupported version",
		"name is required",
		"external runtime requires an adapter",
		"external runtime requires a command",
		"tool tier",
		"approval mode",
		"max tool calls",
		"rule pack requires name or path",
		"sandbox mode",
		"duplicate value",
		"duplicate subagent",
		"terminal command",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing diagnostic %q in:\n%s", want, joined)
		}
	}
}

func TestLoadFileResolvesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/agent.yaml"
	data := []byte(`
version: buckley.agent/v1
name: project-agent
instructions:
  files: [AGENTS.md]
policies:
  rule_packs:
    - path: rules/project.arb
`)
	if err := writeFixture(path, data); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	spec, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if !strings.HasPrefix(spec.Instructions.Files[0], dir) {
		t.Fatalf("instruction file was not resolved: %q", spec.Instructions.Files[0])
	}
	if !strings.HasPrefix(spec.Policies.RulePacks[0].Path, dir) {
		t.Fatalf("rule pack path was not resolved: %q", spec.Policies.RulePacks[0].Path)
	}
}

func TestRenderTextAndJSON(t *testing.T) {
	spec := &Spec{
		Version: Version,
		Name:    "builder",
		Runtime: RuntimeSpec{Driver: "external", Adapter: "opencode"},
		Models:  ModelSpec{Chat: "qwen/qwen-flash"},
		Subagents: []SubagentSpec{
			{Name: "reviewer", Persona: "strict", Model: "review-model", ToolTier: "read_only"},
			{Name: "coder", Skills: []string{"implementation"}},
		},
	}
	diagnostics := spec.Validate()
	out := RenderText(spec, diagnostics)
	for _, want := range []string{"Agent: builder", "Runtime: external/opencode", "chat=qwen/qwen-flash", "Subagents:", "  - coder (skills=implementation)", "  - reviewer (persona=strict, model=review-model, tool_tier=read_only)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("RenderText missing %q in:\n%s", want, out)
		}
	}
	data, err := JSON(spec, diagnostics)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(string(data), `"valid": false`) {
		t.Fatalf("expected JSON to include invalid status for missing external command:\n%s", string(data))
	}
}

func diagnosticsText(diagnostics []Diagnostic) string {
	var b strings.Builder
	for _, diag := range diagnostics {
		b.WriteString(diag.Message)
		b.WriteByte('\n')
	}
	return b.String()
}

func writeFixture(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
