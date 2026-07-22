package tool

import (
	"fmt"
	"testing"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestBuiltinToolSchemasAreOpenRouterCompatible(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&builtin.TodoTool{})

	for _, current := range registry.List() {
		current := current
		t.Run(current.Name(), func(t *testing.T) {
			schema := current.Parameters()
			if schema.Type != "object" {
				t.Fatalf("root type = %q, want object", schema.Type)
			}
			if len(schema.Properties) == 0 && schema.AdditionalProperties == nil {
				t.Fatal("object schema must define properties or additionalProperties")
			}
			for _, required := range schema.Required {
				if _, ok := schema.Properties[required]; !ok {
					t.Errorf("required property %q is not defined", required)
				}
			}
			for name, property := range schema.Properties {
				validateOpenRouterProperty(t, name, property)
			}
		})
	}
}

func validateOpenRouterProperty(t *testing.T, path string, property builtin.PropertySchema) {
	t.Helper()
	switch property.Type {
	case "array":
		if property.Items == nil {
			t.Errorf("%s: array schema is missing items", path)
			return
		}
		validateOpenRouterProperty(t, path+"[]", *property.Items)
	case "object":
		if len(property.Properties) == 0 && property.AdditionalProperties == nil {
			t.Errorf("%s: object schema must define properties or additionalProperties", path)
		}
		for _, required := range property.Required {
			if _, ok := property.Properties[required]; !ok {
				t.Errorf("%s: required property %q is not defined", path, required)
			}
		}
		for name, child := range property.Properties {
			validateOpenRouterProperty(t, fmt.Sprintf("%s.%s", path, name), child)
		}
	case "string", "number", "integer", "boolean":
	case "":
		t.Errorf("%s: JSON schema type is required", path)
	default:
		t.Errorf("%s: unsupported JSON schema type %q", path, property.Type)
	}
}
