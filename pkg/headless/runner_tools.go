package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/policy"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/toolrunner"
)

func (r *Runner) executeToolCall(ctx context.Context, tc model.ToolCall, args map[string]any, _ map[string]tool.Tool) (toolrunner.ToolExecutionResult, error) {
	decision := "auto"
	if args == nil {
		args = map[string]any{}
	}

	r.emit(RunnerEvent{
		Type:      EventToolCallStarted,
		SessionID: r.sessionID,
		Timestamp: time.Now(),
		Data: map[string]any{
			"toolCallId": tc.ID,
			"toolName":   tc.Function.Name,
			"arguments":  tc.Function.Arguments,
		},
	})

	if strings.EqualFold(tc.Function.Name, "run_shell") {
		if interactive, ok := args["interactive"].(bool); ok && interactive {
			message := "Tool execution denied: interactive shell sessions are not supported in headless mode"
			decision = "rejected"
			r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, message)
			r.emit(RunnerEvent{
				Type:      EventToolCallComplete,
				SessionID: r.sessionID,
				Timestamp: time.Now(),
				Data: map[string]any{
					"toolCallId": tc.ID,
					"toolName":   tc.Function.Name,
					"success":    false,
					"error":      message,
				},
			})
			if r.store != nil {
				decidedBy := "system"
				riskScore := 0
				if approvalDecision, score := r.approvalAuditFields(tc.ID); approvalDecision != "" || score != 0 {
					if approvalDecision != "" {
						decidedBy = approvalDecision
					}
					riskScore = score
				}
				if logErr := r.store.LogToolExecution(&storage.ToolAuditEntry{
					SessionID:  r.sessionID,
					ApprovalID: tc.ID,
					ToolName:   tc.Function.Name,
					ToolInput:  tc.Function.Arguments,
					RiskScore:  riskScore,
					Decision:   decision,
					DecidedBy:  decidedBy,
					ExecutedAt: time.Now(),
					DurationMs: 0,
					ToolOutput: message,
				}); logErr != nil {
					r.emitError("failed to log tool execution", logErr)
				}
			}
			return toolrunner.ToolExecutionResult{Result: message, Error: message, Success: false}, nil
		}
	}

	r.clampToolTimeoutArgs(tc.Function.Name, args)

	if r.requiresApproval(tc.Function.Name) {
		approved, err := r.waitForApproval(ctx, tc.ID, tc.Function.Name, args)
		if err != nil {
			return toolrunner.ToolExecutionResult{}, toolExecutionError{err: err}
		}
		if !approved {
			message := "Tool execution rejected by user"
			decision = "rejected"
			r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, message)
			r.emit(RunnerEvent{
				Type:      EventToolCallComplete,
				SessionID: r.sessionID,
				Timestamp: time.Now(),
				Data: map[string]any{
					"toolCallId": tc.ID,
					"toolName":   tc.Function.Name,
					"success":    false,
					"error":      message,
				},
			})
			if r.store != nil {
				decidedBy, riskScore := r.approvalAuditFields(tc.ID)
				if logErr := r.store.LogToolExecution(&storage.ToolAuditEntry{
					SessionID:  r.sessionID,
					ApprovalID: tc.ID,
					ToolName:   tc.Function.Name,
					ToolInput:  tc.Function.Arguments,
					RiskScore:  riskScore,
					Decision:   decision,
					DecidedBy:  decidedBy,
					ExecutedAt: time.Now(),
					DurationMs: 0,
					ToolOutput: message,
				}); logErr != nil {
					r.emitError("failed to log tool execution", logErr)
				}
			}
			return toolrunner.ToolExecutionResult{Result: message, Error: message, Success: false}, nil
		}
		decision = "approved"
	}

	startTime := time.Now()
	result, err := r.tools.ExecuteWithContext(ctx, tc.Function.Name, args)
	duration := time.Since(startTime)

	decidedBy, riskScore := r.approvalAuditFields(tc.ID)
	auditEntry := &storage.ToolAuditEntry{
		SessionID:  r.sessionID,
		ApprovalID: tc.ID,
		ToolName:   tc.Function.Name,
		ToolInput:  tc.Function.Arguments,
		RiskScore:  riskScore,
		Decision:   decision,
		DecidedBy:  decidedBy,
		ExecutedAt: startTime,
		DurationMs: duration.Milliseconds(),
	}

	if err != nil {
		errorResult := fmt.Sprintf("Error: %v", err)
		auditEntry.ToolOutput = errorResult

		r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, errorResult)
		r.emit(RunnerEvent{
			Type:      EventToolCallComplete,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data: map[string]any{
				"toolCallId": tc.ID,
				"toolName":   tc.Function.Name,
				"success":    false,
				"error":      err.Error(),
			},
		})

		if r.store != nil {
			if logErr := r.store.LogToolExecution(auditEntry); logErr != nil {
				r.emitError("failed to log tool execution", logErr)
			}
		}

		return toolrunner.ToolExecutionResult{
			Result:  errorResult,
			Error:   err.Error(),
			Success: false,
		}, nil
	}

	resultContent := r.formatToolResult(result)
	auditEntry.ToolOutput = truncateOutput(resultContent, 10000)

	r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, resultContent)

	r.emit(RunnerEvent{
		Type:      EventToolCallComplete,
		SessionID: r.sessionID,
		Timestamp: time.Now(),
		Data: map[string]any{
			"toolCallId": tc.ID,
			"toolName":   tc.Function.Name,
			"success":    result.Success,
			"output":     truncateOutput(resultContent, 1000),
		},
	})

	if r.store != nil {
		if logErr := r.store.LogToolExecution(auditEntry); logErr != nil {
			r.emitError("failed to log tool execution", logErr)
		}
	}

	return toolrunner.ToolExecutionResult{
		Result:  resultContent,
		Success: result.Success,
	}, nil
}

