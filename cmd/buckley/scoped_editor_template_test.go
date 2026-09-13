package main

import (
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/agentspec"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/config"
)

func TestScopedEditorTemplate(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locating template source")
	}
	profile, err := agentspec.LoadRuntimeProfile(filepath.Join(filepath.Dir(source), "..", "..", "templates", "agents", "scoped-editor.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	profile, err = profile.SubagentProfile("edit")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Spec.Models != (agentspec.ModelSpec{}) {
		t.Fatalf("editor must inherit model settings: %+v", profile.Spec.Models)
	}
	if profile.Spec.Sandbox.Mode != "workspace" || profile.Spec.Sandbox.Network == nil || *profile.Spec.Sandbox.Network {
		t.Fatalf("unexpected sandbox: %+v", profile.Spec.Sandbox)
	}
	for _, modelID := range []string{"openai_compatible/future-model", "anthropic/future-model"} {
		for _, limit := range []int{0, 3, 8, 12, 20} {
			preview := buildAgentRunPreviewSnapshot(agentRunOptions{subagent: "edit", model: modelID, maxToolCalls: limit, taskIntent: agentloop.MutationIntent}, profile)
			wantLimit := 12
			if limit > 0 && limit < wantLimit {
				wantLimit = limit
			}
			if preview.MaxToolCalls != wantLimit || preview.ToolTier != "standard" || preview.TaskIntent != "mutation" || preview.ApprovalMode != "safe" {
				t.Fatalf("incorrect editor limits: %+v", preview)
			}
			if !reflect.DeepEqual(preview.AllowedTools, []string{"edit_file", "read_file", "run_tests", "search_text", "write_file"}) {
				t.Fatalf("unexpected tools: %v", preview.AllowedTools)
			}
			if preview.OutputSchema != artifactv1.SchemaVersion || preview.Model != modelID+" (flag override)" {
				t.Fatalf("lost output/model contract: %+v", preview)
			}
		}
	}
	cfg := config.DefaultConfig()
	cfg.Models.Execution = "caller/model"
	cfg.Models.Reasoning = "low"
	cfg.Sandbox.AllowNetwork = true
	cfg.Approval.AllowNetwork = true
	profile.ApplyToConfig(cfg)
	if cfg.Models.Execution != "caller/model" || cfg.Models.Reasoning != "low" {
		t.Fatal("editor changed caller model configuration")
	}
	if cfg.Sandbox.AllowNetwork || cfg.Approval.AllowNetwork {
		t.Fatal("editor enabled tool network access")
	}
	prompt := profile.Spec.Instructions.Prompt
	if strings.TrimSpace(prompt) == "" || strings.Contains(prompt, "TODO") || strings.Contains(prompt, `\n`) {
		t.Fatalf("unfinished prompt: %q", prompt)
	}
	if words := len(strings.Fields(prompt)); words > 190 {
		t.Fatalf("editor prompt has %d words, want at most 190", words)
	}
}
