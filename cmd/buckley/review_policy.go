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
	defaultReviewTimeout = 4*time.Minute + 45*time.Second
)

type reviewExecutionPlan struct {
	sizeClass           string
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
		maxIterations:       11,
		maxToolCalls:        18,
		verificationTimeout: 75 * time.Second,
		explorationTimeout:  165 * time.Second,
		synthesisLead:       75 * time.Second,
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
	opts.verificationTimeout = plan.verificationTimeout
	opts.explorationTimeout = plan.explorationTimeout
	opts.synthesisLead = plan.synthesisLead
	opts.sizeClass = plan.sizeClass
	return opts
}

func appendReviewExecutionPlan(prompt string, opts automatedReviewOptions) string {
	return prompt + fmt.Sprintf(`

## Bounded Review Plan

- Size class: %s
- Use at most %d model turns and %d total inspection or verification calls.
- Limit each verification command to %d seconds.
- Finish evidence collection within %d seconds.
- Keep the final %d seconds for a complete verdict.
- Inspect the supplied diff and structural evidence before you call a tool.
- Do not repeat equivalent searches, builds, or tests.
- If required evidence cannot fit or project guidance forbids it, finish with a non-approval verdict.
`,
		strings.ToUpper(opts.sizeClass),
		opts.maxIterations,
		opts.maxToolCalls,
		int(opts.verificationTimeout/time.Second),
		int(opts.explorationTimeout/time.Second),
		int(opts.synthesisLead/time.Second),
	)
}
