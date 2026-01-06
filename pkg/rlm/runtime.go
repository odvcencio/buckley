package rlm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/coordination/security"
	"github.com/odvcencio/buckley/pkg/encoding/toon"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

const coordinatorSystemPrompt = `You are the Buckley RLM Coordinator - an orchestration layer that delegates work to specialized sub-agents while maintaining strategic oversight.

## Your Role
You do NOT execute tools directly. Instead, you:
1. Break down the user's request into discrete sub-tasks
2. Delegate each sub-task to an appropriately-weighted sub-agent
3. Review summaries and scratchpad entries to synthesize results
4. Set the final answer when confident

## Available Tools

**delegate** - Dispatch a single task to a sub-agent
- task (required): Clear, actionable instruction for the sub-agent
- weight: trivial|light|medium|heavy|reasoning (affects model selection)
- tools: Optional list of allowed tools (e.g., ["file", "shell"])
- Returns: summary, scratchpad_key, model used

**delegate_batch** - Dispatch multiple independent tasks in parallel
- tasks: Array of {task, weight, tools} objects
- parallel: true (default) for concurrent execution
- Use when tasks have no dependencies on each other

**inspect** - Retrieve details from a scratchpad entry
- key: The scratchpad_key returned from a delegate call
- Use to get full context when a summary is insufficient

**set_answer** - Declare your final response
- content (required): The answer text
- ready: true when answer is complete
- confidence: 0.0-1.0 (must exceed threshold shown in context)
- artifacts: Optional list of scratchpad keys for supporting data
- next_steps: Optional suggestions for follow-up actions

## Weight Selection Guide

| Weight    | Use When                                    | Model Tier          |
|-----------|---------------------------------------------|---------------------|
| trivial   | Simple lookups, formatting, single-file reads | Fastest, cheapest  |
| light     | Basic code analysis, small edits            | Fast, affordable    |
| medium    | Multi-file operations, test writing         | Balanced (default)  |
| heavy     | Complex refactoring, architecture decisions | High-quality        |
| reasoning | Deep analysis, planning, debugging          | Extended thinking   |

## Execution Strategy

1. **Decompose**: Break the request into independent sub-tasks when possible
2. **Parallelize**: Use delegate_batch for independent tasks to save time
3. **Sequence**: Use delegate serially when tasks depend on prior results
4. **Synthesize**: Combine sub-agent summaries into a coherent answer
5. **Verify**: Use inspect if a summary lacks critical details

## Budget Awareness
The context shows your remaining tokens and wall time. Plan accordingly:
- If tokens are low, use lighter weights and fewer delegations
- If time is short, parallelize aggressively
- Set partial answers with ready=false if you're running out of budget

## Confidence Calibration
- 0.9+: Fully confident, all sub-tasks succeeded
- 0.7-0.9: Mostly confident, minor gaps acceptable
- 0.5-0.7: Partial answer, flag uncertainties
- <0.5: Incomplete, explain what's missing

## Anti-patterns to Avoid
- Don't delegate trivial work that you could infer from context
- Don't inspect every scratchpad entry - summaries are usually sufficient
- Don't set ready=true until you've synthesized all sub-agent results
- Don't use heavy/reasoning weights for simple tasks (wastes budget)

Remember: You see summaries, not raw output. Trust sub-agents to execute correctly and focus on coordination.`

// IterationEvent captures progress for observers.
type IterationEvent struct {
	Iteration     int
	MaxIterations int
	Ready         bool
	TokensUsed    int
	Summary       string
	Scratchpad    []EntrySummary
}

// IterationHook receives iteration events.
type IterationHook func(event IterationEvent)

// RuntimeDeps provides dependencies for the runtime.
type RuntimeDeps struct {
	Models       *model.Manager
	Store        *storage.Store
	Registry     *tool.Registry
	ToolApprover *security.ToolApprover
	Bus          bus.MessageBus
	Summarizer   func([]byte) string
	Telemetry    *telemetry.Hub
	SessionID    string
	UseToon      bool // Use TOON encoding for compact tool results
}

// Runtime is the RLM execution engine.
type Runtime struct {
	config      Config
	models      *model.Manager
	router      *ModelRouter
	scratchpad  *Scratchpad
	dispatcher  *BatchDispatcher
	conflicts   *ConflictDetector
	approver    *security.ToolApprover
	bus         bus.MessageBus
	telemetry   *telemetry.Hub
	sessionID   string
	resultCodec *toon.Codec // TOON encoding for compact tool results

	hooksMu sync.RWMutex
	hooks   []IterationHook
}

