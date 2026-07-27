package main

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/rules"
)

type reviewReasoningChecker struct {
	supported bool
}

func TestDefaultReviewTimeoutStaysBelowFiveMinutes(t *testing.T) {
	if defaultReviewTimeout >= 5*time.Minute {
		t.Fatalf("default review timeout = %s, want less than five minutes", defaultReviewTimeout)
	}
}

func (c reviewReasoningChecker) SupportsReasoning(string) bool {
	return c.supported
}

func TestResolveReviewReasoningEffortUsesBuckbotOverride(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Buckbot.Reasoning = "medium"
	cfg.Models.Reasoning = "xhigh"

	got := resolveReviewReasoningEffort(cfg, reviewReasoningChecker{supported: true}, "qwen/qwen3.7-plus", "")
	if got != "medium" {
		t.Fatalf("reasoning effort = %q, want medium", got)
	}
}

func TestResolveReviewReasoningEffortCanInheritGlobalSetting(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Buckbot.Reasoning = "auto"
	cfg.Models.Reasoning = "xhigh"

	got := resolveReviewReasoningEffort(cfg, reviewReasoningChecker{supported: true}, "qwen/qwen3.7-plus", "")
	if got != "xhigh" {
		t.Fatalf("reasoning effort = %q, want xhigh", got)
	}
}

func TestResolveReviewReasoningEffortRequiresModelSupport(t *testing.T) {
	cfg := config.DefaultConfig()
	got := resolveReviewReasoningEffort(cfg, reviewReasoningChecker{}, "plain-model", "")
	if got != "" {
		t.Fatalf("reasoning effort = %q, want empty", got)
	}
}

func TestResolveReviewReasoningEffortUsesExplicitSuffix(t *testing.T) {
	cfg := config.DefaultConfig()
	got := resolveReviewReasoningEffort(cfg, reviewReasoningChecker{supported: true}, "qwen/qwen3.7-plus", "medium")
	if got != "medium" {
		t.Fatalf("reasoning effort = %q, want medium", got)
	}
}

func TestResolveReviewExecutionPlanUsesGovernedSizeClasses(t *testing.T) {
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	focused := resolveReviewExecutionPlan(engine, rules.ReviewPlanFacts{
		FileCount: 3,
		DiffBytes: 8_000,
	})
	if focused.sizeClass != "focused" || focused.reasoningEffort != "low" ||
		focused.maxIterations != 6 || focused.maxToolCalls != 6 ||
		focused.verificationTimeout != 60*time.Second || focused.explorationTimeout != 90*time.Second ||
		focused.synthesisLead != 90*time.Second {
		t.Fatalf("focused plan = %#v", focused)
	}

	broad := resolveReviewExecutionPlan(engine, rules.ReviewPlanFacts{
		FileCount:         2,
		DiffBytes:         8_000,
		ContextIncomplete: true,
	})
	if broad.sizeClass != "broad" || broad.reasoningEffort != "medium" ||
		broad.maxIterations != 8 || broad.maxToolCalls != 8 ||
		broad.verificationTimeout != 90*time.Second || broad.explorationTimeout != 105*time.Second ||
		broad.synthesisLead != 2*time.Minute {
		t.Fatalf("broad plan = %#v", broad)
	}
}

func TestReviewExecutionPlanPreservesExplicitTurnOverride(t *testing.T) {
	opts := automatedReviewOptions{maxIterations: 5, adaptiveReasoning: true}.withExecutionPlan(reviewExecutionPlan{
		sizeClass:           "standard",
		reasoningEffort:     "medium",
		maxIterations:       11,
		maxToolCalls:        18,
		verificationTimeout: 2 * time.Minute,
		explorationTimeout:  4 * time.Minute,
		synthesisLead:       90 * time.Second,
	})
	if opts.maxIterations != 5 {
		t.Fatalf("maxIterations = %d, want explicit override 5", opts.maxIterations)
	}
	if opts.maxToolCalls != 18 || opts.verificationTimeout != 2*time.Minute ||
		opts.explorationTimeout != 4*time.Minute || opts.synthesisLead != 90*time.Second ||
		opts.reasoningEffort != "medium" {
		t.Fatalf("execution plan was not applied: %#v", opts)
	}
}

func TestReviewExecutionPlanScalesAdaptiveCodexModels(t *testing.T) {
	tests := []struct {
		size string
		want string
	}{
		{size: "focused", want: codexReviewModelFocused},
		{size: "standard", want: codexReviewModelStandard},
		{size: "broad", want: codexReviewModelBroad},
		{size: "project", want: codexReviewModelBroad},
	}
	for _, tt := range tests {
		opts := automatedReviewOptions{
			modelID:            codexReviewModelStandard,
			adaptiveCodexModel: true,
			adaptiveReasoning:  true,
		}.withExecutionPlan(reviewExecutionPlan{sizeClass: tt.size})
		if opts.modelID != tt.want {
			t.Fatalf("%s model = %q, want %q", tt.size, opts.modelID, tt.want)
		}
		wantReasoning := "medium"
		if tt.size == "focused" {
			wantReasoning = "xhigh"
		}
		if opts.reasoningEffort != wantReasoning {
			t.Fatalf("%s reasoning = %q, want %q", tt.size, opts.reasoningEffort, wantReasoning)
		}
	}
}

func TestReviewExecutionPlanPreservesExactModel(t *testing.T) {
	opts := automatedReviewOptions{
		modelID:            "codex/gpt-5.6-terra",
		adaptiveCodexModel: false,
	}.withExecutionPlan(reviewExecutionPlan{sizeClass: "broad"})
	if opts.modelID != "codex/gpt-5.6-terra" {
		t.Fatalf("model = %q, want exact Terra override", opts.modelID)
	}
}

func TestAppendReviewExecutionPlanGuidesBoundedEvidenceCollection(t *testing.T) {
	prompt := appendReviewExecutionPlan("review this", automatedReviewOptions{
		sizeClass:           "focused",
		modelID:             "codex/gpt-5.6-luna",
		reasoningEffort:     "low",
		maxIterations:       8,
		maxToolCalls:        12,
		verificationTimeout: 90 * time.Second,
		explorationTimeout:  3 * time.Minute,
		synthesisLead:       75 * time.Second,
	})
	for _, want := range []string{
		"Size class: FOCUSED",
		"Model: codex/gpt-5.6-luna",
		"Reasoning effort: LOW",
		"at most 8 model turns and 12 total",
		"Limit each verification command to 90 seconds",
		"Finish evidence collection within 180 seconds",
		"Keep the final 75 seconds",
		"Do not repeat equivalent searches, builds, or tests",
		"finish with a non-approval verdict",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestReviewExecutionPlanPreservesFixedReasoning(t *testing.T) {
	opts := automatedReviewOptions{
		reasoningEffort:   "medium",
		adaptiveReasoning: false,
	}.withExecutionPlan(reviewExecutionPlan{
		sizeClass:       "broad",
		reasoningEffort: "high",
	})
	if opts.reasoningEffort != "medium" {
		t.Fatalf("reasoning effort = %q, want fixed medium", opts.reasoningEffort)
	}
}
