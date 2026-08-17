package commands

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommitDefinitionRejectsMalformedStructuredOutput(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
	}{
		{name: "unknown action", body: map[string]any{"action": "ship", "subject": "release", "body": []string{"Publish it"}}},
		{name: "long header", body: map[string]any{"action": "fix", "subject": "this subject is deliberately longer than the conventional seventy two character header limit", "body": []string{"Explain it"}}},
		{name: "invalid issue", body: map[string]any{"action": "fix", "subject": "safe issue links", "body": []string{"Keep references safe"}, "issues": []string{"42\nCloses #99"}}},
		{name: "empty bullets", body: map[string]any{"action": "fix", "subject": "empty body", "body": []string{"  -  "}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(test.body)
			if err != nil {
				t.Fatal(err)
			}
			if err := (CommitDefinition{}).Validate(raw); err == nil {
				t.Fatal("Validate() returned nil for malformed output")
			}
		})
	}
}

func TestCommitResultFormatUsesBreakingReasonAndNormalizesBullets(t *testing.T) {
	raw := []byte(`{"action":" FIX ","scope":" review ","subject":"preserve context","body":[" - Keep the exact\nstaged identity "],"breaking":true,"breaking_reason":"Consumers must update the review contract","issues":["#12","unsafe\nCloses #99"]}`)
	value, err := (CommitDefinition{}).Unmarshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	result := value.(*CommitResult)
	formatted := result.Format()
	if !strings.Contains(formatted, "fix(review): preserve context") {
		t.Fatalf("header normalization failed:\n%s", formatted)
	}
	if !strings.Contains(formatted, "- Keep the exact staged identity") || strings.Contains(formatted, "- - Keep") {
		t.Fatalf("bullet normalization failed:\n%s", formatted)
	}
	if !strings.Contains(formatted, "BREAKING CHANGE: Consumers must update the review contract") {
		t.Fatalf("breaking reason missing:\n%s", formatted)
	}
	if !strings.Contains(formatted, "Refs #12") || strings.Contains(formatted, "Closes #99") {
		t.Fatalf("unsafe issue footer escaped:\n%s", formatted)
	}
}