// NewRuntime wires the runtime dependencies together.
func NewRuntime(cfg Config, deps RuntimeDeps) (*Runtime, error) {
	cfg.Normalize()
	if deps.Models == nil {
		return nil, fmt.Errorf("model manager required")
	}

	registry := deps.Registry
	if registry == nil {
		registry = tool.NewRegistry()
	}

	router, err := NewModelRouterFromManager(deps.Models, cfg)
	if err != nil {
		return nil, err
	}

	conflicts := NewConflictDetector()
	scratchpad := NewScratchpad(deps.Store, deps.Summarizer, cfg.Scratchpad)

	dispatcher, err := NewBatchDispatcher(BatchDispatcherConfig{}, BatchDispatcherDeps{
		Router:     router,
		Models:     deps.Models,
		Registry:   registry,
		Scratchpad: scratchpad,
		Conflicts:  conflicts,
		Approver:   deps.ToolApprover,
		Bus:        deps.Bus,
	})
	if err != nil {
		return nil, err
	}

	return &Runtime{
		config:      cfg,
		models:      deps.Models,
		router:      router,
		scratchpad:  scratchpad,
		dispatcher:  dispatcher,
		conflicts:   conflicts,
		approver:    deps.ToolApprover,
		bus:         deps.Bus,
		telemetry:   deps.Telemetry,
		sessionID:   strings.TrimSpace(deps.SessionID),
		resultCodec: toon.New(deps.UseToon),
	}, nil
}

// OnIteration registers a hook for iteration events.
func (r *Runtime) OnIteration(hook IterationHook) {
	if r == nil || hook == nil {
		return
	}
	r.hooksMu.Lock()
	r.hooks = append(r.hooks, hook)
	r.hooksMu.Unlock()
}

// Execute runs the coordinator loop for a task.
func (r *Runtime) Execute(ctx context.Context, task string) (*Answer, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	if strings.TrimSpace(task) == "" {
		return nil, fmt.Errorf("task required")
	}

	start := time.Now()
	answer := NewAnswer(0)
	maxIterations := r.config.Coordinator.MaxIterations
	if maxIterations <= 0 {
		maxIterations = DefaultConfig().Coordinator.MaxIterations
	}
	maxTokens := r.config.Coordinator.MaxTokensBudget
	if maxTokens <= 0 {
		maxTokens = DefaultConfig().Coordinator.MaxTokensBudget
	}
	maxWallTime := r.config.Coordinator.MaxWallTime
	if maxWallTime <= 0 {
		maxWallTime = DefaultConfig().Coordinator.MaxWallTime
	}
	confidenceThreshold := r.config.Coordinator.ConfidenceThreshold
	if confidenceThreshold <= 0 {
		confidenceThreshold = DefaultConfig().Coordinator.ConfidenceThreshold
	}

	runtimeDeadline := false
	if maxWallTime > 0 {
		desired := start.Add(maxWallTime)
		if deadline, ok := ctx.Deadline(); !ok || deadline.After(desired) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, desired)
			defer cancel()
			runtimeDeadline = true
		}
	}

	registry := r.buildCoordinatorRegistry(ctx, &answer)
	toolDefs := toolRegistryDefinitions(registry)
	toolChoice := "auto"
	if len(toolDefs) == 0 {
		toolChoice = "none"
	}

	messages := []model.Message{
		{Role: "system", Content: coordinatorSystemPrompt},
		{Role: "user", Content: r.buildCoordinatorContext(ctx, task, &answer, start, maxTokens, confidenceThreshold)},
	}

	for answer.Iteration < maxIterations && !answer.Ready {
		if err := ctx.Err(); err != nil {
			if err == context.DeadlineExceeded && runtimeDeadline {
				answer.Ready = true
				break
			}
			return &answer, err
		}

		answer.Iteration++

		resp, err := r.models.ChatCompletion(ctx, model.ChatRequest{
			Model:      r.coordinatorModelID(),
			Messages:   messages,
			Tools:      toolDefs,
			ToolChoice: toolChoice,
		})
		if err != nil {
			return &answer, err
		}
		answer.TokensUsed += resp.Usage.TotalTokens

		if len(resp.Choices) == 0 {
			return &answer, fmt.Errorf("no response from coordinator")
		}

		choice := resp.Choices[0]
		if len(choice.Message.ToolCalls) == 0 {
			content := extractText(choice.Message)
			if content != "" {
				answer.Content = strings.TrimSpace(content)
				answer.Ready = true
			}
			summaries := r.collectScratchpadSummaries(ctx, 6)
			r.emitIteration(IterationEvent{
				Iteration:     answer.Iteration,
				MaxIterations: maxIterations,
				Ready:         answer.Ready,
				TokensUsed:    answer.TokensUsed,
				Summary:       answer.Content,
				Scratchpad:    summaries,
			})
			break
		}

		messages = append(messages, model.Message{
			Role:      "assistant",
			Content:   choice.Message.Content,
			ToolCalls: choice.Message.ToolCalls,
		})
		toolResults := r.executeCoordinatorTools(ctx, registry, choice.Message.ToolCalls)
		for _, result := range toolResults {
			messages = append(messages, model.Message{
				Role:       "tool",
				ToolCallID: result.ID,
				Name:       result.Name,
				Content:    result.Result,
			})
		}

		if answer.TokensUsed >= maxTokens {
			answer.Ready = true
		}
		if !answer.Ready && answer.Content != "" && answer.Confidence >= confidenceThreshold {
			answer.Ready = true
		}

		summaries := r.collectScratchpadSummaries(ctx, 6)
		r.emitIteration(IterationEvent{
			Iteration:     answer.Iteration,
			MaxIterations: maxIterations,
			Ready:         answer.Ready,
			TokensUsed:    answer.TokensUsed,
			Summary:       answer.Content,
			Scratchpad:    summaries,
		})

		if !answer.Ready {
			messages = append(messages, model.Message{
				Role:    "user",
				Content: r.buildCoordinatorContext(ctx, task, &answer, start, maxTokens, confidenceThreshold),
			})
		}
	}

	answer.Normalize()
	return &answer, nil
}

