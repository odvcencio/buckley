package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/odvcencio/buckley/pkg/types"
)

// EventType identifies streaming event kinds.
type EventType int

const (
	EventTextDelta EventType = iota
	EventToolUse
	EventUsage
	EventStop
)

// StreamEvent is a single event from the API stream.
type StreamEvent struct {
	Type       EventType
	Text       string
	ToolCall   *ToolCall
	TokenUsage *TokenUsage
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any
}

// TokenUsage from a single API call.
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
}

// ToolResult is the output of a tool execution.
type ToolResult struct {
	Output  string
	IsError bool
}

// ToolSpec describes an available tool.
type ToolSpec struct {
	Name         string
	RequiredTier types.PermissionTier
}

// ChatRequest is sent to the API.
type ChatRequest struct {
	SystemPrompt string
	Messages     []ChatMessage
}

// ChatMessage is a message in the conversation.
type ChatMessage struct {
	Role    string
	Content string
}

// ApiClient abstracts the LLM call.
type ApiClient interface {
	Stream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error)
}

// ToolExecutor abstracts tool dispatch.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, input map[string]any) (*ToolResult, error)
	ExecuteWithSandbox(ctx context.Context, call ToolCall, level types.SandboxLevel) (*ToolResult, error)
	RequiredTier(name string) types.PermissionTier
	Available(role string, tier types.PermissionTier) []ToolSpec
}

// TurnSummary is the result of a single turn.
type TurnSummary struct {
	FinalMessage string
	Iterations   int
	ToolUses     []ToolUseRecord
	AuditTrail   []AuditEntry
}

// FinalText returns the accumulated assistant text.
func (s *TurnSummary) FinalText() string {
	return s.FinalMessage
}

// ToolUseRecord logs a tool invocation.
type ToolUseRecord struct {
	Name   string
	Input  map[string]any
	Output string
	Error  string
}

// AuditEntry logs an arbiter decision.
type AuditEntry struct {
	Domain  string
	Action  string
	Details string
}

var (
	ErrMaxIterations    = fmt.Errorf("max iterations reached")
	ErrBudgetExhausted  = fmt.Errorf("session budget exhausted")
	ErrBudgetEvalFailed = fmt.Errorf("budget evaluation failed")
)

// RuntimeLoop is the governed agent loop.
type RuntimeLoop struct {
	api       ApiClient
	tools     ToolExecutor
	escalator types.PermissionEscalator
	sandbox   types.SandboxResolver
	evaluator types.RuleEvaluator
	maxIter   int
	role      string

	// Accumulated state
	messages []ChatMessage
	text     strings.Builder
	toolUses []ToolUseRecord
	audit    []AuditEntry
}

// NewRuntimeLoop creates a new runtime loop.
func NewRuntimeLoop(
	api ApiClient,
	tools ToolExecutor,
	escalator types.PermissionEscalator,
	sandbox types.SandboxResolver,
	evaluator types.RuleEvaluator,
) *RuntimeLoop {
	return &RuntimeLoop{
		api:       api,
		tools:     tools,
		escalator: escalator,
		sandbox:   sandbox,
		evaluator: evaluator,
		maxIter:   16,
		role:      "interactive",
	}
}

func (r *RuntimeLoop) SetMaxIterations(n int) { r.maxIter = n }
func (r *RuntimeLoop) SetRole(role string)    { r.role = role }

// RunTurn executes one user->assistant->tool->assistant cycle.
func (r *RuntimeLoop) RunTurn(ctx context.Context, input string) (*TurnSummary, error) {
	r.messages = append(r.messages, ChatMessage{Role: "user", Content: input})
	r.text.Reset()
	r.toolUses = nil
	r.audit = nil

	for iter := 0; iter < r.maxIter; iter++ {
		// Stream API response
		ch, err := r.api.Stream(ctx, ChatRequest{Messages: r.messages})
		if err != nil {
			return nil, fmt.Errorf("streaming api: %w", err)
		}

		var toolCalls []ToolCall
		for event := range ch {
			switch event.Type {
			case EventTextDelta:
				r.text.WriteString(event.Text)
			case EventToolUse:
				if event.ToolCall != nil {
					toolCalls = append(toolCalls, *event.ToolCall)
				}
			case EventUsage:
				// Cost check could go here with UsageTracker
			case EventStop:
				// End of stream
			}
		}

		// Mid-turn cost check via arbiter (if evaluator is available)
		if r.evaluator != nil {
			costResult, err := r.evaluator.EvalStrategy("cost/budgets", "cost_policy", map[string]any{
				"budget_util": 0.0, // caller should wire real values
			})
			if err != nil {
				slog.Warn("cost eval failed, continuing", "error", err)
			} else if costResult.String("action") == "halt" {
				return r.summarize(iter), ErrBudgetExhausted
			}
		}

		// Add assistant message
		r.messages = append(r.messages, ChatMessage{Role: "assistant", Content: r.text.String()})

		// No tool calls = done
		if len(toolCalls) == 0 {
			return r.summarize(iter), nil
		}

		// Execute tool calls with permission + sandbox governance
		for _, call := range toolCalls {
			var result *ToolResult
			var execErr error

			// Permission check
			if r.escalator != nil {
				outcome, _ := r.escalator.Decide(ctx, types.EscalationRequest{
					ToolName:     call.Name,
					RequiredTier: r.tools.RequiredTier(call.Name),
					AgentRole:    r.role,
				})
				r.audit = append(r.audit, AuditEntry{
					Domain:  "permissions/escalation",
					Action:  fmt.Sprintf("granted=%v", outcome.Granted),
					Details: outcome.AuditNote,
				})
				if !outcome.Granted {
					result = &ToolResult{Output: "permission denied: " + outcome.AuditNote, IsError: true}
					r.toolUses = append(r.toolUses, ToolUseRecord{
						Name: call.Name, Input: call.Input, Error: result.Output,
					})
					r.messages = append(r.messages, ChatMessage{Role: "tool", Content: result.Output})
					continue
				}
			}

			// Sandbox resolution
			sandboxLvl := types.SandboxNone
			if r.sandbox != nil {
				sandboxLvl = r.sandbox.ForTool(call.Name, r.role, 0)
			}

			// Execute
			result, execErr = r.tools.ExecuteWithSandbox(ctx, call, sandboxLvl)
			if execErr != nil {
				result = &ToolResult{Output: execErr.Error(), IsError: true}
			}

			record := ToolUseRecord{Name: call.Name, Input: call.Input, Output: result.Output}
			if result.IsError {
				record.Error = result.Output
			}
			r.toolUses = append(r.toolUses, record)
			r.messages = append(r.messages, ChatMessage{Role: "tool", Content: result.Output})
		}
	}

	return r.summarize(r.maxIter), ErrMaxIterations
}

func (r *RuntimeLoop) summarize(iterations int) *TurnSummary {
	return &TurnSummary{
		FinalMessage: r.text.String(),
		Iterations:   iterations,
		ToolUses:     r.toolUses,
		AuditTrail:   r.audit,
	}
}
