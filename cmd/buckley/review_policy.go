package main

import (
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/prompts"
	"m31labs.dev/buckley/pkg/rules"
)

const (
	defaultReviewTimeout = 4*time.Minute + 25*time.Second
	// Project reviews default to a longer wall-clock window because their
	// contract is exhaustive repository coverage rather than a small diff
	// sample. Callers can still choose an explicit --timeout.
	defaultProjectReviewTimeout = 20 * time.Minute
	codexReviewModelFocused     = "codex/gpt-5.6-luna"
	codexReviewModelStandard    = "codex/gpt-5.6-terra"
	codexReviewModelBroad       = "codex/gpt-5.6-sol"
	qwenReviewExploration       = 100 * time.Second
	qwenCriticExploration       = 75 * time.Second
	qwenFocusedReasoning        = 2048
	qwenStandardReasoning       = 3072
	qwenBroadReasoning          = 4096
	// Project reports carry a complete inventory, coverage ledger, and action
	// evidence. The runner clamps this to the provider's advertised maximum.
	projectReviewOutputTokenBudget = 32768
)

type reviewExecutionPlan struct {
	sizeClass            string
	reasoningEffort      string
	reasoningMaxTokens   int
	maxOutputTokens      int
	maxIterations        int
	maxToolCalls         int
	maxVerificationCalls int
	verificationTimeout  time.Duration
	explorationTimeout   time.Duration
	synthesisLead        time.Duration
	criticReserve        time.Duration
	criticMaxIterations  int
	criticMaxToolCalls   int
	criticExploration    time.Duration
	criticSynthesisLead  time.Duration
}

func resolveReviewReasoningEffort(cfg *config.Config, checker model.ReasoningChecker, modelID, explicit string) string {
	if checker == nil || !checker.SupportsReasoning(modelID) {
		return ""
	}
	switch explicit = strings.ToLower(strings.TrimSpace(explicit)); explicit {
	case "off", "none":
		return ""
	case "minimal", "low", "medium", "high", "xhigh":
		return explicit
	}
	configured := ""
	if cfg != nil {
		configured = strings.ToLower(strings.TrimSpace(cfg.Buckbot.Reasoning))
	}
	switch configured {
	case "off", "none":
		return ""
	case "minimal", "low", "medium", "high", "xhigh":
		return configured
	case "", "auto":
		return model.ResolveReasoningEffort(cfg, checker, nil, modelID, "review")
	default:
		return model.ResolveReasoningEffort(cfg, checker, nil, modelID, "review")
	}
}

