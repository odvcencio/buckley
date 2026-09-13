package commands

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCommitDefinitionScalarBodyValidationAndCanonicalOutput(t *testing.T) {
	definition := CommitDefinition{}
	raw := json.RawMessage(`{"action":"fix","subject":"retain useful output","body":"- First change\n\n* Second change","issues":"12"}`)
	if err := definition.Validate(raw); err != nil {
		t.Fatalf("Validate rejected scalar list fields: %v", err)
	}
	value, err := definition.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var canonical struct {
		Body   []string `json:"body"`
		Issues []string `json:"issues"`
	}
	if err := json.Unmarshal(encoded, &canonical); err != nil {
		t.Fatalf("output does not use canonical arrays: %s: %v", encoded, err)
	}
	if !reflect.DeepEqual(canonical.Body, []string{"First change", "Second change"}) ||
		!reflect.DeepEqual(canonical.Issues, []string{"12"}) {
		t.Fatalf("canonical output = %s", encoded)
	}
}

func TestCommitDefinitionTolerantListsRetainValidation(t *testing.T) {
	for _, raw := range []string{
		`{"action":"fix","subject":"retain validation","body":42}`,
		`{"action":"fix","subject":"retain validation","body":true}`,
		`{"action":"fix","subject":"retain validation","body":{"text":"wrong"}}`,
		`{"action":"fix","subject":"retain validation","body":["valid",42]}`,
		`{"action":"fix","subject":"retain validation","body":" - \n * "}`,
		`{"action":"ship","subject":"retain validation","body":"valid"}`,
		`{"action":"fix","subject":"retain validation","body":"valid","issues":"Closes #12"}`,
		`{"action":"fix","subject":"retain validation","body":"valid","issues":{"id":12}}`,
	} {
		if err := (CommitDefinition{}).Validate(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted malformed result: %s", raw)
		}
	}
}
