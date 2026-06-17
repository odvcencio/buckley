package agentspec

import (
	"strings"
	"testing"
)

func TestRuntimeProfileSubagentProfile(t *testing.T) {
	profile := &RuntimeProfile{
		SourcePath: "agent.yaml",
		Spec: &Spec{
			Name:    "daily",
			Persona: "lead",
			Instructions: InstructionSpec{
				Prompt: "Parent instructions.",
				Files:  []string{"AGENTS.md"},
			},
			Models: ModelSpec{
				Chat:      "parent-chat",
				Execution: "parent-exec",
				Review:    "parent-review",
			},
			Skills: []string{"base"},
			Tools:  ToolSpec{Tier: "standard", Allow: []string{"read_file"}},
			Policies: PolicySpec{
				ApprovalMode: "safe",
				Domains:      []string{"approval"},
			},
			Subagents: []SubagentSpec{{
				Name:         "reviewer",
				Persona:      "strict-reviewer",
				Model:        "review-model",
				ToolTier:     "read_only",
				Skills:       []string{"review"},
				Instructions: "Look for regressions.",
				Policies: PolicySpec{
					ApprovalMode: "ask",
					MaxToolCalls: 4,
					Domains:      []string{"risk"},
				},
			}},
		},
		InstructionFiles: []InstructionFileContent{{Path: "AGENTS.md", Content: "repo rules"}},
	}

	sub, err := profile.SubagentProfile("reviewer")
	if err != nil {
		t.Fatalf("SubagentProfile: %v", err)
	}
	if sub.Spec.Name != "daily/reviewer" {
		t.Fatalf("name=%q", sub.Spec.Name)
	}
	if sub.Spec.Persona != "strict-reviewer" {
		t.Fatalf("persona=%q", sub.Spec.Persona)
	}
	if sub.Spec.Models.Execution != "review-model" || sub.Spec.Models.Chat != "review-model" || sub.Spec.Models.Review != "parent-review" {
		t.Fatalf("models=%+v", sub.Spec.Models)
	}
	if sub.Spec.Tools.Tier != "read_only" {
		t.Fatalf("tool tier=%q", sub.Spec.Tools.Tier)
	}
	if !contains(sub.Spec.Skills, "base") || !contains(sub.Spec.Skills, "review") {
		t.Fatalf("skills=%v", sub.Spec.Skills)
	}
	if sub.Spec.Policies.ApprovalMode != "ask" || sub.Spec.Policies.MaxToolCalls != 4 {
		t.Fatalf("policies=%+v", sub.Spec.Policies)
	}
	if !contains(sub.Spec.Policies.Domains, "approval") || !contains(sub.Spec.Policies.Domains, "risk") {
		t.Fatalf("domains=%v", sub.Spec.Policies.Domains)
	}
	if len(sub.Spec.Subagents) != 0 {
		t.Fatalf("subagents should not be nested: %+v", sub.Spec.Subagents)
	}
	if !strings.Contains(sub.PromptSection(), "Look for regressions.") {
		t.Fatalf("prompt missing subagent instructions:\n%s", sub.PromptSection())
	}
	if profile.Spec.Persona != "lead" || profile.Spec.Models.Execution != "parent-exec" {
		t.Fatalf("parent profile mutated: %+v", profile.Spec)
	}
}

func TestRuntimeProfileSubagentProfileMissing(t *testing.T) {
	profile := &RuntimeProfile{Spec: &Spec{Name: "daily", Subagents: []SubagentSpec{{Name: "coder"}}}}
	_, err := profile.SubagentProfile("reviewer")
	if err == nil || !strings.Contains(err.Error(), "available: coder") {
		t.Fatalf("err=%v", err)
	}
}

func TestSubagentNamesSorted(t *testing.T) {
	names := SubagentNames(&Spec{Subagents: []SubagentSpec{{Name: "reviewer"}, {Name: "coder"}}})
	if strings.Join(names, ",") != "coder,reviewer" {
		t.Fatalf("names=%v", names)
	}
}
