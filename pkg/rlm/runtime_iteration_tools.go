package rlm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/encoding/toon"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

type coordinatorToolResult struct {
	ID     string
	Name   string
	Result string
}

func (r *Runtime) executeCoordinatorTools(ctx context.Context, registry *tool.Registry, calls []model.ToolCall) []coordinatorToolResult {
	results := make([]coordinatorToolResult, 0, len(calls))
	for _, call := range calls {
		name := call.Function.Name
		result := coordinatorToolResult{ID: call.ID, Name: name}
		args := map[string]any{}
		if call.Function.Arguments != "" {
			_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
		}
		if call.ID != "" {
			args[tool.ToolCallIDParam] = call.ID
		}
		res, err := registry.ExecuteWithContext(ctx, name, args)
		if err != nil {
			result.Result = fmt.Sprintf("execution error: %v", err)
		} else {
			result.Result = r.formatCoordinatorResult(res)
		}
		results = append(results, result)
	}
	return results
}

func (r *Runtime) emitIteration(event IterationEvent) {
	r.recordHistory(event)

	r.hooksMu.RLock()
	hooks := append([]IterationHook{}, r.hooks...)
	r.hooksMu.RUnlock()
	for _, hook := range hooks {
		hook(event)
	}

	if r.telemetry != nil {
		data := map[string]any{
			"iteration":       event.Iteration,
			"max_iterations":  event.MaxIterations,
			"ready":           event.Ready,
			"tokens_used":     event.TokensUsed,
			"tokens_max":      event.MaxTokens,
			"summary":         event.Summary,
			"reasoning_trace": event.ReasoningTrace,
		}
		if len(event.Scratchpad) > 0 {
			data["scratchpad"] = formatScratchpadSummaries(event.Scratchpad)
		}
		if len(event.Delegations) > 0 {
			data["delegations"] = event.Delegations
		}
		data["budget"] = map[string]any{
			"tokens_used":      event.BudgetStatus.TokensUsed,
			"tokens_max":       event.BudgetStatus.TokensMax,
			"tokens_remaining": event.BudgetStatus.TokensRemaining,
			"tokens_percent":   event.BudgetStatus.TokensPercent,
			"wall_time":        event.BudgetStatus.WallTimeElapsed,
			"warning":          event.BudgetStatus.Warning,
		}
		r.telemetry.Publish(telemetry.Event{
			Type:      telemetry.EventRLMIteration,
			SessionID: r.sessionID,
			Data:      data,
		})

		if event.BudgetStatus.Warning != "" {
			r.telemetry.Publish(telemetry.Event{
				Type:      telemetry.EventRLMBudgetWarning,
				SessionID: r.sessionID,
				Data: map[string]any{
					"warning":          event.BudgetStatus.Warning,
					"tokens_percent":   event.BudgetStatus.TokensPercent,
					"tokens_remaining": event.BudgetStatus.TokensRemaining,
				},
			})
		}
	}
}

// recordHistory saves iteration info for context learning with automatic compaction.
func (r *Runtime) recordHistory(event IterationEvent) {
	if r == nil {
		return
	}
	entry := IterationHistory{
		Iteration:   event.Iteration,
		Delegations: event.Delegations,
		Summary:     event.Summary,
		TokensUsed:  event.TokensUsed,
	}
	r.historyMu.Lock()
	r.history = append(r.history, entry)
	needsCompaction := len(r.history) > r.config.Coordinator.HistoryMaxItems
	r.historyMu.Unlock()

	if needsCompaction {
		r.triggerAsyncCompaction()
	}
}

// compactHistoryLocked summarizes old iterations to save context space.
// Must be called with historyMu held.
func (r *Runtime) compactHistoryLocked() {
	maxItems := r.config.Coordinator.HistoryMaxItems
	compactBatch := r.config.Coordinator.HistoryCompactN
	keepRecent := r.config.Coordinator.HistoryKeepRecent

	if maxItems <= 0 {
		maxItems = 8
	}
	if compactBatch <= 0 {
		compactBatch = 3
	}
	if keepRecent <= 0 {
		keepRecent = 3
	}

	if len(r.history) <= maxItems {
		return
	}

	compactableCount := len(r.history) - keepRecent
	if compactableCount < compactBatch {
		return
	}

	toCompact := r.history[:compactBatch]
	remaining := r.history[compactBatch:]

	var totalTokens int
	var allDelegations []DelegationInfo
	var summaryParts []string
	iterations := make([]int, 0, len(toCompact))

	for _, h := range toCompact {
		iterations = append(iterations, h.Iteration)
		totalTokens += h.TokensUsed
		allDelegations = append(allDelegations, h.Delegations...)
		if h.Summary != "" {
			summary := h.Summary
			if len(summary) > 50 {
				summary = summary[:50] + "..."
			}
			summaryParts = append(summaryParts, summary)
		}
	}

	compacted := IterationHistory{
		Iteration:  iterations[0],
		Compacted:  true,
		TokensUsed: totalTokens,
		Summary:    fmt.Sprintf("[Compacted iterations %d-%d] %s", iterations[0], iterations[len(iterations)-1], strings.Join(summaryParts, "; ")),
	}

	successCount := 0
	failCount := 0
	for _, d := range allDelegations {
		if d.Success {
			successCount++
		} else {
			failCount++
		}
	}
	if successCount > 0 || failCount > 0 {
		compacted.Delegations = []DelegationInfo{{
			TaskID:  fmt.Sprintf("compacted-%d-%d", iterations[0], iterations[len(iterations)-1]),
			Summary: fmt.Sprintf("%d delegations (%d succeeded, %d failed)", successCount+failCount, successCount, failCount),
			Success: failCount == 0,
		}}
	}

	r.history = append([]IterationHistory{compacted}, remaining...)
}

