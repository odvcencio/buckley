package main

import (
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
)

const (
	defaultReviewTimeout     = 4*time.Minute + 25*time.Second
	codexReviewModelFocused  = "codex/gpt-5.6-luna"
	codexReviewModelStandard = "codex/gpt-5.6-terra"
	codexReviewModelBroad    = "codex/gpt-5.6-sol"
)

type reviewExecutionPlan struct {
	sizeClass           string
	reasoningEffort     string
	reasoningMaxTokens  int
	maxIterations       int
	maxToolCalls        int
	verificationTimeout time.Duration
	explorationTimeout  time.Duration
	synthesisLead       time.Duration
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
		sizeClass:           "standard",
		reasoningEffort:     "medium",
		reasoningMaxTokens:  1536,
		maxIterations:       5,
		maxToolCalls:        5,
		verificationTimeout: 60 * time.Second,
		explorationTimeout:  75 * time.Second,
		synthesisLead:       165 * time.Second,
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
	plan.maxToolCalls = reviewPlanInt(result.Params["max_tool_calls"], plan.maxToolCalls)
	verificationSeconds := reviewPlanInt(result.Params["verification_timeout_seconds"], int(plan.verificationTimeout/time.Second))
	explorationSeconds := reviewPlanInt(result.Params["exploration_timeout_seconds"], int(plan.explorationTimeout/time.Second))
	reserveSeconds := reviewPlanInt(result.Params["synthesis_reserve_seconds"], int(plan.synthesisLead/time.Second))
	plan.verificationTimeout = time.Duration(verificationSeconds) * time.Second
	plan.explorationTimeout = time.Duration(explorationSeconds) * time.Second
	plan.synthesisLead = time.Duration(reserveSeconds) * time.Second
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

func (opts automatedReviewOptions) withExecutionPlan(plan reviewExecutionPlan) automatedReviewOptions {
	if opts.maxIterations <= 0 {
		opts.maxIterations = plan.maxIterations
	}
	opts.maxToolCalls = plan.maxToolCalls
	opts.reasoningMaxTokens = plan.reasoningMaxTokens
	opts.verificationTimeout = plan.verificationTimeout
	opts.explorationTimeout = plan.explorationTimeout
	opts.synthesisLead = plan.synthesisLead
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
	return opts
}

func appendReviewExecutionPlan(prompt string, opts automatedReviewOptions) string {
	return prompt + fmt.Sprintf(`

## Bounded Review Plan

- Size class: %s
- Model: %s
- Reasoning effort: %s
- Limit each model turn to %d reasoning tokens.
- Use at most %d model turns and %d total inspection or verification calls.
- Limit each verification command to %d seconds.
- Finish evidence collection within %d seconds.
- Keep the final %d seconds for a complete verdict.
- Return only the final review.
- Start the first line with "## Grade:".
- In "## CI Status", write "- Build: STATE" and "- Tests: STATE".
- Do not bold the Build or Tests labels.
- Do not expose analysis, repair commentary, progress text, or a plan.
- Keep the final review concise enough to fit the output limit.
- Inspect the supplied diff and structural evidence before you call a tool.
- Do not read a changed file when the supplied diff already shows the required lines.
- Use tools only for omitted definitions, callers, invariants, or targeted verification.
- Do not repeat equivalent searches, builds, or tests.
- If required evidence cannot fit or project guidance forbids it, finish with a non-approval verdict.
`,
		strings.ToUpper(opts.sizeClass),
		opts.modelID,
		strings.ToUpper(opts.reasoningEffort),
		opts.reasoningMaxTokens,
		opts.maxIterations,
		opts.maxToolCalls,
		int(opts.verificationTimeout/time.Second),
		int(opts.explorationTimeout/time.Second),
		int(opts.synthesisLead/time.Second),
	)
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
