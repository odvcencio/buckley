package builtin

import (
	"encoding/json"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

func TestSubmitArtifactFeedbackNamesInvalidFieldsAndAllowsOneCorrection(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed", "blocked"} {
		t.Run(status, func(t *testing.T) {
			sink := &ArtifactSubmission{}
			const literal = "\tliteral source\r\n"
			ref, err := sink.CaptureReadSource(sourceReadResult("/never-reread", literal, 1, 1))
			if err != nil {
				t.Fatal(err)
			}
			tool := &SubmitArtifactTool{Submission: sink}
			invalid := map[string]any{"artifact": map[string]any{"summary": "CALLER_SUMMARY_PRIVATE"}, "source_refs": []string{"all"}}
			before, _ := json.Marshal(invalid)
			result, err := tool.Execute(invalid)
			if err != nil || result.Success {
				t.Fatalf("rejected artifact=%+v, %v", result, err)
			}
			for _, field := range []string{"artifact.kind", "artifact.status", "artifact.title"} {
				if !strings.Contains(result.Error, field+":") {
					t.Fatalf("missing field %s in %q", field, result.Error)
				}
			}
			if strings.Contains(result.Error, "CALLER_SUMMARY_PRIVATE") {
				t.Fatal("rejection echoed artifact content")
			}
			after, _ := json.Marshal(invalid)
			if string(before) != string(after) {
				t.Fatal("rejected call mutated producer input")
			}
			if _, submitted := sink.Artifact(); submitted {
				t.Fatal("invalid submission finalized sink")
			}
			recovery := sink.RecoveryArtifact()
			if len(recovery.Blocks) != 1 || recovery.Blocks[0].Table.Rows[0][4] != literal {
				t.Fatal("rejection lost captured bytes")
			}
			corrected := map[string]any{"kind": "subagent_result", "status": status, "title": "Source handoff", "summary": "One source page was observed; summary remains model-authored."}
			if status == "incomplete" {
				corrected["incomplete_reasons"] = []string{"Remaining source was not read"}
			}
			result, err = tool.Execute(map[string]any{"artifact": corrected, "source_refs": []string{"all"}})
			if err != nil || !result.Success {
				t.Fatalf("single corrected submission=%+v, %v", result, err)
			}
			got, _ := sink.Artifact()
			if got.SchemaVersion != artifactv1.SchemaVersion || string(got.Status) != status || len(got.Blocks) != 1 || got.Blocks[0].Table.Rows[0][4] != literal || got.EvidenceRefs[0].ID != ref {
				t.Fatalf("correction lost semantics or snapshot: %+v", got)
			}
		})
	}
}

func TestSubmitArtifactFeedbackIncludesSingleMissingField(t *testing.T) {
	result, err := (&SubmitArtifactTool{Submission: &ArtifactSubmission{}}).Execute(map[string]any{"artifact": map[string]any{"kind": "analysis", "status": "completed", "title": "Result"}})
	if err != nil || result.Success || !strings.Contains(result.Error, "artifact.summary:") || !strings.Contains(result.Error, "summary is required") {
		t.Fatalf("unhelpful missing-summary feedback: %+v %v", result, err)
	}
}

func TestSubmitArtifactFeedbackLimitsNestedFieldErrors(t *testing.T) {
	blocks := make([]any, 20)
	for i := range blocks {
		blocks[i] = map[string]any{"kind": "prose"}
	}
	result, err := (&SubmitArtifactTool{Submission: &ArtifactSubmission{}}).Execute(map[string]any{"artifact": map[string]any{"kind": "analysis", "status": "completed", "title": "Result", "summary": "Observed work", "blocks": blocks}})
	if err != nil || result.Success {
		t.Fatalf("malformed blocks=%+v %v", result, err)
	}
	if count := strings.Count(result.Error, "prose block requires text"); count != 8 {
		t.Fatalf("returned %d errors; want bounded field list: %q", count, result.Error)
	}
	if !strings.Contains(result.Error, "12 more omitted") || len(result.Error) > 2048 {
		t.Fatalf("unbounded or silently omitted feedback: %q", result.Error)
	}
	// Invalid fields are reported in a stable order independent of map input order.
	again, _ := (&SubmitArtifactTool{Submission: &ArtifactSubmission{}}).Execute(map[string]any{"artifact": map[string]any{"blocks": blocks, "summary": "Observed work", "title": "Result", "status": "completed", "kind": "analysis"}})
	if again.Error != result.Error {
		t.Fatal("validation feedback varies by argument map order")
	}
}

func TestSubmitArtifactFeedbackPreservesStructuralAndSourceRejections(t *testing.T) {
	for _, tc := range []struct {
		name     string
		artifact any
		refs     any
	}{
		{"unknown property", map[string]any{"kind": "analysis", "status": "completed", "title": "x", "summary": "y", "PRIVATE_UNTRUSTED_FIELD": "PRIVATE_UNTRUSTED_VALUE"}, []string{}},
		{"wrong type", map[string]any{"kind": 3, "status": "completed", "title": "x", "summary": "y"}, []string{}},
		{"null artifact", nil, []string{}},
		{"unknown capture", map[string]any{"kind": "analysis", "status": "completed", "title": "x", "summary": "y"}, []string{"src_unknown"}},
		{"misplaced selector", map[string]any{"kind": "analysis", "status": "completed", "title": "x", "summary": "y", "source_refs": []string{"all"}}, []string{}},
		{"explicit wrong version", map[string]any{"schema_version": "buckley.artifact/v999", "kind": "analysis", "status": "completed", "title": "x", "summary": "y"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &ArtifactSubmission{}
			result, err := (&SubmitArtifactTool{Submission: sink}).Execute(map[string]any{"artifact": tc.artifact, "source_refs": tc.refs})
			if err != nil || result.Success || result.Error == "" {
				t.Fatalf("invalid input=%+v %v", result, err)
			}
			if strings.Contains(result.Error, "PRIVATE_UNTRUSTED") {
				t.Fatal("structural error echoed unknown input")
			}
			if _, ok := sink.Artifact(); ok {
				t.Fatal("rejected result finalized")
			}
		})
	}
}
