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
		{name: "repeated action prefix", body: map[string]any{"action": "test", "subject": "test: replace unwritable fixture", "body": []string{"Explain it"}}},
		{name: "repeated scoped breaking prefix", body: map[string]any{"action": "add", "scope": "core", "subject": "fix!: reject duplicate header", "body": []string{"Explain it"}}},
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

func TestCommitDefinitionValidationExplainsSubjectHeaderOwnership(t *testing.T) {
	raw := []byte(`{"action":"fix","scope":"ui","subject":"feat(scope): preserve ownership","body":["Explain the change"]}`)
	err := (CommitDefinition{}).Validate(raw)
	if err == nil {
		t.Fatal("Validate() returned nil for repeated structured prefix")
	}
	for _, want := range []string{"action and scope belong only in the structured header", "subject must not repeat"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestCommitDefinitionSystemPromptDisallowsRepeatedHeaderPrefix(t *testing.T) {
	prompt := (CommitDefinition{}).SystemPrompt()
	for _, want := range []string{"subject/summary", "do not repeat", "structured header"} {
		if !strings.Contains(strings.ToLower(prompt), want) {
			t.Fatalf("system prompt missing %q:\n%s", want, prompt)
		}
	}
	if description := (CommitDefinition{}).Tool().Parameters.Properties["subject"].Description; !strings.Contains(strings.ToLower(description), "do not repeat") {
		t.Fatalf("subject schema description missing duplicate-prefix guidance: %q", description)
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
