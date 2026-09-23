package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/modelprofile"
	"m31labs.dev/buckley/pkg/oneshot/commands"
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
	deepSeekV4ProReviewModel    = "deepseek/deepseek-v4-pro-0813"
	deepSeekFocusedReasoning    = 3072
	deepSeekStandardReasoning   = 6144
	deepSeekBroadReasoning      = 8192
	deepSeekReviewExploration   = 90 * time.Second
	deepSeekSupportingContext   = 32_000
	// Project reports carry a complete inventory, coverage ledger, and action
	// evidence. The runner clamps this to the provider's advertised maximum.
	projectReviewOutputTokenBudget = 32768
)

func effectiveReviewBehavior(opts automatedReviewOptions) *modelprofile.ReviewBehavior {
	if opts.reviewBehavior != nil {
		return modelprofile.NormalizeReviewBehavior(opts.reviewBehavior)
	}
	return modelprofile.DefaultReviewBehaviorForModel(opts.modelID)
}

// reviewDepth controls how much evidence a review is expected to collect
// before it synthesizes a verdict. Spot is the compatibility/default mode;
// the other modes add explicit falsification and verification obligations.
type reviewDepth string

const (
	reviewDepthSpot     reviewDepth = "spot"
	reviewDepthBalanced reviewDepth = "balanced"
	reviewDepthInDepth  reviewDepth = "in-depth"
)

func parseReviewDepth(value string) (reviewDepth, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "spot", "quick", "fast":
		return reviewDepthSpot, nil
	case "balanced", "standard", "normal", "investigate":
		return reviewDepthBalanced, nil
	case "in-depth", "in_depth", "deep", "detailed", "exhaustive":
		return reviewDepthInDepth, nil
	default:
		return "", fmt.Errorf("invalid review depth %q (want spot, balanced, or in-depth)", value)
	}
}

func normalizedReviewDepth(value reviewDepth) reviewDepth {
	depth, err := parseReviewDepth(string(value))
	if err != nil {
		return reviewDepthSpot
	}
	return depth
}

func reviewDepthNeedsVerification(depth string) bool {
	return normalizedReviewDepth(reviewDepth(depth)) != reviewDepthSpot
}

func reviewDepthLabel(depth reviewDepth) string {
	switch normalizedReviewDepth(depth) {
	case reviewDepthBalanced:
		return "BALANCED"
	case reviewDepthInDepth:
		return "IN-DEPTH"
	default:
		return "SPOT"
	}
}

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

// resolveReviewReasoningEffort resolves buckbot's reasoning effort. When
// buckbot.reasoning is "auto" (or any other unrecognized value) and Gate 2
// (decisions.gates.reasoning_choice) is enabled, it asks a Decisions
// choice question and uses the answer instead of Buckley's fixed adaptive
// default; on any gate failure, timeout, or disabled configuration it
// falls back to that existing default unchanged.
func resolveReviewReasoningEffort(ctx context.Context, cfg *config.Config, checker model.ReasoningChecker, modelID, explicit string) string {
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
		if effort, ok := reasoningChoiceGate(ctx, cfg); ok {
			return effort
		}
		return model.ResolveReasoningEffort(cfg, checker, nil, modelID, "review")
	default:
		if effort, ok := reasoningChoiceGate(ctx, cfg); ok {
			return effort
		}
		return model.ResolveReasoningEffort(cfg, checker, nil, modelID, "review")
	}
}

