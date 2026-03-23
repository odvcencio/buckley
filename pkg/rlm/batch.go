package rlm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/coordination/reliability"
	"github.com/odvcencio/buckley/pkg/coordination/security"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/rules"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/oklog/ulid/v2"
	"golang.org/x/time/rate"
)

const defaultBatchConcurrency = 4

// SubTask is a single delegated task.
type SubTask struct {
	ID            string
	Prompt        string
	Weight        Weight
	AllowedTools  []string
	SystemPrompt  string
	MaxIterations int
}

// BatchRequest describes a batch of sub-agent tasks.
type BatchRequest struct {
	Tasks    []SubTask
	Parallel bool
}

// BatchResult captures the outcome of a dispatched task.
type BatchResult struct {
	TaskID     string
	AgentID    string
	ModelUsed  string
	Summary    string
	RawKey     string
	TokensUsed int
	Duration   time.Duration
	Error      string
}

// BatchDispatcher runs sub-agents with optional concurrency control.
type BatchDispatcher struct {
	router      *ModelRouter
	models      *model.Manager
	registry    *tool.Registry
	scratchpad  ScratchpadWriter
	conflicts   *ConflictDetector
	approver    *security.ToolApprover
	rateLimiter *rate.Limiter
	semaphore   chan struct{}
	breaker     *reliability.CircuitBreaker
	bus         bus.MessageBus
	engine      *rules.Engine
}

// BatchDispatcherConfig configures dispatcher behavior.
type BatchDispatcherConfig struct {
	MaxConcurrent int
	RateLimit     rate.Limit
	Burst         int
	Circuit       reliability.CircuitBreakerConfig
}

// BatchDispatcherDeps supplies dependencies for dispatcher creation.
type BatchDispatcherDeps struct {
	Router     *ModelRouter
	Models     *model.Manager
	Registry   *tool.Registry
	Scratchpad ScratchpadWriter
	Conflicts  *ConflictDetector
	Approver   *security.ToolApprover
	Bus        bus.MessageBus
	Engine     *rules.Engine
}

// NewBatchDispatcher creates a dispatcher with the given configuration.
func NewBatchDispatcher(cfg BatchDispatcherConfig, deps BatchDispatcherDeps) (*BatchDispatcher, error) {
	if deps.Router == nil {
		return nil, fmt.Errorf("model router required")
	}
	if deps.Models == nil {
		return nil, fmt.Errorf("model manager required")
	}
	if deps.Registry == nil {
		return nil, fmt.Errorf("tool registry required")
	}

	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = defaultBatchConcurrency
	}
	burst := cfg.Burst
	if burst <= 0 {
		burst = maxConcurrent
	}
	limiter := rate.NewLimiter(cfg.RateLimit, burst)
	if cfg.RateLimit <= 0 {
		limiter = nil
	}

	breaker := reliability.NewCircuitBreaker(cfg.Circuit)

	return &BatchDispatcher{
		router:      deps.Router,
		models:      deps.Models,
		registry:    deps.Registry,
		scratchpad:  deps.Scratchpad,
		conflicts:   deps.Conflicts,
		approver:    deps.Approver,
		rateLimiter: limiter,
		semaphore:   make(chan struct{}, maxConcurrent),
		breaker:     breaker,
		bus:         deps.Bus,
		engine:      deps.Engine,
	}, nil
}

// Execute runs the batch and returns results in input order.
func (d *BatchDispatcher) Execute(ctx context.Context, req BatchRequest) ([]BatchResult, error) {
	if len(req.Tasks) == 0 {
		return nil, nil
	}
	if !req.Parallel || len(req.Tasks) == 1 {
		return d.executeSequential(ctx, req.Tasks)
	}
	return d.executeParallel(ctx, req.Tasks)
}

func (d *BatchDispatcher) executeSequential(ctx context.Context, tasks []SubTask) ([]BatchResult, error) {
	results := make([]BatchResult, 0, len(tasks))
	var combinedErr error
	for _, task := range tasks {
		res, err := d.executeTask(ctx, task)
		results = append(results, res)
		if err != nil {
			combinedErr = errors.Join(combinedErr, err)
		}
	}
	return results, combinedErr
}