func resolveReviewExecutionPlan(engine *rules.Engine, facts rules.ReviewPlanFacts) reviewExecutionPlan {
	plan := reviewExecutionPlan{
		sizeClass:            "standard",
		reasoningEffort:      "medium",
		reasoningMaxTokens:   1536,
		maxIterations:        5,
		maxToolCalls:         0,
		maxVerificationCalls: 1,
		verificationTimeout:  60 * time.Second,
		explorationTimeout:   50 * time.Second,
		synthesisLead:        90 * time.Second,
		criticReserve:        75 * time.Second,
		criticMaxIterations:  2,
		criticMaxToolCalls:   0,
		criticExploration:    20 * time.Second,
		criticSynthesisLead:  45 * time.Second,
	}
	if engine == nil {
		return plan
	}
	result, err := engine.EvalStrategy("review_plan", "review_plan", facts.ToMap())
	if err != nil {
		return plan
	}
	if value, ok := result.Params["size_class"].(string); ok && strings.TrimSpace(value) != "" {
		plan.sizeClass = strings.TrimSpace(value)
	}
	if value, ok := result.Params["reasoning_effort"].(string); ok && validReviewReasoningEffort(value) {
		plan.reasoningEffort = strings.ToLower(strings.TrimSpace(value))
	}
	plan.reasoningMaxTokens = reviewPlanInt(result.Params["reasoning_max_tokens"], plan.reasoningMaxTokens)
	plan.maxIterations = reviewPlanInt(result.Params["max_iterations"], plan.maxIterations)
	plan.maxToolCalls = reviewPlanLimit(result.Params["max_tool_calls"], plan.maxToolCalls)
	plan.maxVerificationCalls = reviewPlanInt(result.Params["max_verification_calls"], plan.maxVerificationCalls)
	verificationSeconds := reviewPlanInt(result.Params["verification_timeout_seconds"], int(plan.verificationTimeout/time.Second))
	explorationSeconds := reviewPlanInt(result.Params["exploration_timeout_seconds"], int(plan.explorationTimeout/time.Second))
	reserveSeconds := reviewPlanInt(result.Params["synthesis_reserve_seconds"], int(plan.synthesisLead/time.Second))
	criticReserveSeconds := reviewPlanInt(result.Params["approval_critic_reserve_seconds"], int(plan.criticReserve/time.Second))
	criticExplorationSeconds := reviewPlanInt(result.Params["critic_exploration_timeout_seconds"], int(plan.criticExploration/time.Second))
	criticSynthesisSeconds := reviewPlanInt(result.Params["critic_synthesis_reserve_seconds"], int(plan.criticSynthesisLead/time.Second))
	plan.criticMaxIterations = reviewPlanInt(result.Params["critic_max_iterations"], plan.criticMaxIterations)
	plan.criticMaxToolCalls = reviewPlanInt(result.Params["critic_max_tool_calls"], plan.criticMaxToolCalls)
	plan.verificationTimeout = time.Duration(verificationSeconds) * time.Second
	plan.explorationTimeout = time.Duration(explorationSeconds) * time.Second
	plan.synthesisLead = time.Duration(reserveSeconds) * time.Second
	plan.criticReserve = time.Duration(criticReserveSeconds) * time.Second
	plan.criticExploration = time.Duration(criticExplorationSeconds) * time.Second
	plan.criticSynthesisLead = time.Duration(criticSynthesisSeconds) * time.Second
	return plan
}

func reviewPlanInt(value any, fallback int) int {
	switch number := value.(type) {
	case int:
		if number > 0 {
			return number
		}
	case float64:
		if number > 0 {
			return int(number)
		}
	}
	return fallback
}

// reviewPlanLimit is like reviewPlanInt, but permits zero as an intentional
// unlimited value. Other review-plan fields need a positive value, while a
// tool-call limit uses zero to mean "let the review run".
func reviewPlanLimit(value any, fallback int) int {
	switch number := value.(type) {
	case int:
		if number >= 0 {
			return number
		}
	case float64:
		if number >= 0 {
			return int(number)
		}
	}
	return fallback
}

func enabledReviewDuration(enabled bool, value time.Duration) time.Duration {
	if !enabled {
		return 0
	}
	return value
}

func (opts automatedReviewOptions) withExecutionPlan(plan reviewExecutionPlan) automatedReviewOptions {
	if opts.maxIterations <= 0 {
		opts.maxIterations = plan.maxIterations
	}
	if opts.maxToolCalls <= 0 {
		opts.maxToolCalls = plan.maxToolCalls
	}
	if plan.maxOutputTokens > 0 {
		opts.maxOutputTokens = plan.maxOutputTokens
	}
	opts.maxVerificationCalls = plan.maxVerificationCalls
	opts.reasoningMaxTokens = plan.reasoningMaxTokens
	opts.verificationTimeout = plan.verificationTimeout
	opts.explorationTimeout = plan.explorationTimeout
	opts.synthesisLead = plan.synthesisLead
	opts.criticReserve = plan.criticReserve
	opts.criticMaxIterations = plan.criticMaxIterations
	opts.criticMaxToolCalls = plan.criticMaxToolCalls
	opts.criticExploration = plan.criticExploration
	opts.criticSynthesisLead = plan.criticSynthesisLead
	opts.sizeClass = plan.sizeClass
	if opts.adaptiveCodexModel {
		opts.modelID = codexReviewModelForSize(plan.sizeClass)
	}
	if opts.adaptiveReasoning {
		opts.reasoningEffort = plan.reasoningEffort
		if opts.adaptiveCodexModel {
			opts.reasoningEffort = codexReviewReasoningForSize(plan.sizeClass)
		}
	}
	if isQwenReviewModel(opts.modelID) {
		if opts.adaptiveReasoning {
			opts.reasoningMaxTokens = qwenReviewReasoningForSize(plan.sizeClass)
		} else {
			opts.reasoningMaxTokens = qwenReviewReasoningForEffort(opts.reasoningEffort)
		}
		// A zero exploration timeout is intentional for project reviews: the
		// outer review deadline and synthesis reserve are the only boundaries.
		// Preserve it instead of reintroducing a hidden Qwen-only ceiling.
		if opts.explorationTimeout > 0 && opts.explorationTimeout < qwenReviewExploration {
			opts.explorationTimeout = qwenReviewExploration
		}
		if opts.criticExploration < qwenCriticExploration {
			opts.criticExploration = qwenCriticExploration
		}
	}
	return opts
}

