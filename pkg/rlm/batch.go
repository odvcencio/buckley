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
	"github.com/odvcencio/buckley/pkg/telemetry"
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

// ToolCallEvent records a tool invocation for transparency.
type ToolCallEvent struct {
	TaskID    string        `json:"task_id"`
	AgentID   string        `json:"agent_id"`
	ToolName  string        `json:"tool_name"`
	Arguments string        `json:"arguments,omitempty"`
	Success   bool          `json:"success"`
	Error     string        `json:"error,omitempty"`
	Duration  time.Duration `json:"duration_ms"`
}

// BatchResult captures the outcome of a dispatched task.
type BatchResult struct {
	TaskID           string
	AgentID          string
	ModelUsed        string
	Summary          string
	RawKey           string
	TokensUsed       int
	Duration         time.Duration
	Error            string
	WeightRequested  Weight        // Original weight requested
	WeightUsed       Weight        // Actual weight after escalation
	WeightExplanation string       // Why this weight/model was chosen
	EscalationPath   []string      // Models tried before success (for transparency)
	ToolCalls        []ToolCallEvent // Tool calls made by sub-agent
}

// BatchDispatcher runs sub-agents with optional concurrency control.
type BatchDispatcher struct {
	router           *ModelRouter
	models           *model.Manager
	registry         *tool.Registry
	scratchpad       ScratchpadWriter
	conflicts        *ConflictDetector
	approver         *security.ToolApprover
	rateLimiter      *rate.Limiter
	semaphore        chan struct{}
	breaker          *reliability.CircuitBreaker
	bus              bus.MessageBus
	telemetry        *telemetry.Hub
	enableEscalation bool
	maxEscalations   int
}

// BatchDispatcherConfig configures dispatcher behavior.
type BatchDispatcherConfig struct {
	MaxConcurrent    int
	RateLimit        rate.Limit
	Burst            int
	Circuit          reliability.CircuitBreakerConfig
	EnableEscalation bool // Auto-retry with higher tier on failure
	MaxEscalations   int  // Max tier escalations per task (default 2)
}

// EscalationOrder defines the weight tier escalation path.
var EscalationOrder = []Weight{
	WeightTrivial,
	WeightLight,
	WeightMedium,
	WeightHeavy,
	WeightReasoning,
}

