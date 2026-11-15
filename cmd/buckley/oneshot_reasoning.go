package main

import (
	"fmt"
	"os"
	"strings"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

const (
	answerReserveMinTokens     = 1024
	answerReserveMaxTokens     = 16384
	answerReserveMinOutputCap  = 4096
	oneShotDefaultBudgetOutCap = 8192 // output cap the cost guard picks under --budget when none is given
)

// promptEffortValues are the efforts accepted for -p, in the order shown in
// error messages.
var promptEffortValues = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// normalizePromptEffort lower-cases and validates one effort word. An empty
// value is valid and means "not chosen".
func normalizePromptEffort(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	for _, accepted := range promptEffortValues {
		if value == accepted {
			return value, nil
		}
	}
	return "", fmt.Errorf("unsupported reasoning effort %q (use %s)", value, strings.Join(promptEffortValues, ", "))
}

// resolvePromptEffort picks the reasoning effort for a -p run: the --effort
// flag, then BUCKLEY_EFFORT_PROMPT, then BUCKLEY_ONESHOT_EFFORT. An empty
// result means no effort was chosen. It is separate from resolveOneshotEffort,
// whose accepted values are fixed for commit, pr and review.
func resolvePromptEffort(flagValue string) (string, error) {
	for _, candidate := range []string{flagValue, os.Getenv("BUCKLEY_EFFORT_PROMPT"), os.Getenv("BUCKLEY_ONESHOT_EFFORT")} {
		if strings.TrimSpace(candidate) != "" {
			return normalizePromptEffort(candidate)
		}
	}
	return "", nil
}

// oneShotReasoningBudget returns the reasoning budget that keeps an answer
// reserve inside outputCap, or false when no budget applies. The reserve is
// 25% of the cap, at least 1,024 and at most 16,384 tokens. minimal, low and
// medium further limit the budget to 10%, 20% and 50% of the cap. A cap under
// 4,096 or a budget under 1,024 (the provider minimum) gets no budget.
func oneShotReasoningBudget(outputCap int, effort string) (int, bool) {
	if outputCap < answerReserveMinOutputCap {
		return 0, false
	}
	reserve := min(max(outputCap/4, answerReserveMinTokens), answerReserveMaxTokens)
	budget := outputCap - reserve
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal":
		budget = min(budget, outputCap/10)
	case "low":
		budget = min(budget, outputCap/5)
	case "medium":
		budget = min(budget, outputCap/2)
	}
	if budget < answerReserveMinTokens {
		return 0, false
	}
	return budget, true
}

// oneShotBudgetProvider reports whether Buckley already handles a reasoning
// budget (reasoning.max_tokens) for the provider.
func oneShotBudgetProvider(providerID string) bool {
	return providerID == "openrouter" || providerID == "anthropic"
}

// applyOneShotAnswerReserve replaces an effort word with a reasoning budget
// that leaves room for the answer. It returns the request unchanged when the
// feature is off, no effort is in force, the output cap is unknown, or the
// provider keeps the effort word. Once applied, the effort is gone, so a second
// call returns the request unchanged.
func applyOneShotAnswerReserve(req model.ChatRequest, providerID string, enabled bool) model.ChatRequest {
	if !enabled || req.Reasoning == nil || req.Reasoning.Effort == "" || !oneShotBudgetProvider(providerID) {
		return req
	}
	budget, ok := oneShotReasoningBudget(max(req.MaxTokens, req.MaxCompletionTokens), req.Reasoning.Effort)
	if !ok {
		return req
	}
	reasoning := *req.Reasoning
	reasoning.Effort = ""
	reasoning.MaxTokens = budget
	req.Reasoning = &reasoning
	return req
}

// oneShotReasoningHeader returns the header lines that say which output cap
// and reasoning setting a -p run uses. They are printed without --quiet.
func oneShotReasoningHeader(cfg *config.Config, mgr *model.Manager, modelID string, limits acpLoopLimits) []string {
	var lines []string
	outputCap := limits.MaxOutputTokens
	if outputCap <= 0 && limits.MaxCostUSD > 0 {
		lines = append(lines, fmt.Sprintf("output cap: %d tokens per response (default while --budget is set; change with --max-output-tokens)", oneShotDefaultBudgetOutCap))
		outputCap = oneShotDefaultBudgetOutCap
	}
	effort := ""
	if cfg != nil {
		effort = strings.ToLower(strings.TrimSpace(cfg.Models.Reasoning))
	}
	switch effort {
	case "minimal", "low", "medium", "high", "xhigh":
	default:
		return lines
	}
	budget := "none"
	if limits.answerReserve && mgr != nil {
		if route, err := mgr.ResolveModelRoute(modelID); err == nil && oneShotBudgetProvider(route.ProviderID) {
			if tokens, ok := oneShotReasoningBudget(outputCap, effort); ok {
				budget = fmt.Sprint(tokens)
			}
		}
	}
	capText := "none"
	if outputCap > 0 {
		capText = fmt.Sprint(outputCap)
	}
	return append(lines, fmt.Sprintf("reasoning: effort=%s budget_tokens=%s output_cap=%s", effort, budget, capText))
}
