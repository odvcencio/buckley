package builtin

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
)

func TestSubmitArtifactSchemaAdvertisesSubmissionContract(t *testing.T) {
	raw, err := json.Marshal((&SubmitArtifactTool{}).Parameters())
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["type"] != "object" || schema["additionalProperties"] != false || !reflect.DeepEqual(schema["required"], []any{"artifact"}) {
		t.Fatalf("submission envelope changed: %s", raw)
	}
	artifact := schema["properties"].(map[string]any)["artifact"].(map[string]any)
	properties := artifact["properties"].(map[string]any)
	for name := range artifactv1.CurrentSchemaDescriptor().Fields {
		if _, ok := properties[name]; !ok {
			t.Errorf("model schema omits accepted artifact field %q", name)
		}
	}
	wantRaw, err := json.Marshal(artifactv1.SubmissionJSONSchema())
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(artifact, want) {
		t.Error("submit_artifact must advertise the submission schema, including nested blocks, references, required semantic fields, and constraints")
	}
}

func TestSubmitArtifactSchemaResolvesAndValidatesStructuredHandoff(t *testing.T) {
	raw, err := json.Marshal((&SubmitArtifactTool{}).Parameters())
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatalf("canonical references must resolve inside the submission envelope: %v", err)
	}
	artifact := artifactv1.Artifact{
		SchemaVersion: artifactv1.SchemaVersion, ArtifactID: "source-1", Kind: artifactv1.KindSubagentResult,
		Status: artifactv1.StatusIncomplete, Title: "Source evidence", Summary: "One item remains missing",
		Blocks:            []artifactv1.Block{{Kind: artifactv1.BlockTable, Table: &artifactv1.Table{Headers: []string{"item", "source"}, Rows: [][]string{{"found", "literal source"}, {"missing", "not in inspected range"}}}}},
		EvidenceRefs:      []artifactv1.EvidenceRef{{ID: "read-1", Kind: "source", URI: "source.go#L1-L4"}},
		IncompleteReasons: []string{"missing item not in inspected range"},
	}
	for _, tc := range []struct {
		name  string
		edit  func(map[string]any)
		valid bool
	}{
		{name: "incomplete evidence table", valid: true},
		{name: "host protocol defaults", valid: true, edit: func(a map[string]any) { delete(a, "schema_version"); delete(a, "artifact_id") }},
		{name: "wrong explicit version", edit: func(a map[string]any) { a["schema_version"] = "buckley.artifact/v999" }},
		{name: "null explicit id", edit: func(a map[string]any) { a["artifact_id"] = nil }},
		{name: "unknown artifact field", edit: func(a map[string]any) { a["not_a_field"] = true }},
		{name: "unknown status", edit: func(a map[string]any) { a["status"] = "looks_good" }},
		{name: "missing title", edit: func(a map[string]any) { delete(a, "title") }},
		{name: "wrong table shape", edit: func(a map[string]any) {
			a["blocks"] = []any{map[string]any{"kind": "table", "table": map[string]any{"columns": []any{"item"}}}}
		}},
		{name: "mismatched block payload", edit: func(a map[string]any) {
			a["blocks"] = []any{map[string]any{"kind": "code", "table": map[string]any{"headers": []any{"item"}}}}
		}},
		{name: "invalid evidence reference", edit: func(a map[string]any) { a["evidence_refs"] = []any{map[string]any{"uri": "source.go"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(artifact)
			if err != nil {
				t.Fatal(err)
			}
			var data map[string]any
			if err := json.Unmarshal(raw, &data); err != nil {
				t.Fatal(err)
			}
			if tc.edit != nil {
				tc.edit(data)
			}
			if err := resolved.Validate(map[string]any{"artifact": data}); (err == nil) != tc.valid {
				t.Fatalf("schema validation: err=%v, want valid=%t", err, tc.valid)
			}
		})
	}
}