// nextWeight returns the next higher weight tier for escalation.
func nextWeight(current Weight) (Weight, bool) {
	for i, w := range EscalationOrder {
		if w == current && i < len(EscalationOrder)-1 {
			return EscalationOrder[i+1], true
		}
	}
	return current, false
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
	Telemetry  *telemetry.Hub
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

	maxEscalations := cfg.MaxEscalations
	if maxEscalations <= 0 {
		maxEscalations = 2 // Default: allow up to 2 escalations
	}

	d := &BatchDispatcher{
		router:           deps.Router,
		models:           deps.Models,
		registry:         deps.Registry,
		scratchpad:       deps.Scratchpad,
		conflicts:        deps.Conflicts,
		approver:         deps.Approver,
		rateLimiter:      limiter,
		semaphore:        make(chan struct{}, maxConcurrent),
		bus:              deps.Bus,
		telemetry:        deps.Telemetry,
		enableEscalation: cfg.EnableEscalation,
		maxEscalations:   maxEscalations,
	}

	// Configure circuit breaker with logging callbacks
	cfg.Circuit.OnFailure = func(event reliability.FailureEvent) {
		// Publish to bus for IPC/logging
		d.publishEvent(context.Background(), "buckley.rlm.circuit.failure", map[string]any{
			"error":           event.Error.Error(),
			"consecutive_num": event.ConsecutiveNum,
			"max_failures":    event.MaxFailures,
			"will_open":       event.WillOpen,
		})
		// Publish to telemetry for TUI
		if d.telemetry != nil {
			d.telemetry.Publish(telemetry.Event{
				Type: telemetry.EventCircuitFailure,
				Data: map[string]any{
					"error":           event.Error.Error(),
					"consecutive_num": event.ConsecutiveNum,
					"max_failures":    event.MaxFailures,
					"will_open":       event.WillOpen,
				},
			})
		}
	}
	cfg.Circuit.OnStateChange = func(event reliability.StateChangeEvent) {
		errMsg := ""
		if event.LastError != nil {
			errMsg = event.LastError.Error()
		}
		// Publish to bus for IPC/logging
		d.publishEvent(context.Background(), "buckley.rlm.circuit.state_change", map[string]any{
			"from":       event.From.String(),
			"to":         event.To.String(),
			"reason":     event.Reason,
			"last_error": errMsg,
		})
		// Publish to telemetry for TUI
		if d.telemetry != nil {
			d.telemetry.Publish(telemetry.Event{
				Type: telemetry.EventCircuitStateChange,
				Data: map[string]any{
					"from":       event.From.String(),
					"to":         event.To.String(),
					"reason":     event.Reason,
					"last_error": errMsg,
				},
			})
		}
	}

	d.breaker = reliability.NewCircuitBreaker(cfg.Circuit)

	return d, nil
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

	// Track original weight request
	weight := task.Weight
	if strings.TrimSpace(string(weight)) == "" {
		weight = WeightMedium
	}
	res.WeightRequested = weight
	res.WeightUsed = weight
	res.EscalationPath = []string{}

	// Try with escalation if enabled
	currentWeight := weight
	escalations := 0

	for {
		modelID, explanation, err := d.selectModelWithExplanation(currentWeight)
		if err != nil {
			res.Error = err.Error()
			return res, err
		}
		res.ModelUsed = modelID
		res.WeightUsed = currentWeight
		res.WeightExplanation = explanation
		res.EscalationPath = append(res.EscalationPath, fmt.Sprintf("%s:%s", currentWeight, modelID))

		if d.rateLimiter != nil {
			if err := d.rateLimiter.Wait(ctx); err != nil {
				res.Error = err.Error()
				return res, err
			}
		}

		agentID := fmt.Sprintf("rlm-%s", res.TaskID)
		if escalations > 0 {
			agentID = fmt.Sprintf("rlm-%s-esc%d", res.TaskID, escalations)
		}

		execResult, execErr := d.executeSubAgent(ctx, task, agentID, modelID, &res)

		// Merge results
		if execResult != nil {
			res.AgentID = execResult.AgentID
			res.Summary = execResult.Summary
			res.RawKey = execResult.RawKey
			res.TokensUsed += execResult.TokensUsed
			// Convert tool calls for transparency
			for _, tc := range execResult.ToolCalls {
				res.ToolCalls = append(res.ToolCalls, ToolCallEvent{
					TaskID:    res.TaskID,
					AgentID:   agentID,
					ToolName:  tc.Name,
					Arguments: tc.Arguments,
					Success:   tc.Success,
					Error:     tc.Result,
					Duration:  tc.Duration,
				})
			}
		}

		// Success - we're done
		if execErr == nil {
			res.Error = ""
			return res, nil
		}

		// Check if we should escalate
		if !d.enableEscalation || escalations >= d.maxEscalations {
			res.Error = execErr.Error()
			return res, execErr
		}

		// Try to escalate to next tier
		nextW, canEscalate := nextWeight(currentWeight)
		if !canEscalate {
			res.Error = execErr.Error()
			return res, execErr
		}

		// Emit escalation event for transparency
		d.emitEscalationEvent(ctx, res.TaskID, currentWeight, nextW, execErr.Error())

		currentWeight = nextW
		escalations++
	}
}

// selectModelWithExplanation returns model ID and human-readable explanation.
func (d *BatchDispatcher) selectModelWithExplanation(weight Weight) (string, string, error) {
	modelID, err := d.router.Select(weight)
	if err != nil {
		return "", "", err
	}

	// Build explanation
	explanation := fmt.Sprintf("Selected %s for %s tier", modelID, weight)

	// Add tier-specific context
	switch weight {
	case WeightTrivial:
		explanation += " (fast, low-cost for simple lookups)"
	case WeightLight:
		explanation += " (balanced for basic analysis)"
	case WeightMedium:
		explanation += " (default tier for general tasks)"
	case WeightHeavy:
		explanation += " (high-quality for complex operations)"
	case WeightReasoning:
		explanation += " (extended thinking for deep analysis)"
	}

	return modelID, explanation, nil
}