func (r *Runner) handleToolCalls(ctx context.Context, toolCalls []model.ToolCall) error {
	// Add the tool call message to conversation
	r.conv.AddToolCallMessage(toolCalls)

	for _, tc := range toolCalls {
		decision := "auto"

		r.emit(RunnerEvent{
			Type:      EventToolCallStarted,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data: map[string]any{
				"toolCallId": tc.ID,
				"toolName":   tc.Function.Name,
				"arguments":  tc.Function.Arguments,
			},
		})

		// Parse arguments
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			args = map[string]any{"raw": tc.Function.Arguments}
		}
		if args != nil && tc.ID != "" {
			args[tool.ToolCallIDParam] = tc.ID
		}

		if strings.EqualFold(tc.Function.Name, "run_shell") {
			if interactive, ok := args["interactive"].(bool); ok && interactive {
				message := "Tool execution denied: interactive shell sessions are not supported in headless mode"
				decision = "rejected"
				r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, message)
				r.emit(RunnerEvent{
					Type:      EventToolCallComplete,
					SessionID: r.sessionID,
					Timestamp: time.Now(),
					Data: map[string]any{
						"toolCallId": tc.ID,
						"toolName":   tc.Function.Name,
						"success":    false,
						"error":      message,
					},
				})
				if r.store != nil {
					decidedBy := "system"
					riskScore := 0
					if approvalDecision, score := r.approvalAuditFields(tc.ID); approvalDecision != "" || score != 0 {
						if approvalDecision != "" {
							decidedBy = approvalDecision
						}
						riskScore = score
					}
					if logErr := r.store.LogToolExecution(&storage.ToolAuditEntry{
						SessionID:  r.sessionID,
						ApprovalID: tc.ID,
						ToolName:   tc.Function.Name,
						ToolInput:  tc.Function.Arguments,
						RiskScore:  riskScore,
						Decision:   decision,
						DecidedBy:  decidedBy,
						ExecutedAt: time.Now(),
						DurationMs: 0,
						ToolOutput: message,
					}); logErr != nil {
						r.emitError("failed to log tool execution", logErr)
					}
				}
				continue
			}
		}
		r.clampToolTimeoutArgs(tc.Function.Name, args)

		// Check if tool requires approval
		if r.requiresApproval(tc.Function.Name) {
			approved, err := r.waitForApproval(ctx, tc.ID, tc.Function.Name, args)
			if err != nil {
				return fmt.Errorf("waiting for tool approval %s: %w", tc.Function.Name, err)
			}
			if !approved {
				message := "Tool execution rejected by user"
				decision = "rejected"
				r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, message)
				r.emit(RunnerEvent{
					Type:      EventToolCallComplete,
					SessionID: r.sessionID,
					Timestamp: time.Now(),
					Data: map[string]any{
						"toolCallId": tc.ID,
						"toolName":   tc.Function.Name,
						"success":    false,
						"error":      message,
					},
				})
				if r.store != nil {
					decidedBy, riskScore := r.approvalAuditFields(tc.ID)
					if logErr := r.store.LogToolExecution(&storage.ToolAuditEntry{
						SessionID:  r.sessionID,
						ApprovalID: tc.ID,
						ToolName:   tc.Function.Name,
						ToolInput:  tc.Function.Arguments,
						RiskScore:  riskScore,
						Decision:   decision,
						DecidedBy:  decidedBy,
						ExecutedAt: time.Now(),
						DurationMs: 0,
						ToolOutput: message,
					}); logErr != nil {
						r.emitError("failed to log tool execution", logErr)
					}
				}
				continue
			}
			decision = "approved"
		}

		// Execute tool with timing
		startTime := time.Now()
		result, err := r.tools.ExecuteWithContext(ctx, tc.Function.Name, args)
		duration := time.Since(startTime)

		// Log to audit trail
		decidedBy, riskScore := r.approvalAuditFields(tc.ID)
		auditEntry := &storage.ToolAuditEntry{
			SessionID:  r.sessionID,
			ApprovalID: tc.ID, // Use tool call ID as approval reference if approved
			ToolName:   tc.Function.Name,
			ToolInput:  tc.Function.Arguments,
			RiskScore:  riskScore,
			Decision:   decision,
			DecidedBy:  decidedBy,
			ExecutedAt: startTime,
			DurationMs: duration.Milliseconds(),
		}

		if err != nil {
			errorResult := fmt.Sprintf("Error: %v", err)
			auditEntry.ToolOutput = errorResult

			r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, errorResult)
			r.emit(RunnerEvent{
				Type:      EventToolCallComplete,
				SessionID: r.sessionID,
				Timestamp: time.Now(),
				Data: map[string]any{
					"toolCallId": tc.ID,
					"toolName":   tc.Function.Name,
					"success":    false,
					"error":      err.Error(),
				},
			})

			// Log failed execution
			if logErr := r.store.LogToolExecution(auditEntry); logErr != nil {
				r.emitError("failed to log tool execution", logErr)
			}
			continue
		}

		// Format result
		resultContent := r.formatToolResult(result)
		auditEntry.ToolOutput = truncateOutput(resultContent, 10000)

		r.conv.AddToolResponseMessage(tc.ID, tc.Function.Name, resultContent)

		r.emit(RunnerEvent{
			Type:      EventToolCallComplete,
			SessionID: r.sessionID,
			Timestamp: time.Now(),
			Data: map[string]any{
				"toolCallId": tc.ID,
				"toolName":   tc.Function.Name,
				"success":    result.Success,
				"output":     truncateOutput(resultContent, 1000),
			},
		})

		// Log successful execution
		if logErr := r.store.LogToolExecution(auditEntry); logErr != nil {
			r.emitError("failed to log tool execution", logErr)
		}
	}

	return nil
}

