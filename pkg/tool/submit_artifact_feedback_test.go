package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestSubmissionFieldFeedbackSurvivesModelEncodingAndRecovery(t *testing.T) {
	for _, toon := range []bool{false, true} {
		name := "JSON"
		if toon {
			name = "TOON"
		}
		t.Run(name, func(t *testing.T) {
			SetResultEncoding(toon)
			t.Cleanup(func() { SetResultEncoding(true) })
			dir := t.TempDir()
			const bytes = "\texact capture\r\n"
			if err := os.WriteFile(filepath.Join(dir, "source"), []byte(bytes), 0600); err != nil {
				t.Fatal(err)
			}
			sink := &builtin.ArtifactSubmission{}
			registry := NewEmptyRegistry()
			registry.Register(&builtin.ReadFileTool{})
			registry.Register(&builtin.SubmitArtifactTool{Submission: sink})
			registry.SetWorkDir(dir)
			registry.SetArtifactSourceCapture(sink)
			read, err := registry.Execute("read_file", map[string]any{"path": "source", "line_numbers": true})
			if err != nil || !read.Success {
				t.Fatalf("read=%+v %v", read, err)
			}
			failure, err := registry.Execute("submit_artifact", map[string]any{"artifact": map[string]any{"summary": "PRIVATE_SUMMARY_SENTINEL"}, "source_refs": []string{"all"}})
			if err != nil || failure.Success {
				t.Fatalf("invalid submission=%+v %v", failure, err)
			}
			wire, err := ToModelOutput(failure)
			if err != nil {
				t.Fatal(err)
			}
			recovery := sink.RecoveryArtifact()
			for _, field := range []string{"artifact.kind:", "artifact.status:", "artifact.title:"} {
				if !strings.Contains(wire, field) || len(recovery.Diagnostics) != 1 || !strings.Contains(recovery.Diagnostics[0].Message, field) {
					t.Fatalf("field %q lost from feedback or recovery", field)
				}
			}
			if strings.Contains(wire, "PRIVATE_SUMMARY_SENTINEL") {
				t.Fatal("feedback echoed producer content")
			}
			if len(recovery.Blocks) != 1 || recovery.Blocks[0].Table.Rows[0][4] != bytes {
				t.Fatal("rejected submission damaged capture")
			}
			correction, err := registry.Execute("submit_artifact", map[string]any{"artifact": map[string]any{"kind": "subagent_result", "status": "incomplete", "title": "Captured source", "summary": "A source page was read; remaining work is incomplete.", "incomplete_reasons": []string{"Unfinished verification"}}, "source_refs": []string{"all"}})
			if err != nil || !correction.Success {
				t.Fatalf("correction=%+v %v", correction, err)
			}
			final, ok := sink.Artifact()
			if !ok || final.Status != artifactv1.StatusIncomplete || len(final.Diagnostics) != 0 || final.Blocks[0].Table.Rows[0][4] != bytes {
				t.Fatal("correction lost source/status or imported stale failure")
			}
		})
	}
}