func (opts automatedReviewOptions) withVerificationTargetBudget(changedFiles []string) automatedReviewOptions {
	if opts.maxToolCalls <= 0 {
		// Unlimited reviews must not acquire a second, hidden ceiling merely
		// because Buckley generated an exact target list. Models may need to
		// retry an invalid or inconclusive command; the outer deadline and loop
		// governor remain the safety boundary.
		opts.maxVerificationCalls = 0
		return opts
	}
	budget := commands.ReviewVerificationCallBudget(changedFiles)
	if budget <= opts.maxVerificationCalls {
		return opts
	}
	if budget > opts.maxToolCalls {
		budget = opts.maxToolCalls
	}
	opts.maxVerificationCalls = budget
	return opts
}

func isQwenReviewModel(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	return modelID == "qwen/qwen3.7-plus" ||
		strings.HasSuffix(modelID, "/qwen3.7-plus") ||
		modelID == "qwen/qwen3.8-max" ||
		strings.HasSuffix(modelID, "/qwen3.8-max")
}

func qwenReviewReasoningForSize(sizeClass string) int {
	switch strings.ToLower(strings.TrimSpace(sizeClass)) {
	case "focused":
		return qwenFocusedReasoning
	case "broad", "project":
		return qwenBroadReasoning
	default:
		return qwenStandardReasoning
	}
}

func qwenReviewReasoningForEffort(effort string) int {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal":
		return 512
	case "low":
		return 1024
	case "high":
		return 4096
	case "xhigh":
		return 8192
	default:
		return 2048
	}
}

func appendQwenReviewExecutionPlan(prompt string, opts automatedReviewOptions) string {
	toolLimit := "no per-review tool-call cap"
	if opts.maxToolCalls > 0 {
		toolLimit = fmt.Sprintf("%d inspection/verification calls", opts.maxToolCalls)
	}
	turnLimit := "no hard per-review model-turn cap"
	if opts.maxIterations > 0 {
		turnLimit = fmt.Sprintf("%d model turns", opts.maxIterations)
	}
	explorationLimit := "until the synthesis reserve or outer deadline requires finalization"
	if opts.explorationTimeout > 0 {
		explorationLimit = fmt.Sprintf("%d seconds", int(opts.explorationTimeout/time.Second))
	}
	verificationLimit := "no separate verification-call cap"
	if opts.maxVerificationCalls > 0 {
		verificationLimit = fmt.Sprintf("%d verification calls", opts.maxVerificationCalls)
	}
	return prompt + fmt.Sprintf(`

## Qwen Review Profile

- Scope: %s. Thinking budget: %d tokens per turn. Limits: %s, %s, %s.
- Read deterministic evidence before summarizing the diff. Treat hypotheses as tests, but treat provider-labeled violations as demonstrated defects unless exact counterevidence disproves them.
- Rank at most three concrete changed-behavior failures. Prefer identity/provenance mismatches, routing and bypass gates, producer/consumer key drift, and empty or failure paths over broad commentary.
- For workflow or release changes, trace event input -> checkout ref -> validated commit -> built bytes -> published identifier. Never assume checkout rewrites event variables.
- Use the supplied diff first. Put every required verification target in the first tool-call batch, alongside only the focused inspections needed for named invariants; do not repeat successful searches or tests.
- Finish evidence collection within %s and reserve the final %d seconds for synthesis.
- Final response budget: %s. Use compact ledgers, but never omit required sections to fit.
- Follow the exact response schema from the system prompt. Account for every changed file, copy Feedback IDs exactly, cite immutable CI precisely, and return only the final review.
- Start exactly once with one ## Grade: heading. Never restart, repeat, or append a second copy of the review.
- Put each Finding ID in exactly one Verdict list: CRITICAL and MAJOR are Blockers; MINOR is a Suggestion. Never list the same ID in both.
- APPROVE only after the strongest concrete failure is DISPROVED. Otherwise return the evidence-supported non-approval verdict.
`,
		strings.ToUpper(opts.sizeClass),
		opts.reasoningMaxTokens,
		turnLimit,
		toolLimit,
		verificationLimit,
		explorationLimit,
		int(opts.synthesisLead/time.Second),
		reviewOutputBudgetText(opts),
	)
}

