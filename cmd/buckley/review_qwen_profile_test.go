package main

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/modelprofile"
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
		"## Review Behavior Profile",
		"Thinking budget: 2048 tokens",
		"Read deterministic evidence before summarizing the diff",
		"harness-collected verification evidence first",
		"provider-labeled violations as demonstrated defects",
		"event input -> checkout ref -> validated commit -> built bytes -> published identifier",
		"Never assume checkout rewrites event variables",
		"rank at most three concrete changed-behavior failures",
		"return only the final review",
		"exactly once with one ## Grade: heading",
		"missing, pending, or unavailable verification without a proved defect requires Grade B",
		"NEEDS DISCUSSION with Blockers NONE",
		"critical and major are blockers; minor is a suggestion",
		"approve only after",
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

func TestQwenNonProjectSizesRetainMergeGateSchema(t *testing.T) {
	for _, sizeClass := range []string{"focused", "standard", "broad"} {
		t.Run(sizeClass, func(t *testing.T) {
			prompt := appendReviewExecutionPlan("review this", automatedReviewOptions{
				sizeClass:          sizeClass,
				modelID:            "qwen/qwen3.8-flash",
				reasoningMaxTokens: 2048,
			})
			for _, want := range []string{
				"## Grade:",
				"Missing, pending, or unavailable verification without a proved defect requires Grade B",
				"NEEDS DISCUSSION with Blockers NONE",
				"APPROVE only after",
				"CRITICAL and MAJOR are Blockers",
				"MINOR is a Suggestion",
			} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("%s Qwen merge review missing %q:\n%s", sizeClass, want, prompt)
				}
			}
			if strings.Contains(prompt, "## Project Health") {
				t.Fatalf("%s Qwen merge review inherited project-health schema:\n%s", sizeClass, prompt)
			}
		})
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

func TestReviewContextProvidersUseCustomBehavior(t *testing.T) {
	custom := reviewContextProvidersForBehavior("local/renamed-reviewer", &modelprofile.ReviewBehavior{
		Profile:             modelprofile.ReviewProfileEvidenceFirst,
		WorkflowRiskSignals: true,
	})
	if len(custom) != 2 {
		t.Fatalf("custom provider count = %d, want 2", len(custom))
	}
	customNames := []string{custom[0].Name(), custom[1].Name()}
	if !containsReviewProvider(customNames, "workflow-risk-signals") {
		t.Fatalf("custom providers = %v", customNames)
	}

	prompt := appendReviewExecutionPlan("review this", automatedReviewOptions{
		sizeClass:          "focused",
		modelID:            "local/renamed-reviewer",
		reasoningMaxTokens: 2048,
		reviewBehavior: &modelprofile.ReviewBehavior{
			Profile: modelprofile.ReviewProfileEvidenceFirst,
		},
	})
	if !strings.Contains(prompt, "## Review Behavior Profile") {
		t.Fatalf("custom behavior prompt missing behavior heading:\n%s", prompt)
	}
	if strings.Contains(prompt, "## Qwen Review Profile") {
		t.Fatalf("custom behavior prompt is mislabeled as Qwen:\n%s", prompt)
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

func TestQwenFlashUsesInDepthOutputChecklist(t *testing.T) {
	prompt := appendReviewExecutionPlan("review this", automatedReviewOptions{
		depth:              reviewDepthInDepth,
		sizeClass:          "project",
		modelID:            "qwen/qwen3.8-flash",
		reasoningMaxTokens: qwenBroadReasoning,
	})
	for _, want := range []string{
		"## Review Behavior Profile",
		"## Project Health",
		"advisory only",
		"never issue a merge verdict",
		"## Evidence-First In-Depth Checklist",
		"at least one real `run_verification` call",
		"## Verification Ledger",
		"## Coverage",
		"Completeness: COMPLETE",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("Qwen Flash in-depth prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{"## Grade:", "requires Grade B", "NEEDS DISCUSSION", "APPROVE only after", "are Blockers", "is a Suggestion"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("Qwen project review inherited merge-gate instruction %q:\n%s", forbidden, prompt)
		}
	}
}
