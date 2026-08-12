package main

import (
	"strings"
	"testing"
	"time"
)

func TestAppendReviewExecutionPlanUsesCompactQwenProfile(t *testing.T) {
	prompt := appendReviewExecutionPlan("review this", automatedReviewOptions{
		sizeClass:            "focused",
		modelID:              "qwen/qwen3.7-plus",
		reasoningEffort:      "low",
		reasoningMaxTokens:   2048,
		maxIterations:        2,
		maxToolCalls:         4,
		maxVerificationCalls: 1,
		explorationTimeout:   100 * time.Second,
		synthesisLead:        85 * time.Second,
	})
	for _, want := range []string{
		"## Qwen Review Profile",
		"Thinking budget: 2048 tokens",
		"Read deterministic evidence before summarizing the diff",
		"harness-collected verification evidence first",
		"provider-labeled violations as demonstrated defects",
		"event input -> checkout ref -> validated commit -> built bytes -> published identifier",
		"Never assume checkout rewrites event variables",
		"rank at most three concrete changed-behavior failures",
		"return only the final review",
		"exactly once with one ## Grade: heading",
		"Never list the same ID in both",
	} {
		if !strings.Contains(strings.ToLower(prompt), strings.ToLower(want)) {
			t.Fatalf("Qwen profile missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "ASD-STE100") {
		t.Fatalf("Qwen profile inherited verbose generic policy:\n%s", prompt)
	}
	if strings.Contains(strings.ToLower(prompt), "put every required verification target") {
		t.Fatalf("Qwen profile asks the model to duplicate host verification:\n%s", prompt)
	}
	if len(prompt) > 2500 {
		t.Fatalf("Qwen profile = %d bytes, want compact profile", len(prompt))
	}
}

func TestReviewContextProvidersForModelAddsRiskSignalsOnlyForQwen(t *testing.T) {
	qwen := reviewContextProvidersForModel("qwen/qwen3.7-plus")
	if len(qwen) != 2 {
		t.Fatalf("Qwen providers = %d, want 2", len(qwen))
	}
	qwenNames := []string{qwen[0].Name(), qwen[1].Name()}
	if !containsReviewProvider(qwenNames, "hyphae") || !containsReviewProvider(qwenNames, "workflow-risk-signals") {
		t.Fatalf("Qwen providers = %v", qwenNames)
	}

	other := reviewContextProvidersForModel("openai/gpt-5.4")
	if len(other) != 1 || other[0].Name() != "hyphae" {
		t.Fatalf("other providers = %v, want hyphae only", []string{other[0].Name()})
	}
}

func containsReviewProvider(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