func appendReviewExecutionPlan(prompt string, opts automatedReviewOptions) string {
	if isQwenReviewModel(opts.modelID) {
		return appendQwenReviewExecutionPlan(prompt, opts)
	}
	turnLimit := "There is no hard per-review model-turn cap; continue until the review is complete or normal timeout/safety controls apply."
	if opts.maxIterations > 0 {
		turnLimit = fmt.Sprintf("Use at most %d model turns.", opts.maxIterations)
	}
	toolLimit := fmt.Sprintf("Use at most %d total inspection or verification calls.", opts.maxToolCalls)
	if opts.maxToolCalls <= 0 {
		toolLimit = "There is no per-review tool-call cap; continue evidence collection until the review is complete or normal timeout/safety controls apply."
	}
	explorationLimit := "until the synthesis reserve or outer deadline requires finalization"
	if opts.explorationTimeout > 0 {
		explorationLimit = fmt.Sprintf("%d seconds", int(opts.explorationTimeout/time.Second))
	}
	verificationLimit := "There is no separate verification-call cap; retry failed or inconclusive required targets when useful."
	if opts.maxVerificationCalls > 0 {
		verificationLimit = fmt.Sprintf("Use at most %d verification calls.", opts.maxVerificationCalls)
	}
	return prompt + fmt.Sprintf(`

## Bounded Review Plan

- Size class: %s
- Model: %s
- Reasoning effort: %s
- Limit each model turn to %d reasoning tokens.
- Final response budget: %s. Use compact ledgers, but never omit required sections to fit.
- %s
- %s
- %s
- Limit each verification command to %d seconds.
- Finish evidence collection within %s.
- Keep the final %d seconds for a complete verdict.
- Return only the final review.
- Start the first line with "## Grade:".
- In "## CI Status", write "- Build: STATE" and "- Tests: STATE".
- Do not bold the Build or Tests labels.
- When no feedback IDs exist, write exactly "- **Feedback disposition**: `+"`NONE_SUPPLIED`"+` — no prior feedback was supplied."
- When feedback IDs exist, use `+"`DISPOSITIONED`"+` and copy every exact ID.
- Omit a candidate finding when your own analysis disproves or withdraws it.
- Omit future-hardening and style observations from Findings. Put them in Remarks.
- Copy every source identifier and registry key exactly from inspected evidence.
- Compare measurements only when their workload labels and settings match.
- List MINOR findings as Suggestions, not Blockers.
- Use REQUEST CHANGES only with a Blocker or proved current failure.
- Pending, unknown, absent, or stale remote CI alone requires Grade B with NEEDS DISCUSSION.
- Keep a pending or unavailable CI condition in CI Status and Remarks, not Blockers or Findings.
- For that case, write "- **Recommendation**: NEEDS DISCUSSION" and "- **Blockers**: NONE".
- Never write NEEDS DISCUSSION as the Blockers value.
- Cite supplied commands and results even when this review makes no duplicate tool call.
- Missing duplicate verification alone requires Grade B with NEEDS DISCUSSION, not Grade C.
- Write the Falsification conclusion as one bare token with no words after it.
`+prompts.RuleFindingsRequireProvedFalsification+`
`+prompts.RuleDisprovedOrUnresolvedGoesToRemarks+`
- Require a current failing input, violated invariant, failing check, or reproducible behavior for every Finding.
- Treat CONFIRMED_PASS as authoritative for the behavior that its focused command exercises.
- Never let a filename, field-visibility, or source-shape heuristic override a passing focused test.
- Treat INCONCLUSIVE, timeout, cancellation, and unavailable verification as unknown evidence, never proof of failure.
- Report an INCONCLUSIVE verification as UNAVAILABLE in Build and Tests. INCONCLUSIVE is not an output state.
- Treat repository verification directives as execution policy. Never replace a required Docker or CI gate with a host command.
- A self-selected verification timeout is a review limitation. It cannot create a Finding, Blocker, FAIL state, or Grade C.
- Use exact Go test names. The verification tool anchors the complete -run alternation.
- For Go approval evidence, call run_verification with kind=test. Go kind=build does not execute tests.
`+prompts.RuleVerificationInFirstBatch+`
- Omit ASD-STE100, comment-length, wording, naming, and style observations from every section.
- Move possible rename, regeneration, test drift, and private test-hook concerns to Remarks.
- Do not expose analysis, repair commentary, progress text, or a plan.
- Keep the final review concise enough to fit the output limit.
- Inspect the supplied diff and structural evidence before you call a tool.
- Do not read a changed file when the supplied diff already shows the required lines.
- Use tools only for omitted definitions, callers, invariants, or targeted verification.
- Before approval, trace changed state through each cache, dispatch gate, and fast path that can bypass it.
- Do not treat direct helper tests as proof that production dispatch reaches the changed behavior.
- Do not repeat equivalent searches, builds, or tests.
- If required evidence cannot fit or project guidance forbids it, finish with a non-approval verdict.
`,
		strings.ToUpper(opts.sizeClass),
		opts.modelID,
		strings.ToUpper(opts.reasoningEffort),
		opts.reasoningMaxTokens,
		reviewOutputBudgetText(opts),
		turnLimit,
		toolLimit,
		verificationLimit,
		int(opts.verificationTimeout/time.Second),
		explorationLimit,
		int(opts.synthesisLead/time.Second),
	)
}

