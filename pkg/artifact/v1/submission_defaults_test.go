package artifactv1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestDecodeSubmitArtifactDefaultsOnlyAbsentProtocolFields(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"schema_version": SchemaVersion, "artifact_id": "caller-id", "kind": "subagent_result", "status": "incomplete", "title": "Observed result", "summary": "Verification remains incomplete.", "incomplete_reasons": []string{"No test run"}}
	}
	for _, tc := range []struct {
		name string
		omit []string
	}{
		{"version", []string{"schema_version"}},
		{"id", []string{"artifact_id"}},
		{"both", []string{"schema_version", "artifact_id"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := base()
			for _, key := range tc.omit {
				delete(input, key)
			}
			raw, err := json.Marshal(map[string]any{"artifact": input})
			if err != nil {
				t.Fatal(err)
			}
			got, err := DecodeSubmitArtifact(raw)
			if err != nil {
				t.Fatal(err)
			}
			again, err := DecodeSubmitArtifact(raw)
			if err != nil || got.ArtifactID == "" || got.ArtifactID != again.ArtifactID || got.SchemaVersion != SchemaVersion {
				t.Fatalf("unstable protocol defaults: %+v, %v", got, err)
			}
			if err := got.ValidateStrict(); err != nil {
				t.Fatal(err)
			}
			if got.Status != StatusIncomplete || got.Title != input["title"] || got.Summary != input["summary"] || !reflect.DeepEqual(got.IncompleteReasons, []string{"No test run"}) {
				t.Fatalf("semantic fields changed: %+v", got)
			}
			if _, present := input["artifact_id"]; present && got.ArtifactID != "caller-id" {
				t.Fatal("caller ID replaced")
			}
			canonical, _ := json.Marshal(input)
			if _, _, err := decodeAndValidate(canonical); err == nil {
				t.Fatal("canonical artifact accepted missing protocol field")
			}
		})
	}
	for _, field := range []string{"schema_version", "artifact_id", "kind", "status", "title", "summary"} {
		for _, value := range []any{"", nil, 7} {
			t.Run(field+"/explicit/"+fmt.Sprintf("%v", value), func(t *testing.T) {
				input := base()
				input[field] = value
				raw, _ := json.Marshal(map[string]any{"artifact": input})
				if _, err := DecodeSubmitArtifact(raw); err == nil {
					t.Fatalf("accepted explicit invalid %s=%v", field, value)
				}
			})
		}
	}
	for _, field := range []string{"kind", "status", "title", "summary"} {
		t.Run(field+"/absent", func(t *testing.T) {
			input := base()
			delete(input, field)
			delete(input, "schema_version")
			delete(input, "artifact_id")
			raw, _ := json.Marshal(map[string]any{"artifact": input})
			if _, err := DecodeSubmitArtifact(raw); err == nil {
				t.Fatalf("invented missing semantic field %s", field)
			}
		})
	}
	for _, tc := range []struct {
		field string
		value any
	}{
		{"schema_version", "buckley.artifact/v999"},
		{"artifact_id", "invalid id"},
		{"status", "complete"},
		{"unknown_field", true},
		{"source_refs", []string{"all"}},
	} {
		t.Run(tc.field+"/invalid", func(t *testing.T) {
			input := base()
			input[tc.field] = tc.value
			raw, _ := json.Marshal(map[string]any{"artifact": input})
			if _, err := DecodeSubmitArtifact(raw); err == nil {
				t.Fatalf("accepted invalid %s", tc.field)
			}
		})
	}
	for _, raw := range []string{
		`{"artifact":{"SCHEMA_VERSION":"wrong","kind":"analysis","status":"completed","title":"x","summary":"y"}}`,
		`{"artifact":{"ARTIFACT_ID":null,"kind":"analysis","status":"completed","title":"x","summary":"y"}}`,
		`{"artifact":{"ARTIFACT_ID":"","kind":"analysis","status":"completed","title":"x","summary":"y"}}`,
		`{}`, `{"artifact":null}`, `{"artifact":[]}`, `{"artifact":{}} {}`, `{"artifact":{},"extra":true}`} {
		if _, err := DecodeSubmitArtifact([]byte(raw)); err == nil {
			t.Fatalf("accepted malformed envelope %s", raw)
		}
	}
}

func TestSubmissionJSONSchemaDiffersOnlyInProtocolRequirements(t *testing.T) {
	canonical := JSONSchema()
	submission := SubmissionJSONSchema()
	if !reflect.DeepEqual(submission["required"], []string{"kind", "status", "title", "summary"}) {
		t.Fatalf("required=%v", submission["required"])
	}
	submission["required"] = canonical["required"]
	if !reflect.DeepEqual(canonical, submission) {
		t.Fatal("submission altered canonical fields or constraints")
	}
	negotiated := NegotiatedOutput(ProviderCapabilities{ToolCalls: true})
	properties := negotiated.SubmitArtifact.Parameters["properties"].(map[string]any)
	if !reflect.DeepEqual(properties["artifact"], SubmissionJSONSchema()) {
		t.Fatal("negotiated tool schema differs from submission decoder contract")
	}
	native := NegotiatedOutput(ProviderCapabilities{NativeJSONSchema: true})
	if !reflect.DeepEqual(native.JSONSchema, canonical) {
		t.Fatal("native canonical schema changed")
	}
	submission["properties"].(map[string]any)["title"] = "mutated"
	if reflect.DeepEqual(submission, SubmissionJSONSchema()) || !reflect.DeepEqual(JSONSchema(), canonical) {
		t.Fatal("schema calls share mutable data")
	}
}