func (r *Runner) approvalAuditFields(approvalID string) (string, int) {
	if r == nil || r.store == nil || strings.TrimSpace(approvalID) == "" {
		return "", 0
	}
	approval, err := r.store.GetPendingApproval(approvalID)
	if err != nil || approval == nil {
		return "", 0
	}
	return approval.DecidedBy, approval.RiskScore
}

// evaluatePolicy runs the policy engine to determine if approval is needed.
// Returns the evaluation result.
func (r *Runner) evaluatePolicy(toolName string, args map[string]any) policy.EvaluationResult {
	call := policy.ToolCall{
		Name:      toolName,
		Input:     args,
		SessionID: r.sessionID,
	}
	return r.policyEngine.Evaluate(call)
}

func (r *Runner) requiresApproval(toolName string) bool {
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	if toolName != "" && len(r.requiredApprovalTools) > 0 {
		if _, ok := r.requiredApprovalTools[toolName]; ok {
			return true
		}
	}

	// Use policy engine if available
	if r.policyEngine != nil {
		result := r.evaluatePolicy(toolName, nil)
		return result.RequiresApproval
	}

	// Fallback to simple check
	return r.isDangerousTool(toolName)
}

func (r *Runner) isDangerousTool(toolName string) bool {
	dangerousTools := map[string]bool{
		"write_file":     true,
		"apply_patch":    true,
		"run_shell":      true,
		"search_replace": true,
	}
	return dangerousTools[toolName]
}

func (r *Runner) clampToolTimeoutArgs(toolName string, args map[string]any) {
	if r == nil || args == nil || r.maxToolExecTime <= 0 {
		return
	}
	maxSeconds := int(r.maxToolExecTime.Seconds())
	if maxSeconds <= 0 {
		return
	}

	switch strings.TrimSpace(strings.ToLower(toolName)) {
	case "run_shell", "run_tests":
		clampTimeoutSeconds(args, "timeout_seconds", maxSeconds)
	}
}

