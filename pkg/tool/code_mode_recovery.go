package tool

import (
	"context"
	"errors"
	"strings"

	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/types"
)

// CodeModeRecoveryState records one turn's low-yield repository exploration.
// It is intentionally caller-owned so TUI and ACP keep the same recovery
// behavior without leaking state between user turns.
type CodeModeRecoveryState struct {
	zeroYieldStreak       int
	recommendationEmitted bool
}

func (s *CodeModeRecoveryState) observe(toolName string, result *builtin.Result, execErr error) int {
	if s == nil || !isZeroYieldExplorationTool(toolName) {
		return 0
	}
	if execErr != nil || result == nil || !result.Success {
		s.zeroYieldStreak = 0
		return 0
	}

	yield := ResultYieldForTool(toolName, result, execErr)
	if !yield.Observed {
		return s.zeroYieldStreak
	}
	if yield.Count > 0 {
		s.zeroYieldStreak = 0
		s.recommendationEmitted = false
		return 0
	}
	s.zeroYieldStreak++
	return s.zeroYieldStreak
}

func (s *CodeModeRecoveryState) markRecommendationEmitted() {
	if s != nil {
		s.recommendationEmitted = true
	}
}

func (s *CodeModeRecoveryState) recommendationWasEmitted() bool {
	return s != nil && s.recommendationEmitted
}

func isZeroYieldExplorationTool(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "search_text", "find_files", "list_directory":
		return true
	default:
		return false
	}
}

// CodeModeRecoveryGuidance returns Arbiter-selected recovery advice when an
// eligible repository tool fails or repeated successful exploration yields no
// results. A zero yield remains a successful result; the guidance makes that
// distinction explicit for the model and for visible operation projections.
// Calling it updates state exactly once for the attempted tool result.
func CodeModeRecoveryGuidance(
	evaluator types.RuleEvaluator,
	registry *Registry,
	allowedTools []string,
	toolName string,
	result *builtin.Result,
	execErr error,
	state *CodeModeRecoveryState,
) string {
	failed := execErr != nil || result == nil || !result.Success
	failureKind := ""
	if failed {
		failureKind = codeModeFailureKind(result, execErr)
	}
	zeroYieldStreak := state.observe(toolName, result, execErr)
	if (!failed && zeroYieldStreak == 0) || evaluator == nil {
		return ""
	}

	codeModeAvailable := false
	if registry != nil && IsToolAllowed("exec_program", allowedTools) {
		_, codeModeAvailable = registry.Get("exec_program")
	}
	yield := ResultYieldForTool(toolName, result, execErr)
	recovery, err := evaluator.EvalStrategy("runtime/code_mode", "failure_recovery", map[string]any{
		"code_mode.available":          codeModeAvailable,
		"tool.failed":                  failed,
		"tool.name":                    strings.TrimSpace(toolName),
		"failure.kind":                 failureKind,
		"tool.yield_observed":          yield.Observed,
		"tool.yield_count":             yield.Count,
		"tool.yield_unit":              yield.Unit,
		"repository.zero_yield_streak": zeroYieldStreak,
		"recovery.already_recommended": state.recommendationWasEmitted(),
	})
	if err != nil || recovery.String("action") != "recommend_exec_program" {
		return ""
	}

	guidance := strings.TrimSpace(recovery.String("message"))
	if guidance == "" {
		return ""
	}
	state.markRecommendationEmitted()
	return guidance
}

// AppendCodeModeRecoveryGuidance adds CodeModeRecoveryGuidance to a
// model-facing tool result. Callers that also render an operation surface can
// call CodeModeRecoveryGuidance directly and project the returned guidance
// visibly before they persist the same text to model history.
func AppendCodeModeRecoveryGuidance(
	modelOutput string,
	evaluator types.RuleEvaluator,
	registry *Registry,
	allowedTools []string,
	toolName string,
	result *builtin.Result,
	execErr error,
	state *CodeModeRecoveryState,
) string {
	guidance := CodeModeRecoveryGuidance(evaluator, registry, allowedTools, toolName, result, execErr, state)
	if guidance == "" {
		return modelOutput
	}
	if strings.TrimSpace(modelOutput) == "" {
		return guidance
	}
	return modelOutput + "\n\n" + guidance
}

// AppendCodeModeFailureGuidance is retained for callers that only have one
// isolated tool result. New turn-loop integrations should pass a
// CodeModeRecoveryState to AppendCodeModeRecoveryGuidance so successful
// zero-yield exploration can be governed too.
func AppendCodeModeFailureGuidance(
	modelOutput string,
	evaluator types.RuleEvaluator,
	registry *Registry,
	allowedTools []string,
	toolName string,
	result *builtin.Result,
	execErr error,
) string {
	return AppendCodeModeRecoveryGuidance(modelOutput, evaluator, registry, allowedTools, toolName, result, execErr, nil)
}

func codeModeFailureKind(result *builtin.Result, execErr error) string {
	if errors.Is(execErr, context.Canceled) || errors.Is(execErr, context.DeadlineExceeded) {
		return "cancelled"
	}

	parts := make([]string, 0, 2)
	if execErr != nil {
		parts = append(parts, execErr.Error())
	}
	if result != nil && result.Error != "" {
		parts = append(parts, result.Error)
	}
	failureText := strings.ToLower(strings.Join(parts, " "))
	if strings.Contains(failureText, "permission denied") ||
		(strings.Contains(failureText, "approval") && strings.Contains(failureText, "denied")) {
		return "permission_denied"
	}
	return "tool_error"
}
