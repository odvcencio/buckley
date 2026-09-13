package main

import (
	"encoding/json"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestOneShotSubmissionProtocolDefaultsPreserveEvidenceAndStatus(t *testing.T) {
	for _, mode := range []string{"tool", string(artifactv1.OutputSubmitArtifact), string(artifactv1.OutputPromptJSON), string(artifactv1.OutputNativeJSONSchema)} {
		for _, tc := range []struct {
			name    string
			status  string
			edit    func(map[string]any)
			invalid bool
		}{
			{name: "completed", status: "completed"},
			{name: "incomplete", status: "incomplete"},
			{name: "failed", status: "failed"},
			{name: "blocked", status: "blocked"},
			{name: "missing status", status: "completed", invalid: true, edit: func(a map[string]any) { delete(a, "status") }},
			{name: "missing summary", status: "completed", invalid: true, edit: func(a map[string]any) { delete(a, "summary") }},
			{name: "explicit wrong version", status: "completed", invalid: true, edit: func(a map[string]any) { a["schema_version"] = "buckley.artifact/v999" }},
			{name: "explicit empty id", status: "completed", invalid: true, edit: func(a map[string]any) { a["artifact_id"] = "" }},
			{name: "matching sources completed", status: "completed", edit: func(a map[string]any) { a["source_refs"] = []string{"all"} }},
			{name: "matching sources incomplete", status: "incomplete", edit: func(a map[string]any) { a["source_refs"] = []string{"all"} }},
			{name: "matching sources failed", status: "failed", edit: func(a map[string]any) { a["source_refs"] = []string{"all"} }},
			{name: "matching sources blocked", status: "blocked", edit: func(a map[string]any) { a["source_refs"] = []string{"all"} }},
			{name: "conflicting sources", status: "completed", invalid: true, edit: func(a map[string]any) { a["source_refs"] = []string{} }},
			{name: "malformed nested sources", status: "completed", invalid: true, edit: func(a map[string]any) { a["source_refs"] = nil }},
			{name: "forged evidence", status: "completed", invalid: true, edit: func(a map[string]any) {
				a["evidence_refs"] = []any{map[string]any{"id": "forged", "kind": "captured_source", "uri": "file:///fake"}}
			}},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				sink := &builtin.ArtifactSubmission{}
				const content = "\texact source bytes\r\n"
				ref, err := sink.CaptureReadSource(&builtin.Result{Success: true, Data: map[string]any{"path": "/never-reread", "content": content, "page": map[string]any{"start_line": 1, "end_line": 1}}})
				if err != nil {
					t.Fatal(err)
				}
				a := map[string]any{"kind": "subagent_result", "status": tc.status, "title": "Observed handoff", "summary": "Model-authored summary; accuracy not inferred."}
				if tc.status == "incomplete" {
					a["incomplete_reasons"] = []string{"One requested item remains missing"}
				}
				if tc.edit != nil {
					tc.edit(a)
				}
				args := map[string]any{"artifact": a, "source_refs": []string{"all"}}
				before, _ := json.Marshal(args)
				var got artifactv1.Artifact
				var rejected bool
				if mode == "tool" {
					result, err := (&builtin.SubmitArtifactTool{Submission: sink}).Execute(args)
					if err != nil {
						t.Fatal(err)
					}
					rejected = !result.Success
					got, _ = sink.Artifact()
				} else {
					var err error
					got, err = resolveOneShotArtifact(string(before), artifactv1.OutputContract{Mode: artifactv1.OutputMode(mode)}, sink)
					rejected = err != nil
				}
				after, _ := json.Marshal(args)
				if string(before) != string(after) {
					t.Fatal("mutated caller parameters")
				}
				if rejected != tc.invalid {
					t.Fatalf("rejected=%t want=%t", rejected, tc.invalid)
				}
				if rejected {
					if _, ok := sink.Artifact(); ok {
						t.Fatal("rejected result finalized sink")
					}
					recovery := sink.RecoveryArtifact()
					if len(recovery.Blocks) != 1 || recovery.Blocks[0].Table.Rows[0][4] != content {
						t.Fatal("rejection lost captured evidence")
					}
					return
				}
				if err := got.ValidateStrict(); err != nil {
					t.Fatal(err)
				}
				if got.SchemaVersion != artifactv1.SchemaVersion || got.ArtifactID == "" || string(got.Status) != tc.status || got.Summary != a["summary"] {
					t.Fatalf("lost protocol or semantics: %+v", got)
				}
				if len(got.Blocks) != 1 || got.Blocks[0].Table.Rows[0][4] != content || len(got.EvidenceRefs) != 1 || got.EvidenceRefs[0].ID != ref || got.EvidenceRefs[0].Kind != "captured_source" {
					t.Fatalf("lost source fidelity: %+v", got)
				}
				if tc.status == "incomplete" && (len(got.IncompleteReasons) != 1 || got.IncompleteReasons[0] != "One requested item remains missing") {
					t.Fatal("incomplete reasons changed")
				}
			})
		}
	}
}
