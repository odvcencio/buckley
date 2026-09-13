package commit

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCommitResultBodyWireCompatibility(t *testing.T) {
	for _, tc := range []struct {
		body string
		want []string
	}{
		{`["first", "second"]`, []string{"first", "second"}},
		{`"first\nsecond"`, []string{"first\nsecond"}},
		{`""`, []string{""}},
		{`null`, nil},
	} {
		var result CommitResult
		if err := json.Unmarshal([]byte(`{"action":"fix","subject":"retain body","body":`+tc.body+`}`), &result); err != nil {
			t.Fatal(err)
		}
		if result.Action != "fix" || result.Subject != "retain body" || !reflect.DeepEqual(result.Body, tc.want) {
			t.Fatalf("decoded result = %+v, want body %#v", result, tc.want)
		}
		wire, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if result.Body != nil && !strings.Contains(string(wire), `"body":[`) {
			t.Fatalf("noncanonical body output: %s", wire)
		}
	}
}

func TestCommitResultRejectsMalformedWireFields(t *testing.T) {
	for _, wire := range []string{
		`{"body":42}`, `{"body":true}`, `{"body":{"text":"oops"}}`,
		`{"body":["first",42]}`, `{"action":false,"body":"valid"}`,
	} {
		result := CommitResult{Action: "original", Body: []string{"preserved"}}
		if err := json.Unmarshal([]byte(wire), &result); err == nil {
			t.Fatalf("accepted malformed result: %s", wire)
		}
		if result.Action != "original" || !reflect.DeepEqual(result.Body, []string{"preserved"}) {
			t.Fatal("failed decode mutated caller")
		}
	}
}
