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
