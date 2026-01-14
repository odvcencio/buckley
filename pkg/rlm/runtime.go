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
	"github.com/oklog/ulid/v2"
)

const coordinatorSystemPrompt = `You are the Buckley RLM Coordinator - an orchestration layer that delegates work to sub-agents while maintaining strategic oversight.

## Your Role
You do NOT execute tools directly. Instead, you:
1. Break down the user's request into discrete sub-tasks
2. Delegate each sub-task to a sub-agent
3. Review summaries and scratchpad entries to synthesize results
4. Set the final answer when confident

## Available Tools

**delegate** - Dispatch a single task to a sub-agent
- task (required): Clear, actionable instruction for the sub-agent
- tools: Optional list of allowed tools (nil = all tools)
- Returns: summary, scratchpad_key, model used

**delegate_batch** - Dispatch multiple independent tasks in parallel
- tasks: Array of {task, tools} objects
- parallel: true (default) for concurrent execution
- Use when tasks have no dependencies on each other

**inspect** - Retrieve details from a scratchpad entry
- key: The scratchpad_key returned from a delegate call
- Use to get full context when a summary is insufficient

**record_strategy** - Persist strategic decisions for future reference
- category: decomposition|approach|lesson_learned
- summary: Brief description (shown in context on future iterations)
- details: Full details of the decision
- rationale: Why this strategy was chosen

**search_scratchpad** - Semantically search past work (if RAG is enabled)
- query: Natural language description of what you're looking for
- type: Optional filter (file, command, analysis, decision, artifact, strategy)
- limit: Max results (default 5)

**set_answer** - Declare your final response
- content (required): The answer text
- ready: true when answer is complete
- confidence: 0.0-1.0 (must exceed threshold shown in context)
- artifacts: Optional list of scratchpad keys for supporting data
- next_steps: Optional suggestions for follow-up actions

## Execution Strategy

1. **Decompose**: Break the request into independent sub-tasks when possible
2. **Parallelize**: Use delegate_batch for independent tasks to save time
3. **Sequence**: Use delegate serially when tasks depend on prior results
4. **Synthesize**: Combine sub-agent summaries into a coherent answer
5. **Verify**: Use inspect if a summary lacks critical details

## Budget Awareness
The context shows your remaining tokens and wall time. Plan accordingly:
- If budget is low, reduce parallelism and focus on essentials
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

Remember: You see summaries, not raw output. Trust sub-agents to execute correctly and focus on coordination.`

// IterationEvent captures progress for observers.
type IterationEvent struct {
	Iteration      int
	MaxIterations  int
	Ready          bool
	TokensUsed     int
	MaxTokens      int
	Summary        string
	Scratchpad     []EntrySummary
	ReasoningTrace string           // Coordinator's reasoning for this iteration
	Delegations    []DelegationInfo // Tasks delegated this iteration
	BudgetStatus   BudgetStatus     // Token/time budget status
}

// DelegationInfo captures info about a delegated task.
type DelegationInfo struct {
	TaskID         string `json:"task_id"`
	Weight         string `json:"weight"`
	WeightUsed     string `json:"weight_used,omitempty"`
	Model          string `json:"model,omitempty"`
	Escalated      bool   `json:"escalated,omitempty"`
	Summary        string `json:"summary,omitempty"`
	Success        bool   `json:"success"`
	ToolCallsCount int    `json:"tool_calls_count,omitempty"`
}

// BudgetStatus tracks resource consumption.
type BudgetStatus struct {
	TokensUsed      int     `json:"tokens_used"`
	TokensMax       int     `json:"tokens_max"`
	TokensRemaining int     `json:"tokens_remaining"`
	TokensPercent   float64 `json:"tokens_percent"`
	WallTimeElapsed string  `json:"wall_time_elapsed"`
	WallTimeMax     string  `json:"wall_time_max"`
	WallTimePercent float64 `json:"wall_time_percent"`
	Warning         string  `json:"warning,omitempty"` // e.g., "tokens_low", "time_low"
}

// IterationHistory tracks past iterations for context.
type IterationHistory struct {
	Iteration   int              `json:"iteration"`
	Delegations []DelegationInfo `json:"delegations,omitempty"`
	Summary     string           `json:"summary,omitempty"`
	TokensUsed  int              `json:"tokens_used"`
	Compacted   bool             `json:"compacted,omitempty"` // True if this is a compacted summary
}

// CompactionConfig controls context compaction behavior.
type CompactionConfig struct {
	MaxHistoryItems    int // Max items before compaction (default 6)
	CompactedBatchSize int // Batch size for compaction (default 3)
}

