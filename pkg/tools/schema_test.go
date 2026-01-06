package tools

import (
	"encoding/json"
	"testing"
)

func TestObjectSchema(t *testing.T) {
	schema := ObjectSchema(
		map[string]Property{
			"name": StringProperty("The name"),
			"age":  IntProperty("The age"),
		},
		"name",
	)

	if schema.Type != "object" {
		t.Errorf("expected type 'object', got %q", schema.Type)
	}
	if len(schema.Properties) != 2 {
		t.Errorf("expected 2 properties, got %d", len(schema.Properties))
	}
	if len(schema.Required) != 1 || schema.Required[0] != "name" {
		t.Errorf("expected required ['name'], got %v", schema.Required)
	}
}

func TestStringEnumProperty(t *testing.T) {
	prop := StringEnumProperty("Pick one", "a", "b", "c")

	if prop.Type != "string" {
		t.Errorf("expected type 'string', got %q", prop.Type)
	}
	if len(prop.Enum) != 3 {
		t.Errorf("expected 3 enum values, got %d", len(prop.Enum))
	}
}

func TestArrayProperty(t *testing.T) {
	prop := ArrayProperty("List of names", StringProperty("A name"))

	if prop.Type != "array" {
		t.Errorf("expected type 'array', got %q", prop.Type)
	}
	if prop.Items == nil {
		t.Error("expected items to be set")
	}
	if prop.Items.Type != "string" {
		t.Errorf("expected items type 'string', got %q", prop.Items.Type)
	}
}

func TestSchemaJSONSerialization(t *testing.T) {
	schema := ObjectSchema(
		map[string]Property{
			"action": StringEnumProperty("The action", "add", "fix", "update"),
			"items":  ArrayProperty("The items", StringProperty("An item")),
		},
		"action",
	)

	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("failed to marshal schema: %v", err)
	}

	var decoded Schema
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal schema: %v", err)
	}

	if decoded.Type != "object" {
		t.Errorf("expected type 'object', got %q", decoded.Type)
	}
	if len(decoded.Properties) != 2 {
		t.Errorf("expected 2 properties, got %d", len(decoded.Properties))
	}
}