func (d *BatchDispatcher) executeParallel(ctx context.Context, tasks []SubTask) ([]BatchResult, error) {
	results := make([]BatchResult, len(tasks))
	var mu sync.Mutex
	var combinedErr error
	wg := sync.WaitGroup{}

	for idx, task := range tasks {
		idx := idx
		task := task
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d.semaphore != nil {
				d.semaphore <- struct{}{}
				defer func() { <-d.semaphore }()
			}
			res, err := d.executeTask(ctx, task)
			mu.Lock()
			results[idx] = res
			if err != nil {
				combinedErr = errors.Join(combinedErr, err)
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	return results, combinedErr
}

func (d *BatchDispatcher) executeTask(ctx context.Context, task SubTask) (BatchResult, error) {
	res := BatchResult{TaskID: task.ID}
	if strings.TrimSpace(task.Prompt) == "" {
		res.Error = "task prompt required"
		return res, fmt.Errorf("task prompt required")
	}
	if res.TaskID == "" {
		res.TaskID = ulid.Make().String()
	}

	weight := task.Weight
	if strings.TrimSpace(string(weight)) == "" {
		weight = WeightMedium
	}
	maxIterations := task.MaxIterations
	toolTier := ""

	// Evaluate spawning rules to override weight, iterations, and tool tier.
	if d.engine != nil {
		matched, evalErr := rules.Eval(d.engine, "spawning", rules.SpawningFacts{
			TaskType:  inferTaskType(task.Prompt),
			FileCount: countPromptFiles(task.Prompt),
		})
		if evalErr == nil && len(matched) > 0 {
			if w, ok := matched[0].Params["weight"].(string); ok {
				if parsed := weightFromString(w); parsed != "" {
					weight = parsed
				}
			}
			if mi, ok := matched[0].Params["max_iterations"].(float64); ok && int(mi) > 0 {
				maxIterations = int(mi)
			}
			if tt, ok := matched[0].Params["tool_tier"].(string); ok {
				toolTier = tt
			}
		}
	}

	modelID, err := d.router.Select(weight)
	if err != nil {
		res.Error = err.Error()
		return res, err
	}
	res.ModelUsed = modelID

	if d.rateLimiter != nil {
		if err := d.rateLimiter.Wait(ctx); err != nil {
			res.Error = err.Error()
			return res, err
		}
	}

	agentID := fmt.Sprintf("rlm-%s", res.TaskID)
	if maxIterations <= 0 {
		maxIterations = task.MaxIterations
	}
	allowedTools := task.AllowedTools
	if d.engine != nil && toolTier != "" {
		if filtered := d.filterToolsByTier(toolTier, allowedTools); filtered != nil {
			allowedTools = filtered
		}
	}
	agent, err := NewSubAgent(SubAgentConfig{
		ID:            agentID,
		Model:         modelID,
		SystemPrompt:  task.SystemPrompt,
		MaxIterations: maxIterations,
		AllowedTools:  allowedTools,
	}, SubAgentDeps{
		Models:     d.models,
		Registry:   d.registry,
		Scratchpad: d.scratchpad,
		Conflicts:  d.conflicts,
		Approver:   d.approver,
	})
	if err != nil {
		res.Error = err.Error()
		return res, err
	}

	const maxEscalationAttempts = 4
	for attempt := 1; attempt <= maxEscalationAttempts; attempt++ {
		run := func() error {
			start := time.Now()
			if d.bus != nil {
				d.publishEvent(ctx, "buckley.rlm.task.started", map[string]any{
					"task_id":  res.TaskID,
					"agent_id": agentID,
					"model":    modelID,
				})
			}
			execResult, execErr := agent.Execute(ctx, task.Prompt)
			res.Duration = time.Since(start)
			if execResult != nil {
				res.AgentID = execResult.AgentID
				res.Summary = execResult.Summary
				res.RawKey = execResult.RawKey
				res.TokensUsed = execResult.TokensUsed
			}
			if execErr != nil {
				res.Error = execErr.Error()
			}
			if d.bus != nil {
				d.publishEvent(ctx, "buckley.rlm.task.completed", map[string]any{
					"task_id":     res.TaskID,
					"agent_id":    agentID,
					"model":       modelID,
					"duration_ms": res.Duration.Milliseconds(),
					"error":       res.Error,
				})
			}
			return execErr
		}

		if d.breaker != nil {
			err = d.breaker.Execute(run)
		} else {
			err = run()
		}
		if err == nil {
			return res, nil
		}

		// Evaluate escalation strategy on failure.
		if d.engine == nil {
			return res, err
		}
		escalation, evalErr := d.engine.EvalStrategy("escalation", "escalation_policy", map[string]any{
			"failure": map[string]any{
				"type":         classifyError(err),
				"model_weight": string(weight),
				"attempt":      attempt,
			},
		})
		if evalErr != nil {
			return res, err
		}
		action, _ := escalation.Params["action"].(string)
		switch action {
		case "retry":
			continue
		case "escalate":
			if tw, ok := escalation.Params["target_weight"].(string); ok && tw != "" {
				newWeight := weightFromString(tw)
				if newWeight != "" {
					weight = newWeight
					newModel, selErr := d.router.Select(weight)
					if selErr == nil {
						modelID = newModel
						res.ModelUsed = modelID
						// Rebuild agent with new model.
						agent, err = NewSubAgent(SubAgentConfig{
							ID:            agentID,
							Model:         modelID,
							SystemPrompt:  task.SystemPrompt,
							MaxIterations: maxIterations,
							AllowedTools:  allowedTools,
						}, SubAgentDeps{
							Models:     d.models,
							Registry:   d.registry,
							Scratchpad: d.scratchpad,
							Conflicts:  d.conflicts,
							Approver:   d.approver,
						})
						if err != nil {
							res.Error = err.Error()
							return res, err
						}
					}
				}
			}
			continue
		case "abort":
			return res, err
		case "escalate_human":
			res.Error = fmt.Sprintf("escalated to human: %v", err)
			return res, fmt.Errorf("escalated to human: %w", err)
		default:
			return res, err
		}
	}
	return res, err
}

// weightFromString maps a string to a Weight constant.
func weightFromString(s string) Weight {
	switch strings.ToLower(s) {
	case "trivial":
		return WeightTrivial
	case "light":
		return WeightLight
	case "medium":
		return WeightMedium
	case "heavy":
		return WeightHeavy
	case "reasoning":
		return WeightReasoning
	default:
		return ""
	}
}

// inferTaskType extracts a rough task type from the prompt text.
func inferTaskType(prompt string) string {
	lower := strings.ToLower(prompt)
	switch {
	case strings.Contains(lower, "refactor"):
		return "refactor"
	case strings.Contains(lower, "review"):
		return "review"
	case strings.Contains(lower, "bugfix") || strings.Contains(lower, "bug fix") || strings.Contains(lower, "fix bug"):
		return "bugfix"
	default:
		return "general"
	}
}

// countPromptFiles counts rough file path occurrences in a prompt.
func countPromptFiles(prompt string) int {
	count := 0
	for _, word := range strings.Fields(prompt) {
		if strings.Contains(word, "/") && (strings.Contains(word, ".") || strings.HasSuffix(word, "/")) {
			count++
		}
	}
	return count
}

// classifyError returns a failure type string from an error.
func classifyError(err error) string {
	if err == nil {
		return "unknown"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "tool") || strings.Contains(msg, "execution error"):
		return "tool_error"
	case strings.Contains(msg, "budget") || strings.Contains(msg, "token") || strings.Contains(msg, "limit"):
		return "budget_exceeded"
	case strings.Contains(msg, "quality") || strings.Contains(msg, "confidence"):
		return "quality"
	default:
		return "unknown"
	}
}

// filterToolsByTier applies tool budget rules to filter allowed tools.
func (d *BatchDispatcher) filterToolsByTier(toolTier string, current []string) []string {
	if d.engine == nil {
		return current
	}
	matched, err := rules.Eval(d.engine, "tool_budget", rules.ToolBudgetFacts{
		ToolTier: toolTier,
	})
	if err != nil || len(matched) == 0 {
		return current
	}
	action, _ := matched[0].Params["action"].(string)
	if action == "filter" {
		if allowed, ok := matched[0].Params["allowed"]; ok {
			if allowedList, ok := allowed.([]any); ok {
				result := make([]string, 0, len(allowedList))
				for _, item := range allowedList {
					if s, ok := item.(string); ok {
						result = append(result, s)
					}
				}
				return result
			}
		}
	}
	return current
}

func (d *BatchDispatcher) publishEvent(ctx context.Context, subject string, payload map[string]any) {
	if d.bus == nil || subject == "" {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = d.bus.Publish(ctx, subject, data)
}
