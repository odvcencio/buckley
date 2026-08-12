package main

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/prompts"
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

func TestCriticModelReasoningSuffixStaysIndependent(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.Reasoning = "low"
	modelID, explicit := config.SplitReasoningSuffix("qwen/qwen3.7-plus-xhigh")

	got := resolveReviewReasoningEffort(cfg, reviewReasoningChecker{supported: true}, modelID, explicit)
	if modelID != "qwen/qwen3.7-plus" || got != "xhigh" {
		t.Fatalf("critic selection = %q/%q, want qwen/qwen3.7-plus/xhigh", modelID, got)
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
		focused.reasoningMaxTokens != 1024 ||
		focused.maxOutputTokens != projectReviewOutputTokenBudget ||
		focused.maxIterations != 4 || focused.maxToolCalls != 0 ||
		focused.maxVerificationCalls != 1 ||
		focused.verificationTimeout != 90*time.Second || focused.explorationTimeout != 45*time.Second ||
		focused.synthesisLead != 85*time.Second || focused.criticReserve != 70*time.Second ||
		focused.criticMaxIterations != 2 || focused.criticMaxToolCalls != 0 ||
		focused.criticExploration != 15*time.Second || focused.criticSynthesisLead != 45*time.Second {
		t.Fatalf("focused plan = %#v", focused)
	}

	broad := resolveReviewExecutionPlan(engine, rules.ReviewPlanFacts{
		FileCount:   3,
		DiffBytes:   8_000,
		BlastRadius: 1_127,
	})
	if broad.sizeClass != "broad" || broad.reasoningEffort != "medium" ||
		broad.reasoningMaxTokens != 2048 ||
		broad.maxOutputTokens != projectReviewOutputTokenBudget ||
		broad.maxIterations != 6 || broad.maxToolCalls != 0 ||
		broad.maxVerificationCalls != 1 ||
		broad.verificationTimeout != 3*time.Minute || broad.explorationTimeout != 55*time.Second ||
		broad.synthesisLead != 90*time.Second || broad.criticReserve != 80*time.Second ||
		broad.criticMaxIterations != 2 || broad.criticMaxToolCalls != 0 ||
		broad.criticExploration != 20*time.Second || broad.criticSynthesisLead != 50*time.Second {
		t.Fatalf("broad plan = %#v", broad)
	}
}

func TestReviewExecutionPlanPreservesExplicitTurnOverride(t *testing.T) {
	opts := automatedReviewOptions{maxIterations: 5, adaptiveReasoning: true}.withExecutionPlan(reviewExecutionPlan{
		sizeClass:            "standard",
		reasoningEffort:      "medium",
		reasoningMaxTokens:   4096,
		maxIterations:        11,
		maxToolCalls:         18,
		maxVerificationCalls: 1,
		verificationTimeout:  2 * time.Minute,
		explorationTimeout:   4 * time.Minute,
		synthesisLead:        90 * time.Second,
	})
	if opts.maxIterations != 5 {
		t.Fatalf("maxIterations = %d, want explicit override 5", opts.maxIterations)
	}
	if opts.maxToolCalls != 18 || opts.maxVerificationCalls != 1 || opts.verificationTimeout != 2*time.Minute ||
		opts.explorationTimeout != 4*time.Minute || opts.synthesisLead != 90*time.Second ||
		opts.reasoningEffort != "medium" || opts.reasoningMaxTokens != 4096 {
		t.Fatalf("execution plan was not applied: %#v", opts)
	}
}

func TestReviewExecutionPlanPreservesExplicitToolCallOverride(t *testing.T) {
	opts := automatedReviewOptions{maxToolCalls: 27}.withExecutionPlan(reviewExecutionPlan{
		sizeClass:       "standard",
		maxIterations:   6,
		maxToolCalls:    18,
		reasoningEffort: "medium",
	})
	if opts.maxToolCalls != 27 {
		t.Fatalf("maxToolCalls = %d, want explicit override 27", opts.maxToolCalls)
	}
}

func TestReviewVerificationTargetBudgetExpandsWithinToolLimit(t *testing.T) {
	opts := automatedReviewOptions{
		maxToolCalls:         3,
		maxVerificationCalls: 1,
	}.withVerificationTargetBudget([]string{
		"cmd/buckley/review.go",
		"pkg/oneshot/commands/review_context.go",
		"pkg/ui/widgets/chatview.go",
		"pkg/conversation/context_projection.go",
	})

	if opts.maxVerificationCalls != 3 {
		t.Fatalf("maxVerificationCalls = %d, want 3", opts.maxVerificationCalls)
	}
}

func TestReviewVerificationTargetBudgetRemainsUnlimitedWithUnlimitedTools(t *testing.T) {
	opts := automatedReviewOptions{
		maxToolCalls:         0,
		maxVerificationCalls: 1,
	}.withVerificationTargetBudget([]string{
		"cmd/buckley/review.go",
		"pkg/oneshot/commands/review_context.go",
		"pkg/ui/widgets/chatview.go",
		"pkg/conversation/context_projection.go",
	})

	if opts.maxVerificationCalls != 0 {
		t.Fatalf("maxVerificationCalls = %d, want no hidden verification ceiling", opts.maxVerificationCalls)
	}
}

func TestReviewExecutionPlanPreservesQwenAdaptiveTurns(t *testing.T) {
	plan := reviewExecutionPlan{
		sizeClass:            "broad",
		maxIterations:        6,
		maxToolCalls:         6,
		maxVerificationCalls: 1,
		explorationTimeout:   10 * time.Second,
	}
	bounded := automatedReviewOptions{
		modelID: "qwen/qwen3.7-plus",
	}.withExecutionPlan(plan)
	if bounded.maxIterations != 6 || bounded.maxToolCalls != 6 ||
		bounded.explorationTimeout != qwenReviewExploration ||
		bounded.criticExploration != qwenCriticExploration {
		t.Fatalf("Qwen plan = %#v, want governed turn budget and Qwen exploration windows", bounded)
	}

	explicit := automatedReviewOptions{
		modelID:       "qwen/qwen3.7-plus",
		maxIterations: 5,
	}.withExecutionPlan(plan)
	if explicit.maxIterations != 5 {
		t.Fatalf("explicit Qwen turns = %d, want 5", explicit.maxIterations)
	}

	other := automatedReviewOptions{
		modelID: "other/reviewer",
	}.withExecutionPlan(plan)
	if other.maxIterations != 6 {
		t.Fatalf("other model turns = %d, want 6", other.maxIterations)
	}
}

func TestReviewExecutionPlanRecognizesQwen38MaxProfile(t *testing.T) {
	options := automatedReviewOptions{
		modelID:           "qwen/qwen3.8-max",
		adaptiveReasoning: true,
	}.withExecutionPlan(reviewExecutionPlan{
		sizeClass:          "project",
		reasoningEffort:    "high",
		reasoningMaxTokens: 1024,
		maxOutputTokens:    projectReviewOutputTokenBudget,
	})
	if options.reasoningMaxTokens != qwenBroadReasoning {
		t.Fatalf("Qwen3.8 Max reasoning budget = %d, want %d", options.reasoningMaxTokens, qwenBroadReasoning)
	}
	if options.maxOutputTokens != projectReviewOutputTokenBudget {
		t.Fatalf("Qwen3.8 Max output budget = %d, want %d", options.maxOutputTokens, projectReviewOutputTokenBudget)
	}
	prompt := appendReviewExecutionPlan("review this", options)
	if !strings.Contains(prompt, "Final response budget: 32768 completion tokens") {
		t.Fatalf("Qwen3.8 prompt omitted final response budget:\n%s", prompt)
	}
}

func TestReviewExecutionPlanAdaptsQwenReasoningBudget(t *testing.T) {
	tests := []struct {
		size string
		want int
	}{
		{size: "focused", want: qwenFocusedReasoning},
		{size: "standard", want: qwenStandardReasoning},
		{size: "broad", want: qwenBroadReasoning},
		{size: "project", want: qwenBroadReasoning},
	}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			got := automatedReviewOptions{
				modelID:           "qwen/qwen3.7-plus",
				adaptiveReasoning: true,
			}.withExecutionPlan(reviewExecutionPlan{
				sizeClass:          tt.size,
				reasoningEffort:    "medium",
				reasoningMaxTokens: 1024,
			})
			if got.reasoningMaxTokens != tt.want {
				t.Fatalf("reasoning budget = %d, want %d", got.reasoningMaxTokens, tt.want)
			}
		})
	}
}

