package main

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
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

func TestACPLoopLimitsFromChildContract_PropagatesLineageAndBudgets(t *testing.T) {
	limits, err := acpLoopLimitsFromChildContract(subagent.ChildContract{
		RunID:           " run-child ",
		ParentRunID:     " run-parent ",
		ParentSessionID: " session-parent ",
		TaskID:          " task-child ",
		StepCap:         11,
		TimeoutSeconds:  80,
		Budget: agentcoord.Budget{
			MaxToolCalls:     19,
			MaxModelRequests: 13,
			MaxElapsedSecond: 50,
			MaxCostUSD:       2.5,
		},
	})
	if err != nil {
		t.Fatalf("acpLoopLimitsFromChildContract: %v", err)
	}
	if !limits.ChildContract || limits.RunID != "run-child" || limits.ParentRunID != "run-parent" || limits.ParentSessionID != "session-parent" || limits.TaskID != "task-child" {
		t.Fatalf("limits lineage = %+v", limits)
	}
	if limits.StepCap != 11 || limits.MaxToolCalls != 19 || limits.MaxModelRequests != 13 || limits.MaxElapsedSeconds != 50 || limits.MaxCostUSD != 2.5 {
		t.Fatalf("limits budget = %+v", limits)
	}

	unbounded, err := acpLoopLimitsFromChildContract(subagent.ChildContract{})
	if err != nil {
		t.Fatalf("unbounded acpLoopLimitsFromChildContract: %v", err)
	}
	if !unbounded.ChildContract || unbounded.StepCap != 0 || unbounded.MaxToolCalls != 0 || unbounded.MaxModelRequests != 0 || unbounded.MaxElapsedSeconds != 0 || unbounded.MaxCostUSD != 0 {
		t.Fatalf("zero contract gained limits: %+v", unbounded)
	}
}

func TestACPLoopLimitsFromChildContract_RejectsNonFiniteCost(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.01} {
		_, err := acpLoopLimitsFromChildContract(subagent.ChildContract{
			Budget: agentcoord.Budget{MaxCostUSD: value},
		})
		if err == nil || !strings.Contains(err.Error(), "finite and non-negative") {
			t.Fatalf("acpLoopLimitsFromChildContract(%v) error = %v", value, err)
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

func TestRunAgentRun_AppliesChildModelBeforeDependencyInitialization(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(`
version: buckley.agent/v1
name: exact-child
subagents:
  - name: reviewer
    model: z-ai/glm-5.2
`), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	previousInit := initDependenciesFn
	previousOverride := modelOverrideFlag
	t.Cleanup(func() {
		initDependenciesFn = previousInit
		modelOverrideFlag = previousOverride
	})
	sentinel := errors.New("stop after inspecting startup routing")
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		if modelOverrideFlag != "z-ai/glm-5.2" {
			t.Fatalf("dependency initialization saw model override %q, want exact child pin", modelOverrideFlag)
		}
		cfg := config.DefaultConfig()
		applyStartupModelOverride(cfg, modelOverrideFlag)
		if _, exists := cfg.Models.FallbackChains["z-ai/glm-5.2"]; exists {
			t.Fatal("child pin retained configured OpenRouter fallback chain")
		}
		return nil, nil, nil, sentinel
	}

	err := runAgentRun([]string{path, "reviewer", "inspect this"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("runAgentRun error = %v, want sentinel", err)
	}
	if modelOverrideFlag != previousOverride {
		t.Fatalf("model override was not restored: got %q want %q", modelOverrideFlag, previousOverride)
	}
}