func (r *Runtime) coordinatorModelID() string {
	modelID := strings.TrimSpace(r.config.Coordinator.Model)
	if modelID == "" || strings.EqualFold(modelID, "auto") {
		return r.models.GetExecutionModel()
	}
	return modelID
}

func (r *Runtime) buildCoordinatorRegistry(ctx context.Context, answer *Answer) *tool.Registry {
	registry := tool.NewEmptyRegistry()
	ctxProvider := func() context.Context { return ctx }
	registry.Register(NewDelegateTool(r.dispatcher, ctxProvider))
	registry.Register(NewDelegateBatchTool(r.dispatcher, ctxProvider))
	registry.Register(NewInspectTool(r.scratchpad, ctxProvider))
	registry.Register(NewSetAnswerTool(answer))
	return registry
}

func (r *Runtime) buildCoordinatorContext(ctx context.Context, task string, answer *Answer, start time.Time, maxTokens int, confidenceThreshold float64) string {
	var sb strings.Builder
	sb.WriteString("Task:\n")
	sb.WriteString(task)
	sb.WriteString("\n\nAnswer State:\n")
	sb.WriteString(fmt.Sprintf("iteration: %d\nready: %t\nconfidence: %.2f\nconfidence_threshold: %.2f\n", answer.Iteration, answer.Ready, answer.Confidence, confidenceThreshold))
	if answer.Content != "" {
		sb.WriteString("content: ")
		sb.WriteString(answer.Content)
		sb.WriteString("\n")
	}
	sb.WriteString("\nBudget:\n")
	sb.WriteString(fmt.Sprintf("tokens_used: %d\nmax_tokens: %d\n", answer.TokensUsed, maxTokens))
	if maxTokens > 0 {
		remaining := maxTokens - answer.TokensUsed
		if remaining < 0 {
			remaining = 0
		}
		sb.WriteString(fmt.Sprintf("tokens_remaining: %d\n", remaining))
	}
	deadline := time.Time{}
	if ctx != nil {
		if d, ok := ctx.Deadline(); ok {
			deadline = d
		}
	}
	if deadline.IsZero() {
		maxWallTime := r.config.Coordinator.MaxWallTime
		if maxWallTime <= 0 {
			maxWallTime = DefaultConfig().Coordinator.MaxWallTime
		}
		if maxWallTime > 0 {
			deadline = start.Add(maxWallTime)
		}
	}
	if !deadline.IsZero() {
		remaining := time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		sb.WriteString(fmt.Sprintf("wall_time_remaining: %s\n", remaining.Round(time.Second)))
	}

	summaries, err := r.scratchpad.ListSummaries(ctx, 8)
	if err == nil && len(summaries) > 0 {
		sb.WriteString("\nScratchpad summaries:\n")
		for _, summary := range summaries {
			sb.WriteString("- ")
			sb.WriteString(summary.Key)
			sb.WriteString(" [")
			sb.WriteString(string(summary.Type))
			sb.WriteString("]: ")
			sb.WriteString(summary.Summary)
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

func (r *Runtime) collectScratchpadSummaries(ctx context.Context, limit int) []EntrySummary {
	if r == nil || r.scratchpad == nil {
		return nil
	}
	summaries, err := r.scratchpad.ListSummaries(ctx, limit)
	if err != nil {
		return nil
	}
	return summaries
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
		res, err := registry.Execute(name, args)
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
	r.hooksMu.RLock()
	hooks := append([]IterationHook{}, r.hooks...)
	r.hooksMu.RUnlock()
	for _, hook := range hooks {
		hook(event)
	}
	if r.telemetry != nil {
		data := map[string]any{
			"iteration":      event.Iteration,
			"max_iterations": event.MaxIterations,
			"ready":          event.Ready,
			"tokens_used":    event.TokensUsed,
			"summary":        event.Summary,
		}
		if len(event.Scratchpad) > 0 {
			data["scratchpad"] = formatScratchpadSummaries(event.Scratchpad)
		}
		r.telemetry.Publish(telemetry.Event{
			Type:      telemetry.EventRLMIteration,
			SessionID: r.sessionID,
			Data:      data,
		})
	}
}

type coordinatorToolResult struct {
	ID     string
	Name   string
	Result string
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
	// Use TOON encoding for compact token-efficient results
	codec := r.resultCodec
	if codec == nil {
		codec = toon.New(true) // Default to TOON
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
