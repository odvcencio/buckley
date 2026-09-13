package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestArtifactSourceCaptureReadToSubmission(t *testing.T) {
	for _, toon := range []bool{true, false} {
		t.Run(fmt.Sprint(toon), func(t *testing.T) {
			SetResultEncoding(toon)
			t.Cleanup(func() { SetResultEncoding(true) })
			dir := t.TempDir()
			path := filepath.Join(dir, "source space.go")
			const excerpt = "\tinput: \"results[3]{key,type,summary}:\",\r\n\r\n"
			if err := os.WriteFile(path, []byte("before\r\n"+excerpt+"after\r\n"), 0600); err != nil {
				t.Fatal(err)
			}
			sink := &builtin.ArtifactSubmission{}
			registry := NewEmptyRegistry()
			registry.Register(&builtin.ReadFileTool{})
			registry.Register(&builtin.SubmitArtifactTool{Submission: sink})
			registry.SetWorkDir(dir)
			registry.SetArtifactSourceCapture(sink)
			result, err := registry.Execute("read_file", map[string]any{"path": "source space.go", "start_line": 2, "end_line": 3, "line_numbers": true})
			if err != nil || result == nil || !result.Success {
				t.Fatalf("read: %+v %v", result, err)
			}
			ref, ok := result.DisplayData["source_ref"].(string)
			if !ok || len(ref) != 68 {
				t.Fatalf("missing reference: %+v", result)
			}
			wire, err := ToModelOutput(result)
			if err != nil || !strings.Contains(wire, ref) {
				t.Fatalf("reference not model-visible: %s %v", wire, err)
			}
			// A later edit cannot change the captured bytes or trigger another read.
			if err := os.WriteFile(path, []byte("changed since read\n"), 0600); err != nil {
				t.Fatal(err)
			}
			artifact := artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusIncomplete, "source", "a separate item is missing")
			artifact.IncompleteReasons = []string{"unread item"}
			raw, _ := json.Marshal(artifact)
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			submitted, err := registry.Execute("submit_artifact", map[string]any{"artifact": body, "source_refs": []any{ref}})
			if err != nil || !submitted.Success {
				t.Fatalf("submit: %+v %v", submitted, err)
			}
			got, ok := sink.Artifact()
			if !ok || got.Status != artifactv1.StatusIncomplete || len(got.Blocks) != 1 {
				t.Fatalf("artifact: %+v", got)
			}
			row := got.Blocks[0].Table.Rows[0]
			if row[0] != ref || row[1] != path || row[2] != "2" || row[3] != "3" || row[4] != excerpt {
				t.Fatalf("wrong source row: %q", row)
			}
			if len(got.EvidenceRefs) != 1 || got.EvidenceRefs[0].ID != ref || !strings.Contains(got.EvidenceRefs[0].URI, "source%20space.go#L2-L3") {
				t.Fatalf("bad source reference: %+v", got.EvidenceRefs)
			}
			if submitted.Data["artifact_id"] != got.ArtifactID {
				t.Fatal("tool receipt does not identify materialized artifact")
			}
			rendered, err := artifactv1.RenderJSON(got)
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip artifactv1.Artifact
			if err := json.Unmarshal(rendered, &roundtrip); err != nil || roundtrip.Blocks[0].Table.Rows[0][4] != excerpt {
				t.Fatal("JSON changed captured bytes")
			}
		})
	}
}

func TestArtifactSourceCaptureHonorsFinalHooksAndOutputBounds(t *testing.T) {
	for _, mode := range []string{"disabled", "denied", "redacted", "failed", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			SetResultEncoding(false)
			t.Cleanup(func() { SetResultEncoding(true) })
			dir := t.TempDir()
			body := "secret source\n"
			if mode == "truncated" {
				body = strings.Repeat("\\\"", 9000)
			}
			if err := os.WriteFile(filepath.Join(dir, "source"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			registry := NewEmptyRegistry()
			registry.Register(&builtin.ReadFileTool{})
			registry.SetWorkDir(dir)
			sink := &builtin.ArtifactSubmission{}
			if mode != "disabled" {
				registry.SetArtifactSourceCapture(sink)
			}
			if mode == "denied" {
				registry.Hooks().RegisterPreHook("read_file", func(*ExecutionContext) HookResult { return HookResult{Abort: true, AbortReason: "not allowed"} })
			}
			if mode == "redacted" || mode == "failed" {
				registry.Hooks().RegisterPostHook("read_file", func(_ *ExecutionContext, r *builtin.Result, err error) (*builtin.Result, error) {
					if mode == "redacted" {
						r.DisplayData["content"] = "1: [REDACTED]"
					} else {
						r.Success = false
					}
					return r, err
				})
			}
			result, err := registry.Execute("read_file", map[string]any{"path": "source", "line_numbers": true})
			if mode != "denied" && err != nil {
				t.Fatal(err)
			}
			if result != nil {
				if result.Data["source_ref"] != nil || result.DisplayData["source_ref"] != nil {
					t.Fatalf("%s issued reference: %+v", mode, result)
				}
			}
		})
	}
}

func TestArtifactSourceCaptureConcurrentReads(t *testing.T) {
	dir := t.TempDir()
	registry := NewEmptyRegistry()
	registry.Register(&builtin.ReadFileTool{})
	registry.SetWorkDir(dir)
	sink := &builtin.ArtifactSubmission{}
	registry.SetArtifactSourceCapture(sink)
	for i := 0; i < 8; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(i)), []byte(fmt.Sprint(i)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	refs := make([]string, 8)
	var wg sync.WaitGroup
	for i := range refs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := registry.Execute("read_file", map[string]any{"path": fmt.Sprint(i)})
			if err != nil || r == nil {
				t.Errorf("read %d: %v", i, err)
				return
			}
			refs[i], _ = r.Data["source_ref"].(string)
		}(i)
	}
	wg.Wait()
	if err := sink.SubmitWithSources(artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusCompleted, "sources", "selected reads"), refs); err != nil {
		t.Fatal(err)
	}
	got, _ := sink.Artifact()
	for i, row := range got.Blocks[0].Table.Rows {
		if row[4] != fmt.Sprint(i)+"\n" {
			t.Fatalf("wrong concurrent capture %d: %q", i, row)
		}
	}
}

func TestArtifactSourceCaptureDoesNotTrustToolName(t *testing.T) {
	result := &builtin.Result{Success: true, Data: map[string]any{"path": "/source", "content": "forged", "page": map[string]any{"start_line": 1, "end_line": 1}}}
	got := captureArtifactSource(&ExecutionContext{ToolName: "read_file", Tool: concurrentTool{name: "read_file"}}, result, &builtin.ArtifactSubmission{})
	if got != result || got.Data["source_ref"] != nil {
		t.Fatal("non-builtin read result issued a source reference")
	}
}