func clampTimeoutSeconds(args map[string]any, key string, maxSeconds int) {
	if args == nil || strings.TrimSpace(key) == "" || maxSeconds <= 0 {
		return
	}

	raw, ok := args[key]
	if !ok {
		args[key] = maxSeconds
		return
	}

	current, ok := anyToInt(raw)
	if !ok || current <= 0 || current > maxSeconds {
		args[key] = maxSeconds
	}
}

func anyToInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case float32:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	case int32:
		return int(v), true
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n), true
		}
		return 0, false
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return 0, false
		}
		if n, err := strconv.Atoi(v); err == nil {
			return n, true
		}
		return 0, false
	default:
		return 0, false
	}
}

func (r *Runner) waitForApproval(ctx context.Context, toolCallID, toolName string, args map[string]any) (bool, error) {
	// Evaluate policy for risk score
	var riskScore int
	var riskReasons []string
	if r.policyEngine != nil {
		result := r.evaluatePolicy(toolName, args)
		riskScore = result.RiskScore
		riskReasons = result.RiskReasons
	}

	expiresAt := time.Now().Add(5 * time.Minute)

	approval := &PendingApproval{
		ID:        toolCallID,
		ToolName:  toolName,
		ToolArgs:  args,
		CreatedAt: time.Now(),
		ExpiresAt: expiresAt,
	}

	// Persist to storage
	toolInputJSON, _ := json.Marshal(args)
	storedApproval := &storage.PendingApproval{
		ID:          toolCallID,
		SessionID:   r.sessionID,
		ToolName:    toolName,
		ToolInput:   string(toolInputJSON),
		RiskScore:   riskScore,
		RiskReasons: riskReasons,
		Status:      "pending",
		ExpiresAt:   expiresAt,
		CreatedAt:   time.Now(),
	}

	if err := r.store.CreatePendingApproval(storedApproval); err != nil {
		// Log but continue - approval can still work via channel
		r.emitError("failed to persist pending approval", err)
	}

	r.mu.Lock()
	r.pendingApproval = approval
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.pendingApproval = nil
		r.mu.Unlock()
	}()

	r.emit(RunnerEvent{
		Type:      EventApprovalRequired,
		SessionID: r.sessionID,
		Timestamp: time.Now(),
		Data: map[string]any{
			"id":          toolCallID,
			"toolName":    toolName,
			"toolArgs":    args,
			"riskScore":   riskScore,
			"riskReasons": riskReasons,
			"expiresAt":   approval.ExpiresAt,
		},
	})

	// Send push notification if worker is available
	if r.pushWorker != nil {
		if err := r.pushWorker.NotifyApprovalRequired(ctx, storedApproval); err != nil {
			// Log but don't fail - user can still approve via other channels
			r.emitError("failed to send push notification", err)
		}
	}

	// Wait for approval response or timeout
	select {
	case <-ctx.Done():
		r.updateApprovalStatus(toolCallID, "expired", "", "")
		return false, ctx.Err()
	case resp := <-r.approvalChan:
		if resp.ID == toolCallID {
			status := "rejected"
			if resp.Approved {
				status = "approved"
			}
			r.updateApprovalStatus(toolCallID, status, "headless-runner", resp.Reason)
			return resp.Approved, nil
		}
		return false, fmt.Errorf("approval ID mismatch")
	case <-time.After(5 * time.Minute):
		r.updateApprovalStatus(toolCallID, "expired", "", "timeout")
		return false, fmt.Errorf("approval timeout")
	}
}

// updateApprovalStatus updates the approval status in storage.
func (r *Runner) updateApprovalStatus(id, status, decidedBy, reason string) {
	approval, err := r.store.GetPendingApproval(id)
	if err != nil || approval == nil {
		return
	}

	if approval.Status != "pending" {
		return
	}

	approval.Status = status
	if decidedBy != "" {
		approval.DecidedBy = decidedBy
	}
	approval.DecidedAt = time.Now()
	approval.DecisionReason = strings.TrimSpace(reason)

	if err := r.store.UpdatePendingApproval(approval); err != nil {
		r.emitError("failed to update approval status", err)
	}
}

func (r *Runner) formatToolResult(result *builtin.Result) string {
	if result == nil {
		return "No result"
	}
	if !result.Success {
		return fmt.Sprintf("Error: %s", result.Error)
	}

	// Try to get meaningful output from DisplayData first
	if msg, ok := result.DisplayData["message"].(string); ok && msg != "" {
		return msg
	}

	// Serialize Data as JSON
	if len(result.Data) > 0 {
		data, err := json.MarshalIndent(result.Data, "", "  ")
		if err == nil {
			return string(data)
		}
	}

	return "Success"
}
