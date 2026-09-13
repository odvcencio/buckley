package builtin

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

func TestArtifactSubmission_RejectsContradictoryCompletion(t *testing.T) {
	for _, kind := range []artifactv1.ArtifactKind{artifactv1.KindSubagentResult, artifactv1.KindAnalysis, artifactv1.KindReview} {
		for _, correction := range []string{"unfinished", "finished"} {
			t.Run(string(kind)+"/"+correction, func(t *testing.T) {
				sink := &ArtifactSubmission{}
				const literal = "\tobserved page\r\n"
				ref, err := sink.CaptureReadSource(sourceReadResult("/never-reread", literal, 1, 1))
				if err != nil {
					t.Fatal(err)
				}
				artifact := artifactv1.New(kind, artifactv1.StatusCompleted, "Handoff", "MODEL_SUMMARY_PRIVATE")
				artifact.IncompleteReasons = []string{"UNREAD_CONTEXT_PRIVATE"}
				before, err := json.Marshal(artifact)
				if err != nil {
					t.Fatal(err)
				}
				// Persisted/renderable artifacts have a broader lifecycle than a final execution submission.
				if _, err := artifactv1.NormalizeAndValidate(artifact); err != nil {
					t.Fatalf("generic artifact validation changed: %v", err)
				}
				err = sink.SubmitWithSources(artifact, []string{"all"})
				if err == nil {
					t.Fatal("accepted completed handoff that explicitly declares unfinished work")
				}
				if !strings.Contains(err.Error(), "completed") || !strings.Contains(err.Error(), "incomplete_reasons") || !strings.Contains(err.Error(), "incomplete") || strings.Contains(err.Error(), "PRIVATE") {
					t.Fatalf("missing correctable, private-safe status feedback: %v", err)
				}
				after, _ := json.Marshal(artifact)
				if string(before) != string(after) {
					t.Fatal("rejection mutated the caller's artifact")
				}
				if _, ok := sink.Artifact(); ok {
					t.Fatal("rejection finalized the submission sink")
				}
				recovery := sink.RecoveryArtifact()
				if len(recovery.EvidenceRefs) != 1 || recovery.EvidenceRefs[0].ID != ref || len(recovery.Blocks) != 1 || recovery.Blocks[0].Table.Rows[0][4] != literal {
					t.Fatalf("rejection lost immutable captured evidence: %+v", recovery)
				}
				if correction == "unfinished" {
					artifact.Status = artifactv1.StatusIncomplete
				} else {
					artifact.IncompleteReasons = nil
				}
				if err := sink.SubmitWithSources(artifact, []string{ref}); err != nil {
					t.Fatalf("corrected handoff rejected: %v", err)
				}
				got, ok := sink.Artifact()
				if !ok || got.Status != artifact.Status || !reflect.DeepEqual(got.IncompleteReasons, artifact.IncompleteReasons) || got.Summary != artifact.Summary || got.EvidenceRefs[0].ID != ref || got.Blocks[0].Table.Rows[0][4] != literal {
					t.Fatalf("correction changed declared result or captured evidence: %+v", got)
				}
				if err := sink.Submit(artifact); err == nil || err.Error() != "artifact was already submitted" {
					t.Fatalf("single accepted handoff no longer frozen: %v", err)
				}
			})
		}
	}
}

func TestSubmitArtifactTool_RepairsContradictoryCompletion(t *testing.T) {
	sink := &ArtifactSubmission{}
	tool := &SubmitArtifactTool{Submission: sink}
	input := map[string]any{"kind": "subagent_result", "status": "completed", "title": "Context", "summary": "Partial source context", "incomplete_reasons": []string{"A requested symbol was not observed"}}
	result, err := tool.Execute(map[string]any{"artifact": input, "source_refs": []string{}})
	if err != nil || result.Success || !strings.Contains(result.Error, "incomplete_reasons") {
		t.Fatalf("contradiction did not become normal tool failure: %+v err=%v", result, err)
	}
	if input["status"] != "completed" {
		t.Fatal("submission silently downgraded the model's declaration")
	}
	input["status"] = "incomplete"
	result, err = tool.Execute(map[string]any{"artifact": input, "source_refs": []string{}})
	if err != nil || !result.Success {
		t.Fatalf("explicit correction rejected: %+v err=%v", result, err)
	}
	got, _ := sink.Artifact()
	if got.Status != artifactv1.StatusIncomplete || len(got.IncompleteReasons) != 1 {
		t.Fatalf("corrected status lost: %+v", got)
	}
}

func TestArtifactSubmission_PreservesOtherLifecycleDeclarations(t *testing.T) {
	for _, status := range []artifactv1.ArtifactStatus{artifactv1.StatusDraft, artifactv1.StatusInProgress, artifactv1.StatusFailed, artifactv1.StatusBlocked, artifactv1.StatusIncomplete, artifactv1.StatusCompleted} {
		t.Run(string(status), func(t *testing.T) {
			artifact := artifactv1.New(artifactv1.KindAnalysis, status, "Context", "Model-authored result")
			if status != artifactv1.StatusCompleted {
				artifact.IncompleteReasons = []string{"Declared limitation"}
			}
			sink := &ArtifactSubmission{}
			if err := sink.Submit(artifact); err != nil {
				t.Fatalf("noncontradictory result rejected: %v", err)
			}
			got, _ := sink.Artifact()
			if got.Status != status || !reflect.DeepEqual(got.IncompleteReasons, artifact.IncompleteReasons) {
				t.Fatalf("declared result changed: %+v", got)
			}
		})
	}
}
