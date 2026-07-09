package pr

import (
	"reflect"
	"testing"
)

func TestDecodePRResultStrict(t *testing.T) {
	raw := []byte(`{"title":"t","summary":"s","changes":["a","b"],"testing":["x"],"breaking":true,"issues":["12"],"reviewers_hint":"h"}`)

	pr, err := decodePRResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pr.Title != "t" || pr.Summary != "s" || pr.ReviewersHint != "h" {
		t.Errorf("scalar fields wrong: %+v", pr)
	}
	if !reflect.DeepEqual(pr.Changes, []string{"a", "b"}) {
		t.Errorf("Changes = %v, want [a b]", pr.Changes)
	}
	if !reflect.DeepEqual(pr.Testing, []string{"x"}) {
		t.Errorf("Testing = %v, want [x]", pr.Testing)
	}
	if !pr.Breaking {
		t.Errorf("Breaking = false, want true")
	}
	if !reflect.DeepEqual(pr.Issues, []string{"12"}) {
		t.Errorf("Issues = %v, want [12]", pr.Issues)
	}
}

func TestDecodePRResultIssuesAsString(t *testing.T) {
	// glm-4.6 failure mode: a bare string where the schema wants an array.
	raw := []byte(`{"title":"t","summary":"s","changes":["a"],"testing":["x"],"issues":"42"}`)

	pr, err := decodePRResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(pr.Issues, []string{"42"}) {
		t.Errorf("Issues = %v, want [42]", pr.Issues)
	}
}

func TestDecodePRResultChangesAndTestingAsString(t *testing.T) {
	raw := []byte(`{"title":"t","summary":"s","changes":"only one change","testing":"go test ./..."}`)

	pr, err := decodePRResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(pr.Changes, []string{"only one change"}) {
		t.Errorf("Changes = %v, want [only one change]", pr.Changes)
	}
	if !reflect.DeepEqual(pr.Testing, []string{"go test ./..."}) {
		t.Errorf("Testing = %v, want [go test ./...]", pr.Testing)
	}
}

func TestDecodePRResultBreakingAsString(t *testing.T) {
	raw := []byte(`{"title":"t","summary":"s","changes":["a"],"testing":["x"],"breaking":"true"}`)

	pr, err := decodePRResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pr.Breaking {
		t.Errorf("Breaking = false, want true (from string \"true\")")
	}
}

func TestDecodePRResultArrayWithNonStringElements(t *testing.T) {
	// Models sometimes emit a number or object inside a string array.
	raw := []byte(`{"title":"t","summary":"s","changes":["real change", 42],"testing":["x"]}`)

	pr, err := decodePRResult(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(pr.Changes, []string{"real change", "42"}) {
		t.Errorf("Changes = %v, want [real change 42]", pr.Changes)
	}
}

func TestDecodePRResultSyntacticallyInvalidReturnsError(t *testing.T) {
	// A number with an embedded space is not recoverable by coercion; the
	// caller must re-invoke. decodePRResult must surface the error, not panic.
	raw := []byte(`{"title":"t","summary":"s","changes":["a"],"testing":["x"],"count":1 234}`)

	if _, err := decodePRResult(raw); err == nil {
		t.Errorf("expected error for syntactically invalid JSON, got nil")
	}
}
