package rlm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/coordination/security"
	"github.com/odvcencio/buckley/pkg/encoding/toon"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
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

const (
	coordinatorMaxMessages  = 40
	coordinatorKeepMessages = 12
)

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

	contextMu              sync.Mutex
	historySnapshotHash    uint64
	scratchpadSnapshotHash uint64
	historySnapshotSet     bool
	scratchpadSnapshotSet  bool

	// Async compaction channel
	compactionCh chan struct{}
	compactionWg sync.WaitGroup
	closeOnce    sync.Once

	// Token counting cache
	tokenCacheMu sync.RWMutex
	tokenCache   map[string]int

	// Rate limiting for iterations
	lastIterationTime atomic.Int64 // Unix nanoseconds
	minIterationDelay time.Duration

	// Model call timeout
	modelCallTimeout time.Duration
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
		config:            cfg,
		models:            deps.Models,
		selector:          selector,
		scratchpad:        scratchpad,
		dispatcher:        dispatcher,
		conflicts:         conflicts,
		approver:          deps.ToolApprover,
		bus:               deps.Bus,
		telemetry:         deps.Telemetry,
		sessionID:         strings.TrimSpace(deps.SessionID),
		resultCodec:       toon.New(deps.UseToon),
		compactionCh:      make(chan struct{}, 1),
		tokenCache:        make(map[string]int),
		minIterationDelay: 10 * time.Millisecond, // Minimum delay between iterations
		modelCallTimeout:  2 * time.Minute,       // Default timeout for model calls
	}

	// Initialize RAG-based scratchpad search if embedder is provided
	if deps.Embedder != nil {
		runtime.scratchpadRAG = NewScratchpadRAG(scratchpad, deps.Embedder)
		scratchpad.SetOnWrite(runtime.scratchpadRAG.OnWrite)
	}

	// Start async compaction worker
	runtime.compactionWg.Add(1)
	go runtime.compactionWorker()

	return runtime, nil
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
	baseConfidenceThreshold := r.config.Coordinator.ConfidenceThreshold
	if baseConfidenceThreshold <= 0 {
		baseConfidenceThreshold = DefaultConfig().Coordinator.ConfidenceThreshold
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

	budgetStatus := r.calculateBudgetStatus(answer.TokensUsed, maxTokens, start)
	confidenceThreshold := r.adaptiveConfidenceThreshold(baseConfidenceThreshold, budgetStatus)

	messages := []model.Message{
		{Role: "system", Content: coordinatorSystemPrompt},
		{Role: "user", Content: resumeNote + r.buildCoordinatorContext(ctx, task, answer, start, maxTokens, confidenceThreshold)},
	}

	for answer.Iteration < maxIterations && !answer.Ready {
		// Check context cancellation with early exit
		if err := ctx.Err(); err != nil {
			if err == context.DeadlineExceeded && runtimeDeadline {
				answer.Ready = true
				break
			}
			return answer, err
		}

		// Rate limiting between iterations
		if !r.rateLimitIteration(ctx) {
			if err := ctx.Err(); err != nil {
				if err == context.DeadlineExceeded && runtimeDeadline {
					answer.Ready = true
					break
				}
				return answer, err
			}
		}

		answer.Iteration++

		// Create timeout context for model call
		modelCtx, modelCancel := context.WithTimeout(ctx, r.modelCallTimeout)
		resp, err := r.models.ChatCompletion(modelCtx, model.ChatRequest{
			Model:      r.coordinatorModelID(),
			Messages:   messages,
			Tools:      toolDefs,
			ToolChoice: toolChoice,
		})
		modelCancel()

		// Handle timeout specifically
		if err != nil {
			if ctx.Err() != nil {
				// Parent context cancelled
				if ctx.Err() == context.DeadlineExceeded && runtimeDeadline {
					answer.Ready = true
					break
				}
				return answer, ctx.Err()
			}
			// Model call timeout or other error
			return answer, fmt.Errorf("model call failed: %w", err)
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
			budgetStatus := r.calculateBudgetStatus(answer.TokensUsed, maxTokens, start)
			r.emitIteration(IterationEvent{
				Iteration:      answer.Iteration,
				MaxIterations:  maxIterations,
				Ready:          answer.Ready,
				TokensUsed:     answer.TokensUsed,
				MaxTokens:      maxTokens,
				Summary:        answer.Content,
				Scratchpad:     summaries,
				ReasoningTrace: content,
				BudgetStatus:   budgetStatus,
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

		budgetStatus := r.calculateBudgetStatus(answer.TokensUsed, maxTokens, start)
		confidenceThreshold = r.adaptiveConfidenceThreshold(baseConfidenceThreshold, budgetStatus)
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
			BudgetStatus:   budgetStatus,
		})

		if !answer.Ready {
			messages = append(messages, model.Message{
				Role:    "user",
				Content: r.buildCoordinatorContext(ctx, task, answer, start, maxTokens, confidenceThreshold),
			})
			messages = r.compactMessages(messages)
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

	answer := NewAnswer(0)
	return r.executeFromState(ctx, task, &answer)
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
		if r.shouldIncludeHistory(history) {
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
	}

	// Scratchpad summaries
	summaries, err := r.scratchpad.ListSummaries(ctx, 8)
	if err == nil && len(summaries) > 0 && r.shouldIncludeScratchpad(summaries) {
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
