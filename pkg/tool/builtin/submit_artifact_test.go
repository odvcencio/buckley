package builtin

import (
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

func TestSubmitArtifactTool_ValidatesAndCapturesExactlyOneArtifact(t *testing.T) {
	submission := &ArtifactSubmission{}
	tool := &SubmitArtifactTool{Submission: submission}
	result, err := tool.Execute(map[string]any{
		"artifact": map[string]any{
			"schema_version": artifactv1.SchemaVersion,
			"artifact_id":    "artifact-test",
			"kind":           "analysis",
			"status":         "completed",
			"title":          "Test artifact",
			"summary":        "Validated through submit_artifact.",
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected successful submission, got %+v", result)
	}
	artifact, ok := submission.Artifact()
	if !ok || artifact.ArtifactID != "artifact-test" || artifact.Status != artifactv1.StatusCompleted {
		t.Fatalf("unexpected submitted artifact: %+v, ok=%v", artifact, ok)
	}
	result, err = tool.Execute(map[string]any{"artifact": map[string]any{
		"schema_version": artifactv1.SchemaVersion,
		"artifact_id":    "artifact-second",
		"kind":           "analysis",
		"status":         "completed",
		"title":          "Second artifact",
		"summary":        "Must be rejected.",
	}})
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if result.Success || result.Error != "artifact was already submitted" {
		t.Fatalf("expected repeated submission rejection, got %+v", result)
	}
}

func TestSubmitArtifactTool_ReturnsCorrectableFailureForMalformedArtifact(t *testing.T) {
	tool := &SubmitArtifactTool{Submission: &ArtifactSubmission{}}
	result, err := tool.Execute(map[string]any{"artifact": map[string]any{"title": "missing required fields"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success || result.Error == "" {
		t.Fatalf("expected malformed artifact to be a tool failure, got %+v", result)
	}
}
