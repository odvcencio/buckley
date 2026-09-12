package main

import (
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentspec"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

func TestSourceExtractorTemplate(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locating template test source")
	}
	profile, err := agentspec.LoadRuntimeProfile(filepath.Join(filepath.Dir(source), "..", "..", "templates", "agents", "source-extractor.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := profile.Spec.Metadata["buckley.output_schema"]; got != artifactv1.SchemaVersion {
		t.Fatalf("root output schema = %q, want %q", got, artifactv1.SchemaVersion)
	}
	profile, err = profile.SubagentProfile("extract")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Spec.Models != (agentspec.ModelSpec{}) {
		t.Fatalf("template must not pin models: %+v", profile.Spec.Models)
	}
	for _, limit := range []int{0, 3, 8, 12, 20} {
		preview := buildAgentRunPreviewSnapshot(agentRunOptions{
			subagent: "extract", model: "openai_compatible/future-model", maxToolCalls: limit,
		}, profile)
		wantLimit := 12
		if limit > 0 && limit < wantLimit {
			wantLimit = limit
		}
		if preview.ToolTier != "read_only" || preview.TaskIntent != "read_only" || preview.ApprovalMode != "safe" {
			t.Fatalf("unsafe or ambiguous execution profile: %+v", preview)
		}
		if !reflect.DeepEqual(preview.AllowedTools, []string{"read_file", "search_text"}) {
			t.Fatalf("unexpected tools: %v", preview.AllowedTools)
		}
		if preview.OutputSchema != artifactv1.SchemaVersion {
			t.Fatalf("extract worker lost captured-source output contract: %+v", preview)
		}
		if preview.MaxToolCalls != wantLimit {
			t.Fatalf("max calls=%d, want%d", preview.MaxToolCalls, wantLimit)
		}
		if preview.Model != "openai_compatible/future-model (flag override)" {
			t.Fatalf("model override lost: %s", preview.Model)
		}
	}
	if profile.Spec.Sandbox.Mode != "workspace" {
		t.Fatalf("source worker sandbox = %q, want workspace", profile.Spec.Sandbox.Mode)
	}
	prompt := profile.Spec.Instructions.Prompt
	if strings.TrimSpace(prompt) == "" || strings.Contains(prompt, "TODO") || strings.Contains(prompt, `\n`) {
		t.Fatalf("unfinished or incorrectly escaped prompt: %q", prompt)
	}
	if words := len(strings.Fields(prompt)); words > 180 {
		t.Fatalf("extractive prompt has %d words, want at most 180", words)
	}
}
