package tool

import (
	"reflect"
	"testing"
)

func TestDecodeArguments_RecoversCompleteObjectWrappers(t *testing.T) {
	want := map[string]any{"path": "a.go", "line": float64(7)}
	tests := map[string]string{
		"plain object":          `{"path":"a.go","line":7}`,
		"JSON fence":            "```json\n{\"path\":\"a.go\",\"line\":7}\n```",
		"untyped fence":         "```\n{\"path\":\"a.go\",\"line\":7}\n```",
		"uppercase JSON fence":  "```JSON\n{\"path\":\"a.go\",\"line\":7}\n```",
		"JSON encoded object":   `"{\"path\":\"a.go\",\"line\":7}"`,
		"encoded fenced object": "```json\n\"{\\\"path\\\":\\\"a.go\\\",\\\"line\\\":7}\"\n```",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeArguments(raw)
			if err != nil {
				t.Fatalf("DecodeArguments() error = %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("DecodeArguments() = %#v, want %#v", got, want)
			}
		})
	}
}

func TestDecodeArguments_PreservesEmptyAndNullBehavior(t *testing.T) {
	for _, raw := range []string{"", "  ", "null"} {
		got, err := DecodeArguments(raw)
		if err != nil {
			t.Fatalf("DecodeArguments(%q) error = %v", raw, err)
		}
		if len(got) != 0 {
			t.Fatalf("DecodeArguments(%q) = %#v, want empty object", raw, got)
		}
		if normalized, err := NormalizeArgumentsJSON(raw); err != nil || string(normalized) != "{}" {
			t.Fatalf("NormalizeArgumentsJSON(%q) = %q, %v; want {}", raw, normalized, err)
		}
	}
}

func TestNormalizeArgumentsJSON_RejectsDuplicateObjectKeys(t *testing.T) {
	tests := map[string]string{
		"top-level duplicate":      `{"path":"a.go","path":"b.go"}`,
		"nested duplicate":         `{"opts":{"line":7,"line":8}}`,
		"duplicate in array":       `{"items":[{"a":1,"a":2}]}`,
		"deeply nested duplicate":  `{"a":{"b":{"c":true,"c":false}}}`,
		"encoded duplicate":        `"{\"a\":1,\"a\":2}"`,
		"fenced duplicate":         "```json\n{\"a\":1,\"a\":2}\n```",
		"encoded fenced duplicate": "```json\n\"{\\\"a\\\":1,\\\"a\\\":2}\"\n```",
		"escaped duplicate":        `{"a":1,"\u0061":2}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := NormalizeArgumentsJSON(raw)
			if err == nil {
				t.Fatalf("NormalizeArgumentsJSON(%q) = %q, want duplicate-key error", raw, got)
			}
			if got, err := DecodeArguments(raw); err == nil {
				t.Fatalf("DecodeArguments(%q) = %#v, want duplicate-key error", raw, got)
			}
		})
	}
}

func TestNormalizeArgumentsJSON_PreservesValidNestedObjects(t *testing.T) {
	for name, raw := range map[string]string{
		"distinct keys": `{"path":"a.go","opts":{"line":7,"force":true},"tags":[{"k":"v"}]}`,
		"sibling keys":  `{"items":[{"key":"first"},{"key":"second"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := NormalizeArgumentsJSON(raw)
			if err != nil {
				t.Fatalf("NormalizeArgumentsJSON() error = %v", err)
			}
			if string(got) != raw {
				t.Fatalf("NormalizeArgumentsJSON() = %q, want %q", got, raw)
			}
		})
	}
}

func TestDecodeArguments_RejectsAmbiguousOrMalformedInput(t *testing.T) {
	tests := map[string]string{
		"surrounding prose":  `use {"path":"a.go"}`,
		"trailing comma":     `{"path":"a.go",}`,
		"array":              `[{"path":"a.go"}]`,
		"scalar":             `7`,
		"encoded scalar":     `"7"`,
		"unterminated fence": "```json\n{\"path\":\"a.go\"}",
		"extra after fence":  "```json\n{\"path\":\"a.go\"}\n```\nmore",
		"other fence":        "```yaml\npath: a.go\n```",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if got, err := DecodeArguments(raw); err == nil {
				t.Fatalf("DecodeArguments() = %#v, want error", got)
			}
		})
	}
}
