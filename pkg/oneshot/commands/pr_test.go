package commands

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPRDefinitionAcceptsAndNormalizesStringChanges(t *testing.T) {
	raw := json.RawMessage(`{
		"title":"fix: keep PR generation useful",
		"summary":"Accept the provider shape observed in dogfood.",
		"changes":"- Accept a single string\n  without losing wrapped detail\n- Preserve normal array output",
		"testing":["go test ./pkg/oneshot/commands"]
	}`)

	def := PRDefinition{}
	if err := def.Validate(raw); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	value, err := def.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got := value.(*PRResult).Changes
	want := []string{
		"Accept a single string without losing wrapped detail",
		"Preserve normal array output",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
}

func TestPRDefinitionStringChangesNormalization(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "single", raw: "One focused change", want: []string{"One focused change"}},
		{name: "single bullet", raw: "- One focused change", want: []string{"One focused change"}},
		{name: "newline list", raw: "First change\nSecond change", want: []string{"First change", "Second change"}},
		{name: "numbered bullets", raw: "1. First change\n2) Second change", want: []string{"First change", "Second change"}},
		{name: "empty", raw: "  \n ", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizePRChanges(tt.raw); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("normalizePRChanges(%q) = %#v, want %#v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestPRDefinitionPreservesArrayChanges(t *testing.T) {
	raw := json.RawMessage(`{"title":"fix","summary":"summary","changes":["First","Second"],"testing":["test"]}`)
	value, err := (PRDefinition{}).Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := []string{"First", "Second"}
	if got := value.(*PRResult).Changes; !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
}

func TestPRDefinitionKeepsOtherFieldsStrict(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantReason string
	}{
		{
			name:       "testing remains array",
			body:       `{"title":"fix","summary":"summary","changes":"change","testing":"go test ./..."}`,
			wantReason: "cannot unmarshal string",
		},
		{
			name:       "changes rejects object",
			body:       `{"title":"fix","summary":"summary","changes":{"item":"change"},"testing":["test"]}`,
			wantReason: "changes: must be a string or array of strings",
		},
		{
			name:       "changes rejects mixed array",
			body:       `{"title":"fix","summary":"summary","changes":["change",3],"testing":["test"]}`,
			wantReason: "cannot unmarshal number",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (PRDefinition{}).Validate(json.RawMessage(tt.body))
			if err == nil || !strings.Contains(err.Error(), tt.wantReason) {
				t.Fatalf("error = %v, want %q", err, tt.wantReason)
			}
		})
	}
}

func TestPRDefinitionRejectsEmptyStringChanges(t *testing.T) {
	raw := json.RawMessage(`{"title":"fix","summary":"summary","changes":"  ","testing":["test"]}`)
	err := (PRDefinition{}).Validate(raw)
	if err == nil || !strings.Contains(err.Error(), "at least one change is required") {
		t.Fatalf("error = %v, want missing change validation", err)
	}
}
