package builtin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

func TestSubmitArtifactNestedSourceRefs(t *testing.T) {
	for _, status := range []artifactv1.ArtifactStatus{artifactv1.StatusCompleted, artifactv1.StatusIncomplete, artifactv1.StatusFailed, artifactv1.StatusBlocked} {
		for _, selection := range []string{"all", "explicit", "empty"} {
			t.Run(fmt.Sprintf("%s/%s", status, selection), func(t *testing.T) {
				sink := &ArtifactSubmission{}
				ref, err := sink.CaptureReadSource(sourceReadResult("/snapshot.go", "\tvalue\r\n", 1, 1))
				if err != nil {
					t.Fatal(err)
				}
				refs := []string{"all"}
				if selection == "explicit" {
					refs = []string{ref}
				}
				if selection == "empty" {
					refs = []string{}
				}
				input := map[string]any{"kind": "analysis", "status": status, "title": "Context", "summary": "Observed context", "source_refs": refs}
				if status == artifactv1.StatusIncomplete {
					input["incomplete_reasons"] = []string{"requested context missing"}
				}
				params := map[string]any{"artifact": input}
				before, _ := json.Marshal(params)
				result, err := (&SubmitArtifactTool{Submission: sink}).Execute(params)
				if err != nil || !result.Success {
					t.Fatalf("nested selector rejected: %+v %v", result, err)
				}
				after, _ := json.Marshal(params)
				if !bytes.Equal(before, after) {
					t.Fatal("submission mutated caller input")
				}
				got, ok := sink.Artifact()
				if !ok || got.Status != status || got.Summary != "Observed context" {
					t.Fatalf("semantic fields changed: %+v", got)
				}
				if status == artifactv1.StatusIncomplete && len(got.IncompleteReasons) != 1 {
					t.Fatal("incomplete reason lost")
				}
				if selection == "empty" {
					if len(got.Blocks) != 0 || len(got.EvidenceRefs) != 0 {
						t.Fatal("empty selector implicitly retained captures")
					}
				} else if len(got.Blocks) != 1 || got.Blocks[0].Table == nil || got.Blocks[0].Table.Rows[0][4] != "\tvalue\r\n" || len(got.EvidenceRefs) != 1 || got.EvidenceRefs[0].ID != ref {
					t.Fatalf("host snapshot changed or missing: %+v", got)
				}
				wire, _ := json.Marshal(got)
				if strings.Contains(string(wire), "\"source_refs\"") {
					t.Fatal("selector leaked into canonical artifact")
				}
				if err := got.ValidateStrict(); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(map[string]any{"artifact": input})
				if _, err := artifactv1.DecodeSubmitArtifact(raw); err == nil {
					t.Fatal("canonical artifact decoder accepted selector field")
				}
			})
		}
	}
}

func TestSubmitArtifactNestedSourceRefsFailClosed(t *testing.T) {
	for _, name := range []string{"null", "string", "number", "non-string-item", "unknown", "duplicate", "all-combined", "both-equal", "both-empty", "outer-null", "unknown-field", "forged-evidence", "forged-block", "all-without-captures"} {
		t.Run(name, func(t *testing.T) {
			sink := &ArtifactSubmission{}
			ref, err := sink.CaptureReadSource(sourceReadResult("/snapshot.go", "host bytes\n", 1, 1))
			if err != nil {
				t.Fatal(err)
			}
			input := map[string]any{"kind": "analysis", "status": "completed", "title": "Context", "summary": "Observed context", "source_refs": []string{"all"}}
			params := map[string]any{"artifact": input}
			switch name {
			case "null":
				input["source_refs"] = nil
			case "string":
				input["source_refs"] = "all"
			case "number":
				input["source_refs"] = 1
			case "non-string-item":
				input["source_refs"] = []any{1}
			case "unknown":
				input["source_refs"] = []string{"src_" + strings.Repeat("f", 64)}
			case "duplicate":
				input["source_refs"] = []string{ref, ref}
			case "all-combined":
				input["source_refs"] = []string{"all", ref}
			case "both-equal":
				params["source_refs"] = []string{"all"}
			case "both-empty":
				params["source_refs"] = []string{}
			case "outer-null":
				params["source_refs"] = nil
			case "unknown-field":
				input["unexpected"] = "not canonical"
			case "forged-evidence":
				input["evidence_refs"] = []any{map[string]any{"id": ref, "kind": "captured_source", "uri": "file:///snapshot.go#L1-L1"}}
			case "forged-block":
				input["blocks"] = []any{map[string]any{"kind": "prose", "text": "model-written excerpt"}}
			case "all-without-captures":
				sink = &ArtifactSubmission{}
			}
			before, _ := json.Marshal(params)
			tool := &SubmitArtifactTool{Submission: sink}
			result, err := tool.Execute(params)
			if err != nil || result.Success || result.Error == "" {
				t.Fatalf("invalid selector accepted: %+v %v", result, err)
			}
			if _, ok := sink.Artifact(); ok {
				t.Fatal("invalid submission finalized sink")
			}
			after, _ := json.Marshal(params)
			if !bytes.Equal(before, after) {
				t.Fatal("rejection mutated caller input")
			}
			if name != "all-without-captures" {
				delete(input, "source_refs")
				delete(input, "unexpected")
				delete(input, "blocks")
				delete(input, "evidence_refs")
				params["source_refs"] = []string{ref}
				corrected, err := tool.Execute(params)
				if err != nil || !corrected.Success {
					t.Fatalf("rejected submission lost usable capture: %+v %v", corrected, err)
				}
			}
		})
	}
}
