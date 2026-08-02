package commands

import (
	"encoding/json"
	"testing"
)

// TestStringList_AcceptsArrayAndString locks the tolerant unmarshal: the
// documented array shape parses as-is, and a prose string (the observed
// model deviation) becomes trimmed, bullet-free lines instead of failing
// the whole generation.
func TestStringList_AcceptsArrayAndString(t *testing.T) {
	t.Parallel()

	var fromArray PRResult
	if err := json.Unmarshal([]byte(`{"title":"t","summary":"s","changes":["one","two"]}`), &fromArray); err != nil {
		t.Fatalf("array form: %v", err)
	}
	if len(fromArray.Changes) != 2 || fromArray.Changes[0] != "one" {
		t.Fatalf("array form parsed as %v", fromArray.Changes)
	}

	var fromString PRResult
	raw := `{"title":"t","summary":"s","changes":"- Added the widget\n* Fixed the frobber\n\n"}`
	if err := json.Unmarshal([]byte(raw), &fromString); err != nil {
		t.Fatalf("string form: %v", err)
	}
	if len(fromString.Changes) != 2 || fromString.Changes[0] != "Added the widget" || fromString.Changes[1] != "Fixed the frobber" {
		t.Fatalf("string form parsed as %v", fromString.Changes)
	}

	out, err := json.Marshal(fromString.Changes)
	if err != nil || string(out) != `["Added the widget","Fixed the frobber"]` {
		t.Fatalf("marshal = %s, %v; want a plain JSON array", out, err)
	}

	var bad PRResult
	if err := json.Unmarshal([]byte(`{"title":"t","summary":"s","changes":42}`), &bad); err == nil {
		t.Fatal("numeric changes did not fail")
	}
}
