package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestPrintOneShotRecoveryRetainsToolFailures(t *testing.T) {
	for _, withSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "errors only", true: "errors and source"}[withSource], func(t *testing.T) {
			dir := t.TempDir()
			const content = "\texact source\r\n"
			if err := os.WriteFile(filepath.Join(dir, "source"), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			sink := &builtin.ArtifactSubmission{}
			registry := tool.NewEmptyRegistry()
			registry.Register(&builtin.ReadFileTool{})
			registry.Register(&builtin.SubmitArtifactTool{Submission: sink})
			registry.SetWorkDir(dir)
			registry.SetArtifactSourceCapture(sink)
			if withSource {
				r, e := registry.Execute("read_file", map[string]any{"path": "source", "line_numbers": true})
				if e != nil || !r.Success {
					t.Fatalf("read=%+v err=%v", r, e)
				}
			}
			r, e := registry.Execute("read_file", map[string]any{"path": "missing.go"})
			if e != nil || r.Success {
				t.Fatalf("missing read=%+v err=%v", r, e)
			}
			r, e = registry.Execute("submit_artifact", map[string]any{"artifact": map[string]any{"kind": "subagent_result", "status": "incomplete", "summary": "Source missing"}, "source_refs": []string{}})
			if e != nil || r.Success {
				t.Fatalf("invalid submission=%+v err=%v", r, e)
			}
			cause := &agentloop.IncompleteTurnError{FinishReason: agentloop.FinishReasonStepCap, Reason: "PRIVATE_REASONING", FinalizationError: "PRIVATE_REASONING"}
			var stdout string
			code := 0
			stderr := captureStderr(t, func() {
				stdout = captureStdout(t, func() { code = printOneShotArtifactFailure(sink, cause, "NEVER_OBSERVED_LITERAL") })
			})
			if code != 1 || strings.Contains(stdout+stderr, "PRIVATE_REASONING") {
				t.Fatal("failure status or reasoning boundary changed")
			}
			var a artifactv1.Artifact
			if err := json.Unmarshal([]byte(stdout), &a); err != nil {
				t.Fatal(err)
			}
			if err := a.ValidateStrict(); err != nil {
				t.Fatal(err)
			}
			if a.Status != artifactv1.StatusIncomplete || len(a.Diagnostics) != 2 || !strings.Contains(a.Diagnostics[0].Message, "missing.go") || !strings.Contains(a.Diagnostics[1].Message, "title") {
				t.Fatalf("lost useful failures: %+v", a)
			}
			var sourceSeen, missingSeen bool
			for _, b := range a.Blocks {
				if b.Table == nil {
					continue
				}
				if b.Table.Headers[0] == "source_ref" {
					sourceSeen = true
					if b.Table.Rows[0][4] != content {
						t.Fatal("source changed")
					}
				}
				if b.Table.Headers[0] == "required_source_text" {
					missingSeen = b.Table.Rows[0][1] == "not_observed"
				}
			}
			if sourceSeen != withSource || !missingSeen {
				t.Fatal("recovery lost source or coverage distinction")
			}
			if _, submitted := sink.Artifact(); submitted {
				t.Fatal("recovery finalized sink")
			}
		})
	}
}
