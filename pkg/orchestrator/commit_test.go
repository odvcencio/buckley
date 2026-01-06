package orchestrator

import (
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
)

func TestExtractJSON_CodeBlockPriority(t *testing.T) {
	input := "Response:\n```json\n{\n  \"type\": \"feat\",\n  \"subject\": \"add preview panel\"\n}\n```\n\nFallback {\"type\":\"fix\",\"subject\":\"fallback\"}"
	want := "{\n  \"type\": \"feat\",\n  \"subject\": \"add preview panel\"\n}"

	got := extractJSON(input)
	if got != want {
		t.Fatalf("expected code block JSON to be preferred, got %q", got)
	}
}

func TestExtractJSON_RawFallback(t *testing.T) {
	input := "Summary without code block {\"type\":\"chore\",\"subject\":\"tidy deps\"} trailing text"
	want := "{\"type\":\"chore\",\"subject\":\"tidy deps\"}"

	got := extractJSON(input)
	if got != want {
		t.Fatalf("expected raw JSON fallback, got %q", got)
	}
}

func TestCommitGeneratorParseCommit_JSON(t *testing.T) {
	cg := &CommitGenerator{}
	content := "Here is the commit:\n```json\n{\n  \"type\": \"feat\",\n  \"scope\": \"cli\",\n  \"subject\": \"add offline mode\",\n  \"body\": \"Introduce offline plan execution with local cache.\",\n  \"breaking\": true,\n  \"issues\": [\"123\", \"456\"]\n}\n```"

	commit, err := cg.parseCommitMessage(content)
	if err != nil {
		t.Fatalf("parseCommitMessage returned error: %v", err)
	}

	if commit.Type != "add" {
		t.Fatalf("expected type add, got %s", commit.Type)
	}
	if commit.Scope != "cli" {
		t.Fatalf("expected scope cli, got %s", commit.Scope)
	}
	if commit.Subject != "add offline mode" {
		t.Fatalf("expected subject 'add offline mode', got %s", commit.Subject)
	}
	if !strings.Contains(commit.Body, "offline plan execution") {
		t.Fatalf("expected body to include explanation, got %q", commit.Body)
	}
	if !commit.Breaking {
		t.Fatal("expected breaking change flag to be true")
	}
	if len(commit.Issues) != 2 || commit.Issues[0] != "123" || commit.Issues[1] != "456" {
		t.Fatalf("expected issues [123 456], got %v", commit.Issues)
	}
}

func TestCommitGeneratorParseCommit_Fallback(t *testing.T) {
	cg := &CommitGenerator{}
	content := "test(ui): refresh prompt\n\nImprove prompt rendering for long-running workflows.\n\nBREAKING CHANGE: prompt protocols changed\nCloses #42\nFixes #104"

	commit, err := cg.parseCommitMessage(content)
	if err != nil {
		t.Fatalf("parseCommitMessage returned error: %v", err)
	}

	if commit.Type != "test" || commit.Scope != "ui" {
		t.Fatalf("expected type test and scope ui, got type=%s scope=%s", commit.Type, commit.Scope)
	}
	if commit.Subject != "refresh prompt" {
		t.Fatalf("unexpected subject %q", commit.Subject)
	}
	if !strings.Contains(commit.Body, "Improve prompt rendering") {
		t.Fatalf("expected body to include summary, got %q", commit.Body)
	}
	if !commit.Breaking {
		t.Fatal("expected breaking change to be detected")
	}
	if len(commit.Issues) != 2 {
		t.Fatalf("expected two issues, got %v", commit.Issues)
	}
	if commit.Issues[0] != "42" || commit.Issues[1] != "104" {
		t.Fatalf("unexpected issues slice %v", commit.Issues)
	}
}

func TestCommitGeneratorParseCommit_Defaults(t *testing.T) {
	cg := &CommitGenerator{}

	commit, err := cg.parseCommitMessage("nonsense content without commit syntax")
	if err != nil {
		t.Fatalf("parseCommitMessage returned error: %v", err)
	}

	if commit.Type != "update" {
		t.Fatalf("expected default type update, got %s", commit.Type)
	}
	if commit.Subject != "staged changes" {
		t.Fatalf("expected default subject 'staged changes', got %s", commit.Subject)
	}
}

func TestCommitGeneratorFormatCommitMessage(t *testing.T) {
	cg := &CommitGenerator{}
	commit := &CommitInfo{
		Type:     "add",
		Scope:    "cli",
		Subject:  "workflow summary",
		Body:     "Provide a human-readable summary for each orchestrated plan.",
		Breaking: true,
		Issues:   []string{"88", "102"},
	}

	msg := cg.FormatCommitMessage(commit)

	if !strings.HasPrefix(msg, "add(cli): workflow summary\n") {
		t.Fatalf("unexpected first line: %q", msg)
	}
	if !strings.Contains(msg, "\n\nProvide a human-readable summary") {
		t.Fatalf("expected body paragraph, got %q", msg)
	}
	if !strings.Contains(msg, "BREAKING CHANGE: Provide a human-readable summary") {
		t.Fatalf("expected breaking change footer, got %q", msg)
	}
	if !strings.Contains(msg, "Closes #88") || !strings.Contains(msg, "Closes #102") {
		t.Fatalf("expected issue references, got %q", msg)
	}
}

func TestCommitGenerator_getUtilityModel(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		expected string
	}{
		{
			name:     "nil config uses default",
			cfg:      nil,
			expected: config.DefaultUtilityModel,
		},
		{
			name:     "default config uses default utility model",
			cfg:      config.DefaultConfig(),
			expected: config.DefaultUtilityModel,
		},
		{
			name: "custom commit model",
			cfg: func() *config.Config {
				c := config.DefaultConfig()
				c.Models.Utility.Commit = "anthropic/claude-3-haiku"
				return c
			}(),
			expected: "anthropic/claude-3-haiku",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cg := NewCommitGenerator(nil, tt.cfg)
			got := cg.getUtilityModel()
			if got != tt.expected {
				t.Errorf("getUtilityModel() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestNewCommitGenerator(t *testing.T) {
	cfg := config.DefaultConfig()
	cg := NewCommitGenerator(nil, cfg)
	if cg == nil {
		t.Fatal("expected non-nil CommitGenerator")
	}
	if cg.cfg != cfg {
		t.Error("expected config to be set")
	}
}
