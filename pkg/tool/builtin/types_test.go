package builtin

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParameterSchemaNeverEmitsNull guards the Moonshot/Kimi 400 regression: a
// tool that leaves Required/Properties unset must still serialize them as a
// valid array/object, never null, or strict providers reject the whole request
// ("required must be an array") and OpenRouter silently falls back to a
// different model.
func TestParameterSchemaNeverEmitsNull(t *testing.T) {
	data, err := json.Marshal(ParameterSchema{Type: "object"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(data)
	if strings.Contains(got, `"required":null`) || !strings.Contains(got, `"required":[]`) {
		t.Fatalf("required must serialize as [], got %s", got)
	}
	if strings.Contains(got, `"properties":null`) || !strings.Contains(got, `"properties":{}`) {
		t.Fatalf("properties must serialize as {}, got %s", got)
	}

	// An empty schema value should still get a type.
	if !strings.Contains(string(mustMarshal(t, ParameterSchema{})), `"type":"object"`) {
		t.Fatalf("empty schema should default type to object")
	}

	// A populated schema round-trips faithfully.
	full := ParameterSchema{
		Type:       "object",
		Required:   []string{"path"},
		Properties: map[string]PropertySchema{"path": {Type: "string"}},
	}
	if s := string(mustMarshal(t, full)); !strings.Contains(s, `"required":["path"]`) {
		t.Fatalf("expected required:[path], got %s", s)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
