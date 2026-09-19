package main

import (
	"encoding/json"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestResolveOneShotArtifact_RejectsContradictoryCompletionAcrossFallbackModes(t *testing.T) {
	for _, mode := range []artifactv1.OutputMode{artifactv1.OutputSubmitArtifact, artifactv1.OutputPromptJSON, artifactv1.OutputNativeJSONSchema} {
		for _, form := range []string{"envelope", "bare artifact"} {
			t.Run(string(mode)+"/"+form, func(t *testing.T) {
				sink := &builtin.ArtifactSubmission{}
				const literal = "\tobserved source\r\n"
				ref, err := sink.CaptureReadSource(&builtin.Result{Success: true, Data: map[string]any{
					"path": "/never-reread", "content": literal, "page": map[string]any{"start_line": 1, "end_line": 1},
				}})
				if err != nil {
					t.Fatal(err)
				}
				artifact := artifactv1.New(artifactv1.KindSubagentResult, artifactv1.StatusCompleted, "Context", "MODEL_SUMMARY_PRIVATE")
				artifact.IncompleteReasons = []string{"UNREAD_CONTEXT_PRIVATE"}
				var raw []byte
				if form == "envelope" {
					raw, err = json.Marshal(map[string]any{"artifact": artifact, "source_refs": []string{"all"}})
				} else {
					raw, err = artifactv1.RenderJSON(artifact)
				}
				if err != nil {
					t.Fatal(err)
				}
				contract := artifactv1.OutputContract{Mode: mode}
				if _, err := resolveOneShotArtifact(string(raw), contract, sink); err == nil || !strings.Contains(err.Error(), "incomplete_reasons") || strings.Contains(err.Error(), "PRIVATE") {
					t.Fatalf("fallback accepted contradictory declaration or exposed contents: %v", err)
				}
				if _, ok := sink.Artifact(); ok {
					t.Fatal("rejected fallback finalized the sink")
				}
				recovery := sink.RecoveryArtifact()
				if len(recovery.EvidenceRefs) != 1 || recovery.EvidenceRefs[0].ID != ref || recovery.Blocks[0].Table.Rows[0][4] != literal {
					t.Fatal("fallback rejection lost captured evidence")
				}
				artifact.Status = artifactv1.StatusIncomplete
				raw, err = json.Marshal(map[string]any{"artifact": artifact, "source_refs": []string{"all"}})
				if err != nil {
					t.Fatal(err)
				}
				got, err := resolveOneShotArtifact(string(raw), contract, sink)
				if err != nil || got.Status != artifactv1.StatusIncomplete || len(got.IncompleteReasons) != 1 || got.IncompleteReasons[0] != "UNREAD_CONTEXT_PRIVATE" || got.Summary != artifact.Summary || got.EvidenceRefs[0].ID != ref || got.Blocks[0].Table.Rows[0][4] != literal {
					t.Fatalf("fallback repair lost declared limitations or evidence: %+v err=%v", got, err)
				}
			})
		}
	}
}