// Checkpoint captures the state of an RLM execution for resumption.
type Checkpoint struct {
	ID         string             `json:"id"`
	Task       string             `json:"task"`
	Answer     Answer             `json:"answer"`
	History    []IterationHistory `json:"history,omitempty"`
	CreatedAt  time.Time          `json:"created_at"`
	ResumedAt  time.Time          `json:"resumed_at,omitempty"`
	Scratchpad []string           `json:"scratchpad_keys,omitempty"` // Keys to restore
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
	UseToon      bool              // Use TOON encoding for compact tool results
	Embedder     EmbeddingProvider // Optional embedder for RAG-based scratchpad search
}

// Runtime is the RLM execution engine.
type Runtime struct {
	config        Config
	models        *model.Manager
	selector      *ModelSelector
	scratchpad    *Scratchpad
	scratchpadRAG *ScratchpadRAG // Optional RAG-based search
	dispatcher    *Dispatcher
	conflicts     *ConflictDetector
	approver      *security.ToolApprover
	bus           bus.MessageBus
	telemetry     *telemetry.Hub
	sessionID     string
	resultCodec   *toon.Codec // TOON encoding for compact tool results

	hooksMu sync.RWMutex
	hooks   []IterationHook

	// Iteration tracking for context
	historyMu sync.RWMutex
	history   []IterationHistory
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

	selector := NewModelSelector(cfg, deps.Models)
	conflicts := NewConflictDetector()
	scratchpad := NewScratchpad(deps.Store, deps.Summarizer, cfg.Scratchpad)

	dispatcher, err := NewDispatcher(DispatcherConfig{
		MaxConcurrent: cfg.SubAgent.MaxConcurrent,
		Timeout:       cfg.SubAgent.Timeout,
	}, DispatcherDeps{
		Selector:   selector,
		Models:     deps.Models,
		Registry:   registry,
		Scratchpad: scratchpad,
		Conflicts:  conflicts,
		Approver:   deps.ToolApprover,
		Bus:        deps.Bus,
		Telemetry:  deps.Telemetry,
	})
	if err != nil {
		return nil, err
	}

	runtime := &Runtime{
		config:      cfg,
		models:      deps.Models,
		selector:    selector,
		scratchpad:  scratchpad,
		dispatcher:  dispatcher,
		conflicts:   conflicts,
		approver:    deps.ToolApprover,
		bus:         deps.Bus,
		telemetry:   deps.Telemetry,
		sessionID:   strings.TrimSpace(deps.SessionID),
		resultCodec: toon.New(deps.UseToon),
	}

	// Initialize RAG-based scratchpad search if embedder is provided
	if deps.Embedder != nil {
		runtime.scratchpadRAG = NewScratchpadRAG(scratchpad, deps.Embedder)
	}

	return runtime, nil
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

// CreateCheckpoint captures the current execution state for later resumption.
func (r *Runtime) CreateCheckpoint(task string, answer *Answer) (*Checkpoint, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	if answer == nil {
		return nil, fmt.Errorf("answer is nil")
	}

	r.historyMu.RLock()
	history := make([]IterationHistory, len(r.history))
	copy(history, r.history)
	r.historyMu.RUnlock()

	// Collect scratchpad keys
	var scratchpadKeys []string
	if r.scratchpad != nil {
		summaries, err := r.scratchpad.ListSummaries(context.Background(), 50)
		if err == nil {
			for _, s := range summaries {
				scratchpadKeys = append(scratchpadKeys, s.Key)
			}
		}
	}

	checkpoint := &Checkpoint{
		ID:         ulid.Make().String(),
		Task:       task,
		Answer:     *answer,
		History:    history,
		CreatedAt:  time.Now().UTC(),
		Scratchpad: scratchpadKeys,
	}

	return checkpoint, nil
}

// ResumeFromCheckpoint restores state from a checkpoint and continues execution.
func (r *Runtime) ResumeFromCheckpoint(ctx context.Context, checkpoint *Checkpoint) (*Answer, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	if checkpoint == nil {
		return nil, fmt.Errorf("checkpoint is nil")
	}

	// Restore history
	r.historyMu.Lock()
	r.history = make([]IterationHistory, len(checkpoint.History))
	copy(r.history, checkpoint.History)
	r.historyMu.Unlock()

	// Update checkpoint with resume time
	checkpoint.ResumedAt = time.Now().UTC()

	// Emit resume event
	if r.telemetry != nil {
		r.telemetry.Publish(telemetry.Event{
			Type:      telemetry.EventType("rlm.resumed"),
			SessionID: r.sessionID,
			Data: map[string]any{
				"checkpoint_id": checkpoint.ID,
				"task":          checkpoint.Task,
				"iteration":     checkpoint.Answer.Iteration,
				"tokens_used":   checkpoint.Answer.TokensUsed,
			},
		})
	}

	// Continue execution from where we left off
	return r.executeFromState(ctx, checkpoint.Task, &checkpoint.Answer)
}

// executeFromState continues execution from a given state.
func (r *Runtime) executeFromState(ctx context.Context, task string, answer *Answer) (*Answer, error) {
	if answer == nil {
		a := NewAnswer(0)
		answer = &a
	}
	if answer.Ready {
		return answer, nil
	}

	start := time.Now()
	maxIterations := r.config.Coordinator.MaxIterations
	if maxIterations <= 0 {
		maxIterations = DefaultConfig().Coordinator.MaxIterations
	}
	maxTokens := r.config.Coordinator.MaxTokensBudget // 0 = unlimited
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

	registry := r.buildCoordinatorRegistry(ctx, answer)
	toolDefs := toolRegistryDefinitions(registry)
	toolChoice := "auto"
	if len(toolDefs) == 0 {
		toolChoice = "none"
	}

	// Build initial context with resumption note
	resumeNote := ""
	if answer.Iteration > 0 {
		resumeNote = fmt.Sprintf("\n[Resumed from iteration %d with %d tokens used]\n", answer.Iteration, answer.TokensUsed)
	}

	messages := []model.Message{
		{Role: "system", Content: coordinatorSystemPrompt},
		{Role: "user", Content: resumeNote + r.buildCoordinatorContext(ctx, task, answer, start, maxTokens, confidenceThreshold)},
	}

	for answer.Iteration < maxIterations && !answer.Ready {
		if err := ctx.Err(); err != nil {
			if err == context.DeadlineExceeded && runtimeDeadline {
				answer.Ready = true
				break
			}
			return answer, err
		}

		answer.Iteration++

		resp, err := r.models.ChatCompletion(ctx, model.ChatRequest{
			Model:      r.coordinatorModelID(),
			Messages:   messages,
			Tools:      toolDefs,
			ToolChoice: toolChoice,
		})
		if err != nil {
			return answer, err
		}
		answer.TokensUsed += resp.Usage.TotalTokens

		if len(resp.Choices) == 0 {
			return answer, fmt.Errorf("no response from coordinator")
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
				Iteration:      answer.Iteration,
				MaxIterations:  maxIterations,
				Ready:          answer.Ready,
				TokensUsed:     answer.TokensUsed,
				MaxTokens:      maxTokens,
				Summary:        answer.Content,
				Scratchpad:     summaries,
				ReasoningTrace: content,
				BudgetStatus:   r.calculateBudgetStatus(answer.TokensUsed, maxTokens, start),
			})
			break
		}

		reasoningTrace := extractText(choice.Message)

		messages = append(messages, model.Message{
			Role:      "assistant",
			Content:   choice.Message.Content,
			ToolCalls: choice.Message.ToolCalls,
		})
		toolResults := r.executeCoordinatorTools(ctx, registry, choice.Message.ToolCalls)
		delegations := r.extractDelegationsFromToolResults(toolResults)

		for _, result := range toolResults {
			messages = append(messages, model.Message{
				Role:       "tool",
				ToolCallID: result.ID,
				Name:       result.Name,
				Content:    result.Result,
			})
		}

		if maxTokens > 0 && answer.TokensUsed >= maxTokens {
			answer.Ready = true
		}
		if !answer.Ready && answer.Content != "" && answer.Confidence >= confidenceThreshold {
			answer.Ready = true
		}

		summaries := r.collectScratchpadSummaries(ctx, 6)
		r.emitIteration(IterationEvent{
			Iteration:      answer.Iteration,
			MaxIterations:  maxIterations,
			Ready:          answer.Ready,
			TokensUsed:     answer.TokensUsed,
			MaxTokens:      maxTokens,
			Summary:        answer.Content,
			Scratchpad:     summaries,
			ReasoningTrace: reasoningTrace,
			Delegations:    delegations,
			BudgetStatus:   r.calculateBudgetStatus(answer.TokensUsed, maxTokens, start),
		})

		if !answer.Ready {
			messages = append(messages, model.Message{
				Role:    "user",
				Content: r.buildCoordinatorContext(ctx, task, answer, start, maxTokens, confidenceThreshold),
			})
		}
	}

	answer.Normalize()
	return answer, nil
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
	maxTokens := r.config.Coordinator.MaxTokensBudget // 0 = unlimited
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
				Iteration:      answer.Iteration,
				MaxIterations:  maxIterations,
				Ready:          answer.Ready,
				TokensUsed:     answer.TokensUsed,
				MaxTokens:      maxTokens,
				Summary:        answer.Content,
				Scratchpad:     summaries,
				ReasoningTrace: content,
				BudgetStatus:   r.calculateBudgetStatus(answer.TokensUsed, maxTokens, start),
			})
			break
		}

		// Extract reasoning trace from coordinator message (text before tool calls)
		reasoningTrace := extractText(choice.Message)

		messages = append(messages, model.Message{
			Role:      "assistant",
			Content:   choice.Message.Content,
			ToolCalls: choice.Message.ToolCalls,
		})
		toolResults := r.executeCoordinatorTools(ctx, registry, choice.Message.ToolCalls)

		// Track delegations for transparency
		delegations := r.extractDelegationsFromToolResults(toolResults)

		for _, result := range toolResults {
			messages = append(messages, model.Message{
				Role:       "tool",
				ToolCallID: result.ID,
				Name:       result.Name,
				Content:    result.Result,
			})
		}

		if maxTokens > 0 && answer.TokensUsed >= maxTokens {
			answer.Ready = true
		}
		if !answer.Ready && answer.Content != "" && answer.Confidence >= confidenceThreshold {
			answer.Ready = true
		}

		summaries := r.collectScratchpadSummaries(ctx, 6)
		r.emitIteration(IterationEvent{
			Iteration:      answer.Iteration,
			MaxIterations:  maxIterations,
			Ready:          answer.Ready,
			TokensUsed:     answer.TokensUsed,
			MaxTokens:      maxTokens,
			Summary:        answer.Content,
			Scratchpad:     summaries,
			ReasoningTrace: reasoningTrace,
			Delegations:    delegations,
			BudgetStatus:   r.calculateBudgetStatus(answer.TokensUsed, maxTokens, start),
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
	registry.Register(NewRecordStrategyTool(r.scratchpad, ctxProvider))

	// Register RAG-based search if available
	if r.scratchpadRAG != nil {
		registry.Register(NewSearchScratchpadTool(r.scratchpadRAG, ctxProvider))
	}

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

	// Enhanced budget section with visual indicators
	sb.WriteString("\nBudget:\n")
	tokenPercent := 0.0
	if maxTokens > 0 {
		tokenPercent = float64(answer.TokensUsed) / float64(maxTokens) * 100
		remaining := maxTokens - answer.TokensUsed
		if remaining < 0 {
			remaining = 0
		}
		sb.WriteString(fmt.Sprintf("tokens: %d/%d (%.1f%% used, %d remaining)\n", answer.TokensUsed, maxTokens, tokenPercent, remaining))

		// Budget warnings
		if tokenPercent >= 90 {
			sb.WriteString("⚠️ CRITICAL: Token budget nearly exhausted - wrap up immediately\n")
		} else if tokenPercent >= 75 {
			sb.WriteString("⚠️ WARNING: Token budget running low - prioritize completion\n")
		}
	} else {
		sb.WriteString(fmt.Sprintf("tokens_used: %d\n", answer.TokensUsed))
	}

	// Time budget
	deadline := time.Time{}
	maxWallTime := r.config.Coordinator.MaxWallTime
	if maxWallTime <= 0 {
		maxWallTime = DefaultConfig().Coordinator.MaxWallTime
	}
	if ctx != nil {
		if d, ok := ctx.Deadline(); ok {
			deadline = d
		}
	}
	if deadline.IsZero() && maxWallTime > 0 {
		deadline = start.Add(maxWallTime)
	}
	if !deadline.IsZero() {
		elapsed := time.Since(start)
		remaining := time.Until(deadline)
		if remaining < 0 {
			remaining = 0
		}
		total := elapsed + remaining
		timePercent := 0.0
		if total > 0 {
			timePercent = float64(elapsed) / float64(total) * 100
		}
		sb.WriteString(fmt.Sprintf("time: %s elapsed, %s remaining (%.1f%% used)\n", elapsed.Round(time.Second), remaining.Round(time.Second), timePercent))

		if remaining < time.Minute {
			sb.WriteString("⚠️ CRITICAL: Less than 1 minute remaining - finalize now\n")
		} else if remaining < 2*time.Minute {
			sb.WriteString("⚠️ WARNING: Time running low - plan to complete soon\n")
		}
	}

	// Iteration history for learning from past attempts (with compaction)
	r.historyMu.RLock()
	history := make([]IterationHistory, len(r.history))
	copy(history, r.history)
	r.historyMu.RUnlock()

	if len(history) > 0 {
		sb.WriteString("\nIteration History (for context):\n")
		for _, h := range history {
			if h.Compacted {
				// Compacted entry - show condensed summary
				sb.WriteString(fmt.Sprintf("- %s", h.Summary))
				sb.WriteString(fmt.Sprintf(" [%d tokens]\n", h.TokensUsed))
			} else {
				// Regular entry - show full details
				sb.WriteString(fmt.Sprintf("- Iteration %d: ", h.Iteration))
				if len(h.Delegations) > 0 {
					var delegSummaries []string
					for _, d := range h.Delegations {
						status := "✓"
						if !d.Success {
							status = "✗"
						}
						if d.Model != "" {
							delegSummaries = append(delegSummaries, fmt.Sprintf("%s(%s)%s", d.Weight, d.Model, status))
						} else if d.Summary != "" {
							delegSummaries = append(delegSummaries, d.Summary)
						}
					}
					if len(delegSummaries) > 0 {
						sb.WriteString(strings.Join(delegSummaries, ", "))
					}
				}
				if h.Summary != "" {
					if len(h.Summary) > 100 {
						sb.WriteString(fmt.Sprintf(" → %s...", h.Summary[:100]))
					} else {
						sb.WriteString(fmt.Sprintf(" → %s", h.Summary))
					}
				}
				sb.WriteString(fmt.Sprintf(" [%d tokens]\n", h.TokensUsed))
			}
		}
	}

	// Scratchpad summaries
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
	// Record in history for context learning
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
		// Add budget status
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

		// Emit budget warning if needed
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
	// Compact history when it grows too large
	r.compactHistoryLocked()
	r.historyMu.Unlock()
}

// compactHistoryLocked summarizes old iterations to save context space.
// Must be called with historyMu held.
func (r *Runtime) compactHistoryLocked() {
	maxItems := r.config.Coordinator.HistoryMaxItems
	compactBatch := r.config.Coordinator.HistoryCompactN
	keepRecent := r.config.Coordinator.HistoryKeepRecent

	// Use defaults if not configured
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

	// Don't compact if we don't have enough items to make it worthwhile
	compactableCount := len(r.history) - keepRecent
	if compactableCount < compactBatch {
		return
	}

	// Take the oldest non-recent items and compact them
	toCompact := r.history[:compactBatch]
	remaining := r.history[compactBatch:]

	// Build a compacted summary
	var totalTokens int
	var allDelegations []DelegationInfo
	var summaryParts []string
	iterations := make([]int, 0, len(toCompact))

	for _, h := range toCompact {
		iterations = append(iterations, h.Iteration)
		totalTokens += h.TokensUsed
		allDelegations = append(allDelegations, h.Delegations...)
		if h.Summary != "" {
			// Truncate individual summaries in compacted form
			summary := h.Summary
			if len(summary) > 50 {
				summary = summary[:50] + "..."
			}
			summaryParts = append(summaryParts, summary)
		}
	}

	// Create compacted entry
	compacted := IterationHistory{
		Iteration:  iterations[0], // Use first iteration number
		Compacted:  true,
		TokensUsed: totalTokens,
		Summary:    fmt.Sprintf("[Compacted iterations %d-%d] %s", iterations[0], iterations[len(iterations)-1], strings.Join(summaryParts, "; ")),
	}

	// Summarize delegations (just counts)
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

	// Rebuild history with compacted entry at the start
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

	// Determine warning level
	if status.TokensPercent >= 90 || status.WallTimePercent >= 90 {
		status.Warning = "critical"
	} else if status.TokensPercent >= 75 || status.WallTimePercent >= 75 {
		status.Warning = "low"
	}

	return status
}

type coordinatorToolResult struct {
	ID     string
	Name   string
	Result string
}

// extractDelegationsFromToolResults parses delegation info from tool results for transparency.
func (r *Runtime) extractDelegationsFromToolResults(results []coordinatorToolResult) []DelegationInfo {
	var delegations []DelegationInfo

	for _, result := range results {
		if result.Name != "delegate" && result.Name != "delegate_batch" {
			continue
		}

		// Parse the JSON result to extract delegation info
		var parsed map[string]any
		if err := json.Unmarshal([]byte(result.Result), &parsed); err != nil {
			continue
		}

		// Handle single delegate result
		if data, ok := parsed["data"].(map[string]any); ok {
			deleg := extractDelegationFromData(data)
			if deleg.TaskID != "" || deleg.Weight != "" {
				delegations = append(delegations, deleg)
			}
		}

		// Handle batch results
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
