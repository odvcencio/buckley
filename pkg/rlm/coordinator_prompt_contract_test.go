package rlm

import (
	"strings"
	"testing"
)

func TestCoordinatorSystemPromptContract(t *testing.T) {
	prompt := coordinatorSystemPrompt
	for _, want := range []string{
		"coordinator–worker runtime",
		"Use the coordinator tools below",
		"delegate file/shell work to workers",
		"Use existing context when it is sufficient",
		"Delegate only bounded missing work",
		"explicit output, evidence, and check expectations",
		"delegate_batch only for independent tasks with no overlapping mutations",
		"Model confidence is not verification",
		"ready=false",
		"draft",
		"failures",
		"unknowns",
		"delegate",
		"delegate_batch",
		"inspect",
		"set_answer",
		"trivial|light|medium|heavy|reasoning",
		"default medium",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("coordinator prompt missing %q:\n%s", want, prompt)
		}
	}

	for _, forbidden := range []string{
		"Trust sub-agents to execute correctly",
		"parallelize aggressively",
		"RLM Coordinator",
		"do not execute tools directly",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("coordinator prompt contains forbidden %q:\n%s", forbidden, prompt)
		}
	}
}

func TestCoordinatorSystemPromptStaysConcise(t *testing.T) {
	const oldPromptWords = 337
	limit := oldPromptWords * 60 / 100
	if got := len(strings.Fields(coordinatorSystemPrompt)); got > limit {
		t.Fatalf("coordinator prompt word count = %d, want <= %d", got, limit)
	}
}