func reviewOutputBudgetText(opts automatedReviewOptions) string {
	if opts.maxOutputTokens > 0 {
		return fmt.Sprintf("%d completion tokens after provider capability clamping", opts.maxOutputTokens)
	}
	return "provider-derived completion budget (reasoning budget plus final-answer allowance)"
}

func isAdaptiveCodexReviewSelector(modelID string) bool {
	switch strings.ToLower(strings.TrimSpace(modelID)) {
	case "codex", "codex/auto", "codex/adaptive":
		return true
	default:
		return false
	}
}

func codexReviewModelForSize(sizeClass string) string {
	switch strings.ToLower(strings.TrimSpace(sizeClass)) {
	case "focused":
		return codexReviewModelFocused
	case "broad", "project":
		return codexReviewModelBroad
	default:
		return codexReviewModelStandard
	}
}

func codexReviewReasoningForSize(sizeClass string) string {
	if strings.EqualFold(strings.TrimSpace(sizeClass), "focused") {
		// Luna can spend more reasoning on a small diff without creating the
		// broad-review latency tail seen with Sol.
		return "xhigh"
	}
	// Terra and Sol use medium reasoning. Sol supplies the broad-review
	// capacity without the long tail observed with high reasoning.
	return "medium"
}

func resolveConfiguredReviewReasoning(cfg *config.Config) string {
	if cfg == nil {
		return "medium"
	}
	switch value := strings.ToLower(strings.TrimSpace(cfg.Buckbot.Reasoning)); value {
	case "minimal", "low", "medium", "high", "xhigh":
		return value
	default:
		return "medium"
	}
}

func reviewReasoningIsAdaptive(cfg *config.Config, explicit string) bool {
	if validReviewReasoningEffort(explicit) {
		return false
	}
	if cfg == nil {
		return true
	}
	value := strings.ToLower(strings.TrimSpace(cfg.Buckbot.Reasoning))
	return value == "" || value == "auto"
}

func validReviewReasoningEffort(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "minimal", "low", "medium", "high", "xhigh":
		return true
	default:
		return false
	}
}