// calculateBudgetStatus computes current budget status with warnings.
func (r *Runtime) calculateBudgetStatus(tokensUsed, maxTokens int, start time.Time) BudgetStatus {
	status := BudgetStatus{
		TokensUsed: tokensUsed,
		TokensMax:  maxTokens,
	}

	if maxTokens > 0 {
		status.TokensRemaining = maxTokens - tokensUsed
		if status.TokensRemaining < 0 {
			status.TokensRemaining = 0
		}
		status.TokensPercent = float64(tokensUsed) / float64(maxTokens) * 100
	}

	maxWallTime := r.config.Coordinator.MaxWallTime
	if maxWallTime <= 0 {
		maxWallTime = DefaultConfig().Coordinator.MaxWallTime
	}
	if maxWallTime > 0 {
		elapsed := time.Since(start)
		status.WallTimeElapsed = elapsed.Round(time.Second).String()
		status.WallTimeMax = maxWallTime.String()
		status.WallTimePercent = float64(elapsed) / float64(maxWallTime) * 100
	}

	if status.TokensPercent >= 90 || status.WallTimePercent >= 90 {
		status.Warning = "critical"
	} else if status.TokensPercent >= 75 || status.WallTimePercent >= 75 {
		status.Warning = "low"
	}

	return status
}

func (r *Runtime) adaptiveConfidenceThreshold(base float64, status BudgetStatus) float64 {
	threshold := base
	if threshold <= 0 {
		threshold = DefaultConfig().Coordinator.ConfidenceThreshold
	}
	if status.TokensPercent >= 90 || status.WallTimePercent >= 90 {
		threshold *= 0.8
	} else if status.TokensPercent >= 75 || status.WallTimePercent >= 75 {
		threshold *= 0.9
	}
	if threshold < 0 {
		threshold = 0
	}
	if threshold > 1 {
		threshold = 1
	}
	return threshold
}

// extractDelegationsFromToolResults parses delegation info from tool results for transparency.
func (r *Runtime) extractDelegationsFromToolResults(results []coordinatorToolResult) []DelegationInfo {
	var delegations []DelegationInfo

	for _, result := range results {
		if result.Name != "delegate" && result.Name != "delegate_batch" {
			continue
		}

		var parsed map[string]any
		if err := json.Unmarshal([]byte(result.Result), &parsed); err != nil {
			continue
		}

		if data, ok := parsed["data"].(map[string]any); ok {
			deleg := extractDelegationFromData(data)
			if deleg.TaskID != "" || deleg.Weight != "" {
				delegations = append(delegations, deleg)
			}
		}

		if data, ok := parsed["data"].(map[string]any); ok {
			if results, ok := data["results"].([]any); ok {
				for _, item := range results {
					if itemMap, ok := item.(map[string]any); ok {
						deleg := extractDelegationFromData(itemMap)
						if deleg.TaskID != "" || deleg.Weight != "" {
							delegations = append(delegations, deleg)
						}
					}
				}
			}
		}
	}

	return delegations
}

// extractDelegationFromData extracts DelegationInfo from a data map.
func extractDelegationFromData(data map[string]any) DelegationInfo {
	info := DelegationInfo{}

	if v, ok := data["task_id"].(string); ok {
		info.TaskID = v
	}
	if v, ok := data["agent_id"].(string); ok && info.TaskID == "" {
		info.TaskID = v
	}
	if v, ok := data["weight_requested"].(string); ok {
		info.Weight = v
	}
	if v, ok := data["weight_used"].(string); ok {
		info.WeightUsed = v
	}
	if v, ok := data["model"].(string); ok {
		info.Model = v
	}
	if v, ok := data["summary"].(string); ok {
		info.Summary = v
	}
	if v, ok := data["escalated"].(bool); ok {
		info.Escalated = v
	}
	if v, ok := data["tool_calls_count"].(float64); ok {
		info.ToolCallsCount = int(v)
	}
	if _, ok := data["error"].(string); ok && data["error"] != "" {
		info.Success = false
	} else {
		info.Success = true
	}

	return info
}

func (r *Runtime) formatCoordinatorResult(res *builtin.Result) string {
	if res == nil {
		return ""
	}
	payload := map[string]any{"success": res.Success}
	if res.Error != "" {
		payload["error"] = res.Error
	}
	if res.Data != nil {
		payload["data"] = res.Data
	}
	codec := r.resultCodec
	if codec == nil {
		codec = toon.New(true)
	}
	encoded, err := codec.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("{\"success\":%t}", res.Success)
	}
	return string(encoded)
}

func extractText(msg model.Message) string {
	content, err := model.ExtractTextContent(msg.Content)
	if err != nil {
		return fmt.Sprintf("%v", msg.Content)
	}
	return content
}

func formatScratchpadSummaries(summaries []EntrySummary) []map[string]any {
	out := make([]map[string]any, 0, len(summaries))
	for _, summary := range summaries {
		out = append(out, map[string]any{
			"key":        summary.Key,
			"type":       string(summary.Type),
			"summary":    summary.Summary,
			"created_by": summary.CreatedBy,
			"created_at": summary.CreatedAt,
		})
	}
	return out
}
