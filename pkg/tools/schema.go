// Package tools provides structured tool definitions for LLM tool-use.
//
// This package is the foundation of Buckley's tool-use-first architecture.
// Instead of parsing model text output, we define structured contracts that
// models fill via tool calls.
package tools

// Schema defines a JSON Schema for tool parameters.
// This is sent to the model so it knows the exact structure to return.
type Schema struct {
	Type        string              `json:"type"`
	Description string              `json:"description,omitempty"`
	Properties  map[string]Property `json:"properties,omitempty"`
	Required    []string            `json:"required,omitempty"`
	Enum        []string            `json:"enum,omitempty"`
}

// Property defines a single property in a JSON Schema.
type Property struct {
	Type        string    `json:"type"`
	Description string    `json:"description,omitempty"`
	Enum        []string  `json:"enum,omitempty"`
	Items       *Property `json:"items,omitempty"`
	MaxLength   int       `json:"maxLength,omitempty"`
	MinLength   int       `json:"minLength,omitempty"`
	MinItems    int       `json:"minItems,omitempty"`
	MaxItems    int       `json:"maxItems,omitempty"`
	Default     any       `json:"default,omitempty"`
	Minimum     *float64  `json:"minimum,omitempty"`
	Maximum     *float64  `json:"maximum,omitempty"`
}

// ObjectSchema creates a schema for an object type with the given properties.
func ObjectSchema(props map[string]Property, required ...string) Schema {
	return Schema{
		Type:       "object",
		Properties: props,
		Required:   required,
	}
}

// StringProperty creates a string property with optional constraints.
func StringProperty(desc string) Property {
	return Property{
		Type:        "string",
		Description: desc,
	}
}

// StringEnumProperty creates a string property constrained to specific values.
func StringEnumProperty(desc string, values ...string) Property {
	return Property{
		Type:        "string",
		Description: desc,
		Enum:        values,
	}
}

// BoolProperty creates a boolean property.
func BoolProperty(desc string) Property {
	return Property{
		Type:        "boolean",
		Description: desc,
	}
}

// ArrayProperty creates an array property with the given item type.
func ArrayProperty(desc string, items Property) Property {
	return Property{
		Type:        "array",
		Description: desc,
		Items:       &items,
	}
}

// IntProperty creates an integer property.
func IntProperty(desc string) Property {
	return Property{
		Type:        "integer",
		Description: desc,
	}
}

// NumberProperty creates a number property.
func NumberProperty(desc string) Property {
	return Property{
		Type:        "number",
		Description: desc,
	}
}
