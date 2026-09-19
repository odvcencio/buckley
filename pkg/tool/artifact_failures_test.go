package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestRegistryRecoveryRetainsFinalToolFailures(t *testing.T) {
	for _, mode := range []string{"result failure", "returned error", "post-hook replacement", "denied", "unknown", "empty name", "canceled", "success", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "source"), []byte("PRIVATE_RESULT_PAYLOAD\n"), 0600); err != nil {
				t.Fatal(err)
			}
			registry := NewEmptyRegistry()
			registry.Register(&builtin.ReadFileTool{})
			registry.SetWorkDir(dir)
			sink := &builtin.ArtifactSubmission{}
			if mode != "disabled" {
				registry.SetArtifactSourceCapture(sink)
			}
			name := "read_file"
			params := map[string]any{"path": "source", "unused_note": "PRIVATE_ARGUMENT_VALUE"}
			ctx := context.Background()
			switch mode {
			case "result failure", "disabled":
				params["path"] = "missing"
			case "returned error":
				registry.Hooks().RegisterPostHook(name, func(_ *ExecutionContext, _ *builtin.Result, _ error) (*builtin.Result, error) {
					return nil, errors.New("post-hook boundary failure")
				})
			case "post-hook replacement":
				registry.Hooks().RegisterPostHook(name, func(_ *ExecutionContext, r *builtin.Result, _ error) (*builtin.Result, error) {
					r.Success = false
					r.Error = "final hook failure"
					return r, nil
				})
			case "denied":
				registry.Hooks().RegisterPreHook(name, func(*ExecutionContext) HookResult {
					return HookResult{Abort: true, AbortReason: "blocked by test policy"}
				})
			case "unknown":
				name = "unknown_tool"
			case "empty name":
				name = ""
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "success":
				registry.Hooks().RegisterPostHook(name, func(_ *ExecutionContext, r *builtin.Result, e error) (*builtin.Result, error) {
					r.Error = "not a failed outcome"
					return r, e
				})
			}
			result, err := registry.ExecuteWithContext(ctx, name, params)
			if mode == "success" && (err != nil || result == nil || !result.Success) {
				t.Fatalf("successful tool changed: %+v %v", result, err)
			}
			if mode != "success" && err == nil && (result == nil || result.Success) {
				t.Fatalf("failed outcome changed: %+v %v", result, err)
			}
			got := sink.RecoveryArtifact()
			if mode == "success" || mode == "disabled" {
				if len(got.Diagnostics) != 0 {
					t.Fatal("successful or disabled call recorded as failure")
				}
				return
			}
			if len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != "tool_execution_failed" || got.Status != artifactv1.StatusIncomplete || len(got.EvidenceRefs) != 0 || len(got.Blocks) != 0 {
				t.Fatalf("failure recovery lost or invented evidence: %+v", got)
			}
			body, e := artifactv1.RenderJSON(got)
			if e != nil {
				t.Fatal(e)
			}
			if strings.Contains(string(body), "PRIVATE_ARGUMENT_VALUE") || strings.Contains(string(body), "PRIVATE_RESULT_PAYLOAD") {
				t.Fatal("journal retained full arguments or tool payload")
			}
			if mode == "post-hook replacement" && !strings.Contains(got.Diagnostics[0].Message, "final hook failure") {
				t.Fatal("journal observed before final hook")
			}
		})
	}
	var nilRegistry *Registry
	for _, name := range []string{"", "missing"} {
		if _, err := nilRegistry.Execute(name, nil); err == nil {
			t.Fatal("nil registry error behavior changed")
		}
	}
}

func TestRegistryRecoveryKeepsReadAndSubmissionFailures(t *testing.T) {
	sink := &builtin.ArtifactSubmission{}
	registry := NewEmptyRegistry()
	registry.Register(&builtin.ReadFileTool{})
	registry.Register(&builtin.SubmitArtifactTool{Submission: sink})
	registry.SetWorkDir(t.TempDir())
	registry.SetArtifactSourceCapture(sink)
	result, err := registry.Execute("read_file", map[string]any{"path": "missing.go"})
	if err != nil || result.Success {
		t.Fatalf("missing read: %+v %v", result, err)
	}
	result, err = registry.Execute("submit_artifact", map[string]any{"artifact": map[string]any{"kind": "subagent_result", "status": "incomplete", "summary": "A source is missing"}, "source_refs": []string{}})
	if err != nil || result.Success {
		t.Fatalf("malformed submission: %+v %v", result, err)
	}
	got := sink.RecoveryArtifact()
	if len(got.Diagnostics) != 2 || !strings.Contains(got.Diagnostics[0].Message, "read_file") || !strings.Contains(got.Diagnostics[1].Message, "title") {
		t.Fatalf("lost actionable failure sequence: %+v", got)
	}
	result, err = registry.Execute("submit_artifact", map[string]any{"artifact": map[string]any{"kind": "subagent_result", "status": "incomplete", "title": "Missing source", "summary": "A source is missing", "incomplete_reasons": []string{"missing.go could not be read"}}, "source_refs": []string{}})
	if err != nil || !result.Success {
		t.Fatalf("corrected submission: %+v %v", result, err)
	}
	submitted, _ := sink.Artifact()
	if len(submitted.Diagnostics) != 0 || len(sink.RecoveryArtifact().Diagnostics) != 0 {
		t.Fatal("stale journal changed submitted result")
	}
}
