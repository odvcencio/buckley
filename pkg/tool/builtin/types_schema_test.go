package builtin

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPropertySchemaRawSchemaPreservesKeywords(t *testing.T) {
	raw := map[string]any{
		"$id": "https://example.invalid/schema", "$defs": map[string]any{"count": map[string]any{"type": "integer", "minimum": float64(0)}},
		"oneOf": []any{map[string]any{"$ref": "#/$defs/count"}, map[string]any{"const": "unknown"}},
	}
	schema := ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"value": {Type: "string", Description: "convenience fields must not override raw schema", RawSchema: raw},
		"items": {Type: "array", Items: &PropertySchema{RawSchema: raw}},
	}}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	properties := got["properties"].(map[string]any)
	if !reflect.DeepEqual(properties["value"], raw) || !reflect.DeepEqual(properties["items"].(map[string]any)["items"], raw) {
		t.Fatalf("schema keywords changed: %s", encoded)
	}
}

func TestPropertySchemaLegacyWireFormatUnchanged(t *testing.T) {
	schema := PropertySchema{Type: "object", Description: "container", Properties: map[string]PropertySchema{
		"names": {Type: "array", Description: "values", Items: &PropertySchema{Type: "string", Description: "name", Enum: []string{"a", "b"}}, Default: []string{"a"}},
	}, Required: []string{"names"}, AdditionalProperties: false}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"type":"object","description":"container","properties":{"names":{"type":"array","description":"values","default":["a"],"items":{"type":"string","description":"name","enum":["a","b"]}}},"required":["names"],"additionalProperties":false}`
	if string(encoded) != want {
		t.Fatalf("legacy wire format changed:\ngot %s\nwant %s", encoded, want)
	}
}

func TestPropertySchemaRawSchemaEmptyAndInvalid(t *testing.T) {
	encoded, err := json.Marshal(PropertySchema{Type: "string", RawSchema: map[string]any{}})
	if err != nil || string(encoded) != "{}" {
		t.Fatalf("explicit empty raw schema must remain authoritative: %s %v", encoded, err)
	}
	if _, err := json.Marshal(PropertySchema{RawSchema: map[string]any{"invalid": func() {}}}); err == nil {
		t.Fatal("unencodable raw schema must return error, not silently use convenience fields")
	}
}