// executeSubAgent runs a single sub-agent attempt.
func (d *BatchDispatcher) executeSubAgent(ctx context.Context, task SubTask, agentID, modelID string, res *BatchResult) (*SubAgentResult, error) {
	agent, err := NewSubAgent(SubAgentConfig{
		ID:            agentID,
		Model:         modelID,
		SystemPrompt:  task.SystemPrompt,
		MaxIterations: task.MaxIterations,
		AllowedTools:  task.AllowedTools,
	}, SubAgentDeps{
		Models:     d.models,
		Registry:   d.registry,
		Scratchpad: d.scratchpad,
		Conflicts:  d.conflicts,
		Approver:   d.approver,
	})
	if err != nil {
		return nil, err
	}

	var execResult *SubAgentResult
	var execErr error

	run := func() error {
		start := time.Now()

		// Emit task started with weight info
		d.publishEvent(ctx, "buckley.rlm.task.started", map[string]any{
			"task_id":          res.TaskID,
			"agent_id":         agentID,
			"model":            modelID,
			"weight":           string(res.WeightUsed),
			"weight_requested": string(res.WeightRequested),
			"explanation":      res.WeightExplanation,
		})

		// Emit to telemetry for TUI
		if d.telemetry != nil {
			d.telemetry.Publish(telemetry.Event{
				Type:   telemetry.EventTaskStarted,
				TaskID: res.TaskID,
				Data: map[string]any{
					"agent_id":         agentID,
					"model":            modelID,
					"weight":           string(res.WeightUsed),
					"weight_requested": string(res.WeightRequested),
					"explanation":      res.WeightExplanation,
				},
			})
		}

		execResult, execErr = agent.Execute(ctx, task.Prompt)
		res.Duration = time.Since(start)

		// Emit tool calls in real-time (already happened during execution)
		// Now emit completion
		d.publishEvent(ctx, "buckley.rlm.task.completed", map[string]any{
			"task_id":         res.TaskID,
			"agent_id":        agentID,
			"model":           modelID,
			"weight":          string(res.WeightUsed),
			"duration_ms":     res.Duration.Milliseconds(),
			"tokens_used":     res.TokensUsed,
			"escalation_path": res.EscalationPath,
			"error":           res.Error,
		})

		if d.telemetry != nil {
			eventType := telemetry.EventTaskCompleted
			if execErr != nil {
				eventType = telemetry.EventTaskFailed
			}
			d.telemetry.Publish(telemetry.Event{
				Type:   eventType,
				TaskID: res.TaskID,
				Data: map[string]any{
					"agent_id":         agentID,
					"model":            modelID,
					"weight":           string(res.WeightUsed),
					"duration_ms":      res.Duration.Milliseconds(),
					"tokens_used":      res.TokensUsed,
					"escalation_path":  res.EscalationPath,
					"tool_calls_count": len(res.ToolCalls),
				},
			})
		}

		return execErr
	}

	if d.breaker != nil {
		err = d.breaker.Execute(run)
	} else {
		err = run()
	}

	return execResult, err
}

// emitEscalationEvent publishes an escalation event for transparency.
func (d *BatchDispatcher) emitEscalationEvent(ctx context.Context, taskID string, from, to Weight, reason string) {
	d.publishEvent(ctx, "buckley.rlm.task.escalated", map[string]any{
		"task_id":     taskID,
		"from_weight": string(from),
		"to_weight":   string(to),
		"reason":      reason,
	})

	if d.telemetry != nil {
		d.telemetry.Publish(telemetry.Event{
			Type:   telemetry.EventType("rlm.escalation"),
			TaskID: taskID,
			Data: map[string]any{
				"from_weight": string(from),
				"to_weight":   string(to),
				"reason":      reason,
			},
		})
	}
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