func resolveReviewExecutionPlan(engine *rules.Engine, facts rules.ReviewPlanFacts) reviewExecutionPlan {
	plan := reviewExecutionPlan{
		sizeClass:            "standard",
		reasoningEffort:      "medium",
		reasoningMaxTokens:   1536,
		maxOutputTokens:      projectReviewOutputTokenBudget,
		maxIterations:        5,
		maxToolCalls:         0,
		maxVerificationCalls: 1,
		verificationTimeout:  2 * time.Minute,
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
	// A configured/derived remote verification timeout (see
	// review.verification.runner.timeout and remoteVerificationDefaultTimeout)
	// is a floor, not a suggestion: the plan's generated default must not
	// shrink it back down to a budget sized for a single local package.
	if opts.verificationTimeoutFloor > 0 && opts.verificationTimeout < opts.verificationTimeoutFloor {
		opts.verificationTimeout = opts.verificationTimeoutFloor
	}
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
	behavior := effectiveReviewBehavior(opts)
	if behavior != nil {
		if opts.adaptiveReasoning {
			opts.reasoningMaxTokens = reviewBehaviorTokenBudget(behavior.ReasoningMaxTokensBySize, plan.sizeClass, opts.reasoningMaxTokens)
		} else {
			opts.reasoningMaxTokens = reviewBehaviorTokenBudget(behavior.ReasoningMaxTokensByEffort, opts.reasoningEffort, opts.reasoningMaxTokens)
		}
		minExploration := time.Duration(behavior.MinExplorationTimeoutSeconds) * time.Second
		// A zero exploration timeout is intentional for project reviews: the
		// outer review deadline and synthesis reserve are the only boundaries.
		if opts.explorationTimeout > 0 && minExploration > 0 && opts.explorationTimeout < minExploration {
			opts.explorationTimeout = minExploration
		}
		minCriticExploration := time.Duration(behavior.MinCriticExplorationTimeoutSeconds) * time.Second
		if minCriticExploration > 0 && opts.criticExploration < minCriticExploration {
			opts.criticExploration = minCriticExploration
		}
	}
	// In-depth is the completion-first mode. It removes generated plan caps,
	// while preserving any operator/configured limits explicitly supplied by
	// the caller. The outer timeout and runtime emergency fuses remain active.
	if normalizedReviewDepth(opts.depth) == reviewDepthInDepth {
		if !opts.maxIterationsExplicit {
			opts.maxIterations = 0
		}
		if !opts.maxToolCallsExplicit {
			opts.maxToolCalls = 0
		}
		opts.maxVerificationCalls = 0
		opts.explorationTimeout = 0
		opts.criticMaxIterations = 0
		opts.criticMaxToolCalls = 0
		opts.criticExploration = 0
	}
	return opts
}

func reviewBehaviorTokenBudget(values map[string]int, key string, fallback int) int {
	if len(values) == 0 {
		return fallback
	}
	if value := values[strings.ToLower(strings.TrimSpace(key))]; value > 0 {
		return value
	}
	return fallback
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
	profile := prompt + fmt.Sprintf(`

## Review Behavior Profile

- Scope: %s. Thinking budget: %d tokens per turn. Limits: %s, %s, %s.
- Read deterministic evidence before summarizing the diff. Treat hypotheses as tests, but treat provider-labeled violations as demonstrated defects unless exact counterevidence disproves them.
- Rank at most three concrete changed-behavior failures. Prefer identity/provenance mismatches, routing and bypass gates, producer/consumer key drift, and empty or failure paths over broad commentary.
- For workflow or release changes, trace event input -> checkout ref -> validated commit -> built bytes -> published identifier. Never assume checkout rewrites event variables.
- Use the supplied diff and harness-collected verification evidence first. Spend tool calls on focused inspections for named invariants; do not repeat successful searches or verification.
- Finish evidence collection within %s and reserve the final %d seconds for synthesis.
- Final response budget: %s. Use compact ledgers, but never omit required sections to fit.
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
	if isProjectReviewSize(opts.sizeClass) {
		profile += `
- Follow the advisory project-health response schema from the system prompt. Start exactly once with ## Project Health and return only that complete report.
- This mode is advisory only; never issue a merge verdict.
`
	} else {
		profile += `
- Follow the exact response schema from the system prompt. Account for every changed file, copy Feedback IDs exactly, cite immutable CI precisely, and return only the final review.
- Start exactly once with one ## Grade: heading.
- Missing, pending, or unavailable verification without a proved defect requires Grade B and NEEDS DISCUSSION with Blockers NONE.
- Put each Finding ID in exactly one Verdict list: CRITICAL and MAJOR are Blockers; MINOR is a Suggestion. Never list the same ID in both.
- APPROVE only after the strongest concrete failure is DISPROVED. Otherwise return the evidence-supported non-approval verdict.
`
	}
	if normalizedReviewDepth(opts.depth) == reviewDepthInDepth {
		profile += `

## Evidence-First In-Depth Checklist

- Before the final answer, perform at least one real ` + "`run_verification`" + ` call when that tool is available.
- The final answer must contain the literal headings ` + "`## Evidence Collected`" + `, ` + "`## Verification Ledger`" + `, and ` + "`## Coverage`" + `, plus ` + "`Completeness: COMPLETE`" + `. Never omit these headings to save tokens; a partial declaration is rejected.
`
	}
	return profile
}

func appendReviewExecutionPlan(prompt string, opts automatedReviewOptions) string {
	behavior := effectiveReviewBehavior(opts)
	if behavior != nil && behavior.Profile == modelprofile.ReviewProfileEvidenceFirst {
		return appendReviewDepthInstructions(appendQwenReviewExecutionPlan(prompt, opts), opts)
	}
	turnLimit := "unlimited"
	if opts.maxIterations > 0 {
		turnLimit = fmt.Sprintf("%d", opts.maxIterations)
	}
	toolLimit := "unlimited"
	if opts.maxToolCalls > 0 {
		toolLimit = fmt.Sprintf("%d", opts.maxToolCalls)
	}
	verificationLimit := "unlimited when offered"
	if opts.maxVerificationCalls > 0 {
		verificationLimit = fmt.Sprintf("%d when offered", opts.maxVerificationCalls)
	}
	explorationLimit := "outer deadline"
	if opts.explorationTimeout > 0 {
		explorationLimit = fmt.Sprintf("%ds", int(opts.explorationTimeout/time.Second))
	}
	verificationTimeout := "outer deadline"
	if opts.verificationTimeout > 0 {
		verificationTimeout = fmt.Sprintf("%ds", int(opts.verificationTimeout/time.Second))
	}
	reasoningEffort := strings.ToUpper(strings.TrimSpace(opts.reasoningEffort))
	if reasoningEffort == "" {
		reasoningEffort = "PROVIDER DEFAULT"
	}
	reasoningBudget := "provider default"
	if opts.reasoningMaxTokens > 0 {
		reasoningBudget = fmt.Sprintf("%s/%d tokens per turn", reasoningEffort, opts.reasoningMaxTokens)
	}
	planPrompt := prompt + fmt.Sprintf(`

## Review Runtime

- Scope: %s; model: %s.
- Budgets: reasoning %s; output %s; model turns %s; tool calls %s.
- Deadlines: evidence %s; verification %s per call; synthesis reserve %ds.
- Verification: Buckley owns the supplied baseline; focused retry capacity is %s.
`,
		strings.ToUpper(opts.sizeClass),
		opts.modelID,
		reasoningBudget,
		reviewOutputBudgetText(opts),
		turnLimit,
		toolLimit,
		explorationLimit,
		verificationTimeout,
		int(opts.synthesisLead/time.Second),
		verificationLimit,
	)
	if !isProjectReviewSize(opts.sizeClass) {
		planPrompt += `- Verdict mapping: missing, pending, or unavailable verification without a proved defect requires Grade B and NEEDS DISCUSSION with Blockers NONE; CRITICAL/MAJOR findings are Blockers; MINOR findings are Suggestions.
`
	}
	if behavior != nil && behavior.Profile == modelprofile.ReviewProfileStructuredCodeReview {
		planPrompt += `

## Structured Code Review Profile

- Use the structured tool-call channel exclusively. Never encode a tool invocation as XML, pseudo-tags, or assistant prose; if a tool result is needed, issue the real tool call.
- When ` + "`exec_program`" + ` is offered, prefer one read-only code-mode program for broad inventory, joins, and cross-references, then use narrow tool calls for follow-up evidence. Never assume code mode exists when it is not offered.
- Treat the Canopy inventory as a table of contents, not as proof. Follow important call sites and lifecycle edges into source, and keep a compact evidence ledger while exploring.
- Prefer one high-signal search or verification call over repeated equivalent calls. Preserve exact paths, symbols, commands, and result states for the final review.
- Before synthesis, check that every requested review surface has either direct evidence or an explicit ` + "`UNAVAILABLE`" + ` explanation. Do not infer approval from an empty or truncated tool response.
`
	}
	return appendReviewDepthInstructions(planPrompt, opts)
}

func isProjectReviewSize(sizeClass string) bool {
	return strings.EqualFold(strings.TrimSpace(sizeClass), "project")
}

// appendReviewDepthInstructions is deliberately layered after the provider
// profile. That keeps DeepSeek/Qwen compact profiles intact while giving all
// models the same observable depth contract.
func appendReviewDepthInstructions(prompt string, opts automatedReviewOptions) string {
	switch normalizedReviewDepth(opts.depth) {
	case reviewDepthBalanced:
		return prompt + `

## Review Depth: BALANCED INVESTIGATION

- Use two passes: first map the relevant state and call sites, then falsify the highest-risk hypotheses.
- For every proposed finding, trace the changed behavior through its definition, callers, configuration, failure path, and the nearest relevant test or executable check.
- Make the focused run_verification attempts required by the captured repository gate. If a required gate is unavailable, retry or let the harness fail closed; never emit a caveated completion.
- Keep a compact verification ledger in the final review. Every finding must point to a ledger entry marked ` + "`SUPPORTED`" + `, ` + "`DISPROVED`" + `, or ` + "`UNAVAILABLE`" + `.
- Retain the base review schema and add the literal ` + "`## Evidence Collected`" + ` and ` + "`## Verification Ledger`" + ` headings. List actual source, tool, and CI evidence in the first and tested hypotheses in the second. ` + "`## Coverage`" + ` does not replace either section.
- Cover the complete balanced scope: every changed file plus its direct callers, configuration gates, failure path, and nearest relevant test. Generated/vendor/build output may be excluded only when it is explicitly outside that scope.
- End with ` + "`Completeness: COMPLETE`" + `. If this scope cannot be completed, continue gathering evidence or let the harness fail the pass; do not emit a partial review.
`
	case reviewDepthInDepth:
		return prompt + `

## Review Depth: IN-DEPTH INVESTIGATION

- Treat this as an exhaustive repository, changeset, or PR investigation. Do not sample important source, tests, configuration, documentation, CI, persistence, concurrency, security, or provider-routing paths. Generated/vendor/build output is the only exclusion class, and it must be identified explicitly.
- Start from the supplied Canopy/table-of-contents evidence, then expand important symbols into callers, callees, state transitions, fast paths, and bypasses. Use code mode (` + "`exec_program`" + ` when offered) for broad read-only joins and paginated file/search tools for exact evidence.
- Maintain a coverage ledger while exploring. A large file must be read in pages; never treat truncated tool output as a complete read.
- For each material hypothesis, perform a focused falsification pass and every ` + "`run_verification`" + ` attempt required by the repository gate. Record the exact command, target, exit/status, and evidence conclusion. If a required check is unavailable, retry or let the harness fail closed rather than emitting a caveat.
- Trace every important change through producer/consumer boundaries, caches, dispatch gates, retries, persistence, and error/cancellation paths. Prefer executable evidence over source-shape heuristics.
- Before synthesis, run a separate adversarial pass for missed findings and stale assumptions. Do not promote an unresolved or unavailable check into a finding.
- The final review must include ` + "`## Evidence Collected`" + `, ` + "`## Verification Ledger`" + `, and ` + "`## Coverage`" + ` sections with ` + "`Completeness: COMPLETE`" + `. If the deadline prevents complete coverage, do not emit a review; the harness will retry or fail closed.
`
	default:
		// Spot is the compatibility/default profile. The existing provider
		// prompts already describe its fast evidence-first behavior; avoid
		// spending extra context tokens restating it for every review.
		return prompt
	}
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
