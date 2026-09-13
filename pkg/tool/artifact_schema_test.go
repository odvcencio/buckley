package tool

import (
	"encoding/json"
	"reflect"
	"testing"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestArtifactSchemaSurvivesProviderToolSerialization(t *testing.T) {
	raw, err := json.Marshal(ToOpenAIFunction(&builtin.SubmitArtifactTool{}))
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Function struct {
			Name       string `json:"name"`
			Parameters struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Function.Name != "submit_artifact" {
		t.Fatal("wrong tool name")
	}
	var got map[string]any
	if err := json.Unmarshal(wire.Function.Parameters.Properties["artifact"], &got); err != nil {
		t.Fatal(err)
	}
	wantRaw, err := json.Marshal(artifactv1.SubmissionJSONSchema())
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("provider request lost submission artifact schema")
	}
}