func TestReviewExecutionPlanMapsFixedQwenReasoningEffortToBudget(t *testing.T) {
	tests := []struct {
		effort string
		want   int
	}{
		{effort: "minimal", want: 512},
		{effort: "low", want: 1024},
		{effort: "medium", want: 2048},
		{effort: "high", want: 4096},
		{effort: "xhigh", want: 8192},
	}
	for _, tt := range tests {
		t.Run(tt.effort, func(t *testing.T) {
			got := automatedReviewOptions{
				modelID:           "qwen/qwen3.7-plus",
				reasoningEffort:   tt.effort,
				adaptiveReasoning: false,
			}.withExecutionPlan(reviewExecutionPlan{
				sizeClass:          "focused",
				reasoningEffort:    "low",
				reasoningMaxTokens: 1024,
			})
			if got.reasoningEffort != tt.effort || got.reasoningMaxTokens != tt.want {
				t.Fatalf(
					"fixed reasoning = %q/%d, want %q/%d",
					got.reasoningEffort,
					got.reasoningMaxTokens,
					tt.effort,
					tt.want,
				)
			}
		})
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
		reasoningMaxTokens:  2048,
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
		"Limit each model turn to 2048 reasoning tokens",
		"Use at most 8 model turns.",
		"Use at most 12 total inspection or verification calls.",
		"Limit each verification command to 90 seconds",
		"Finish evidence collection within 180 seconds",
		"Keep the final 75 seconds",
		`Return only the final review`,
		`Start the first line with "## Grade:"`,
		`In "## CI Status", write "- Build: STATE" and "- Tests: STATE"`,
		"Do not bold the Build or Tests labels",
		"When no feedback IDs exist, write exactly",
		"`NONE_SUPPLIED` — no prior feedback was supplied",
		"When feedback IDs exist, use `DISPOSITIONED` and copy every exact ID",
		"Omit a candidate finding when your own analysis disproves or withdraws it",
		"Omit future-hardening and style observations from Findings",
		"Copy every source identifier and registry key exactly",
		"Compare measurements only when their workload labels and settings match",
		"List MINOR findings as Suggestions, not Blockers",
		"Use REQUEST CHANGES only with a Blocker or proved current failure",
		"Pending, unknown, absent, or stale remote CI alone requires Grade B with NEEDS DISCUSSION",
		"Missing duplicate verification alone requires Grade B",
		"not Blockers or Findings",
		"Write the Falsification conclusion as one bare token",
		"Write Findings only when Falsification concludes PROVED",
		"Require a current failing input, violated invariant, failing check, or reproducible behavior",
		"Use Buckley's harness-collected verification evidence first",
		"Report an INCONCLUSIVE verification as UNAVAILABLE",
		"Move possible rename, regeneration, test drift, and private test-hook concerns to Remarks",
		"Do not expose analysis, repair commentary, progress text, or a plan",
		"Keep the final review concise enough to fit the output limit",
		"Do not repeat equivalent searches, builds, or tests",
		"finish with a non-approval verdict",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestAppendReviewExecutionPlanLeavesToolCallsUnlimitedByDefault(t *testing.T) {
	prompt := appendReviewExecutionPlan("review this", automatedReviewOptions{
		sizeClass:          "focused",
		modelID:            "codex/gpt-5.6-luna",
		reasoningEffort:    "low",
		reasoningMaxTokens: 1024,
		maxIterations:      4,
		maxToolCalls:       0,
	})
	if !strings.Contains(prompt, "There is no per-review tool-call cap") {
		t.Fatalf("prompt does not explain the default unlimited tool-call policy:\n%s", prompt)
	}
	if strings.Contains(prompt, "at most 0 total inspection") {
		t.Fatalf("prompt incorrectly presents zero as a hard tool-call cap:\n%s", prompt)
	}
	if !strings.Contains(prompt, "There is no separate verification-call cap") {
		t.Fatalf("prompt does not explain the default unlimited verification policy:\n%s", prompt)
	}
}

func TestAppendQwenReviewExecutionPlanLeavesToolCallsUnlimitedByDefault(t *testing.T) {
	prompt := appendReviewExecutionPlan("review this", automatedReviewOptions{
		sizeClass:            "standard",
		modelID:              "qwen/qwen3.7-plus",
		reasoningMaxTokens:   2048,
		maxIterations:        5,
		maxToolCalls:         0,
		maxVerificationCalls: 0,
	})
	if !strings.Contains(prompt, "no per-review tool-call cap") {
		t.Fatalf("Qwen prompt does not explain the default unlimited tool-call policy:\n%s", prompt)
	}
	if strings.Contains(prompt, "0 inspection/verification calls") {
		t.Fatalf("Qwen prompt incorrectly presents zero as a hard tool-call cap:\n%s", prompt)
	}
	if !strings.Contains(prompt, "no separate verification-call cap") {
		t.Fatalf("Qwen prompt does not explain the default unlimited verification policy:\n%s", prompt)
	}
}

func TestProjectReviewExecutionPlanLeavesTurnsToolsAndExplorationUnbounded(t *testing.T) {
	options := automatedReviewOptions{
		sizeClass:     "project",
		modelID:       "qwen/qwen3.7-plus",
		maxIterations: 0,
		maxToolCalls:  0,
	}.withExecutionPlan(reviewExecutionPlan{
		sizeClass:            "project",
		maxIterations:        0,
		maxToolCalls:         0,
		maxVerificationCalls: 1,
		explorationTimeout:   0,
		synthesisLead:        90 * time.Second,
	})
	if options.maxIterations != 0 || options.maxToolCalls != 0 || options.explorationTimeout != 0 {
		t.Fatalf("project review plan reintroduced a hidden ceiling: %#v", options)
	}
	prompt := appendReviewExecutionPlan("review this", options)
	for _, want := range []string{
		"no hard per-review model-turn cap",
		"no per-review tool-call cap",
		"until the synthesis reserve or outer deadline requires finalization",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("project review prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "100 seconds") {
		t.Fatalf("Qwen project prompt retained a fixed exploration ceiling:\n%s", prompt)
	}
}

// TestAppendReviewExecutionPlanReusesSharedRuleConstants asserts the bounded
// review plan's shared bullets are byte-identical to the single source of
// truth in pkg/prompts, not a separately maintained copy of the wording.
func TestAppendReviewExecutionPlanReusesSharedRuleConstants(t *testing.T) {
	prompt := appendReviewExecutionPlan("review this", automatedReviewOptions{
		sizeClass:           "focused",
		modelID:             "codex/gpt-5.6-luna",
		reasoningEffort:     "low",
		reasoningMaxTokens:  2048,
		maxIterations:       8,
		maxToolCalls:        12,
		verificationTimeout: 90 * time.Second,
		explorationTimeout:  3 * time.Minute,
		synthesisLead:       75 * time.Second,
	})
	for _, rule := range []string{
		prompts.RuleFindingsRequireProvedFalsification,
		prompts.RuleDisprovedOrUnresolvedGoesToRemarks,
		prompts.RuleUseHarnessVerificationEvidence,
	} {
		if !strings.Contains(prompt, rule) {
			t.Fatalf("bounded review plan missing shared rule %q:\n%s", rule, prompt)
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
