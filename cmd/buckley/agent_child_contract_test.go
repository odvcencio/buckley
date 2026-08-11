package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/subagent"
)

func TestApplyAgentRunChildContract_NarrowsAndPinsResolvedFields(t *testing.T) {
	profile := &agentspec.RuntimeProfile{Spec: &agentspec.Spec{
		Models: agentspec.ModelSpec{Execution: "project/model"},
		Instructions: agentspec.InstructionSpec{
			Prompt: "Keep changes focused.",
		},
		Tools: agentspec.ToolSpec{Tier: "standard", Allow: []string{"read_file", "write_file"}},
	}}
	contract := subagent.ChildContract{
		SchemaVersion:    "buckley.subagent-contract/v1",
		Model:            "child/model",
		Tier:             "execute",
		Effort:           "high",
		SystemPrompt:     "Follow the child review protocol.",
		AllowedTools:     []string{"read_file", "search_text"},
		ToolsConstrained: true,
		StepCap:          4,
		ApprovalPosture:  "safe",
		OutputSchema:     "buckley.artifact/v1",
	}
	if err := applyAgentRunChildContract(profile, contract); err != nil {
		t.Fatalf("applyAgentRunChildContract: %v", err)
	}
	spec := profile.Spec
	if spec.Models.Execution != "child/model" || spec.Models.Chat != "child/model" {
		t.Fatalf("models = %+v", spec.Models)
	}
	if got := strings.Join(spec.Tools.Allow, ","); got != "read_file" {
		t.Fatalf("allowlist = %q, want read_file", got)
	}
	if spec.Tools.Tier != "standard" || spec.Policies.ApprovalMode != "safe" {
		t.Fatalf("policy = %+v tools = %+v", spec.Policies, spec.Tools)
	}
	for key, want := range map[string]string{
		"buckley.resolved_tier":    "execute",
		"buckley.reasoning_effort": "high",
		"buckley.step_cap":         "4",
		"buckley.output_schema":    "buckley.artifact/v1",
	} {
		if got := spec.Metadata[key]; got != want {
			t.Fatalf("metadata[%q] = %q, want %q", key, got, want)
		}
	}
	for _, want := range []string{"Keep changes focused.", "Follow the child review protocol."} {
		if !strings.Contains(spec.Instructions.Prompt, want) {
			t.Fatalf("instructions missing %q:\n%s", want, spec.Instructions.Prompt)
		}
	}
}

func TestRunAgentRun_ChildContractAppearsInDryRunProjection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(`
version: buckley.agent/v1
name: daily
tools:
  allow: [read_file, write_file]
subagents:
  - name: reviewer
    tool_tier: standard
`), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	contract, err := subagent.EncodeChildContract(subagent.ChildContract{
		SchemaVersion:    "buckley.subagent-contract/v1",
		Model:            "child/model",
		Tier:             "execute",
		Effort:           "medium",
		SystemPrompt:     "Use evidence before conclusions.",
		AllowedTools:     []string{"read_file", "search_text"},
		ToolsConstrained: true,
		StepCap:          3,
		ApprovalPosture:  "safe",
		OutputSchema:     "buckley.artifact/v1",
	})
	if err != nil {
		t.Fatalf("EncodeChildContract: %v", err)
	}
	t.Setenv(subagent.ChildContractEnv, contract)

	output := captureStdout(t, func() {
		if err := runAgentRun([]string{"--dry-run", "--json", path, "reviewer", "inspect this"}); err != nil {
			t.Fatalf("runAgentRun: %v", err)
		}
	})
	var preview agentRunPreviewSnapshot
	if err := json.Unmarshal([]byte(output), &preview); err != nil {
		t.Fatalf("unmarshal preview: %v\n%s", err, output)
	}
	if preview.Model != "child/model" || preview.ToolTier != "standard" || strings.Join(preview.AllowedTools, ",") != "read_file" {
		t.Fatalf("preview contract projection = %+v", preview)
	}
	if preview.ResolvedTier != "execute" || preview.ReasoningEffort != "medium" || preview.StepCap != 3 || preview.OutputSchema != "buckley.artifact/v1" || preview.ApprovalMode != "safe" || !preview.Instructions {
		t.Fatalf("preview resolved fields = %+v", preview)
	}
}
