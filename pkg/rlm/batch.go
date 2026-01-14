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

const defaultBatchConcurrency = 5

// SubTask is a single delegated task.
// Simplified: no weight tiers, all sub-agents use the same model.
type SubTask struct {
	ID            string
	Prompt        string
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
// Simplified: no weight/escalation tracking.
type BatchResult struct {
	TaskID     string
	AgentID    string
	ModelUsed  string
	Summary    string
	RawKey     string
	TokensUsed int
	Duration   time.Duration
	Error      string
	ToolCalls  []ToolCallEvent // Tool calls made by sub-agent
}

// Dispatcher runs sub-agents with concurrency control.
// Simplified from BatchDispatcher: no weight-based routing or escalation.
type Dispatcher struct {
	selector    *ModelSelector
	models      *model.Manager
	registry    *tool.Registry
	scratchpad  ScratchpadWriter
	conflicts   *ConflictDetector
	approver    *security.ToolApprover
	rateLimiter *rate.Limiter
	semaphore   chan struct{}
	breaker     *reliability.CircuitBreaker
	bus         bus.MessageBus
	telemetry   *telemetry.Hub
	timeout     time.Duration
	timeoutMu   sync.Mutex
	lastDuration time.Duration
	taskCount    int
}

// DispatcherConfig configures dispatcher behavior.
type DispatcherConfig struct {
	MaxConcurrent int
	Timeout       time.Duration
	RateLimit     rate.Limit
	Burst         int
	Circuit       reliability.CircuitBreakerConfig
}

// DispatcherDeps supplies dependencies for dispatcher creation.
type DispatcherDeps struct {
	Selector   *ModelSelector
	Models     *model.Manager
	Registry   *tool.Registry
	Scratchpad ScratchpadWriter
	Conflicts  *ConflictDetector
	Approver   *security.ToolApprover
	Bus        bus.MessageBus
	Telemetry  *telemetry.Hub
}

// NewDispatcher creates a dispatcher with the given configuration.
func NewDispatcher(cfg DispatcherConfig, deps DispatcherDeps) (*Dispatcher, error) {
	if deps.Selector == nil {
		return nil, fmt.Errorf("model selector required")
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

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	d := &Dispatcher{
		selector:    deps.Selector,
		models:      deps.Models,
		registry:    deps.Registry,
		scratchpad:  deps.Scratchpad,
		conflicts:   deps.Conflicts,
		approver:    deps.Approver,
		rateLimiter: limiter,
		semaphore:   make(chan struct{}, maxConcurrent),
		bus:         deps.Bus,
		telemetry:   deps.Telemetry,
		timeout:     timeout,
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
func (d *Dispatcher) Execute(ctx context.Context, req BatchRequest) ([]BatchResult, error) {
	if len(req.Tasks) == 0 {
		return nil, nil
	}
	if !req.Parallel || len(req.Tasks) == 1 {
		return d.executeSequential(ctx, req.Tasks)
	}
	return d.executeParallel(ctx, req.Tasks)
}

func (d *Dispatcher) executeSequential(ctx context.Context, tasks []SubTask) ([]BatchResult, error) {
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

func (d *Dispatcher) executeParallel(ctx context.Context, tasks []SubTask) ([]BatchResult, error) {
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

func (d *Dispatcher) executeTask(ctx context.Context, task SubTask) (BatchResult, error) {
	res := BatchResult{TaskID: task.ID}
	if strings.TrimSpace(task.Prompt) == "" {
		res.Error = "task prompt required"
		return res, fmt.Errorf("task prompt required")
	}
	if res.TaskID == "" {
		res.TaskID = ulid.Make().String()
	}

	// Get the model from selector (single model for all sub-agents)
	modelID := d.selector.Select()
	if modelID == "" {
		res.Error = "no model available"
		return res, fmt.Errorf("no model available")
	}
	res.ModelUsed = modelID

	if d.rateLimiter != nil {
		if err := d.rateLimiter.Wait(ctx); err != nil {
			res.Error = err.Error()
			return res, err
		}
	}

	agentID := fmt.Sprintf("rlm-%s", res.TaskID)

	// Apply timeout if configured
	timeout := d.nextTimeout()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	execResult, execErr := d.executeSubAgent(ctx, task, agentID, modelID, &res)

	// Merge results
	if execResult != nil {
		res.AgentID = execResult.AgentID
		res.Summary = execResult.Summary
		res.RawKey = execResult.RawKey
		res.TokensUsed = execResult.TokensUsed
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

	if execErr != nil {
		res.Error = execErr.Error()
	}

	d.recordTaskDuration(res.Duration)

	return res, execErr
}

// executeSubAgent runs a single sub-agent.
func (d *Dispatcher) executeSubAgent(ctx context.Context, task SubTask, agentID, modelID string, res *BatchResult) (*SubAgentResult, error) {
	agent, err := NewSubAgent(SubAgentInstanceConfig{
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

		// Emit task started
		d.publishEvent(ctx, "buckley.rlm.task.started", map[string]any{
			"task_id":  res.TaskID,
			"agent_id": agentID,
			"model":    modelID,
		})

		// Emit to telemetry for TUI
		if d.telemetry != nil {
			d.telemetry.Publish(telemetry.Event{
				Type:   telemetry.EventTaskStarted,
				TaskID: res.TaskID,
				Data: map[string]any{
					"agent_id": agentID,
					"model":    modelID,
				},
			})
		}

		execResult, execErr = agent.Execute(ctx, task.Prompt)
		res.Duration = time.Since(start)

		// Emit completion
		d.publishEvent(ctx, "buckley.rlm.task.completed", map[string]any{
			"task_id":     res.TaskID,
			"agent_id":    agentID,
			"model":       modelID,
			"duration_ms": res.Duration.Milliseconds(),
			"tokens_used": res.TokensUsed,
			"error":       res.Error,
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
					"duration_ms":      res.Duration.Milliseconds(),
					"tokens_used":      res.TokensUsed,
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

func (d *Dispatcher) publishEvent(ctx context.Context, subject string, payload map[string]any) {
	if d.bus == nil || subject == "" {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = d.bus.Publish(ctx, subject, data)
}

func (d *Dispatcher) nextTimeout() time.Duration {
	if d == nil {
		return 0
	}
	base := d.timeout
	if base <= 0 {
		return 0
	}

	d.timeoutMu.Lock()
	defer d.timeoutMu.Unlock()

	timeout := base
	if d.taskCount > 0 && d.lastDuration > 0 {
		timeout = d.lastDuration + d.lastDuration/2
		maxTimeout := base * 2
		if timeout > maxTimeout {
			timeout = maxTimeout
		}
		minTimeout := base / 2
		if minTimeout > 0 && timeout < minTimeout {
			timeout = minTimeout
		}
	}
	d.taskCount++
	return timeout
}

func (d *Dispatcher) recordTaskDuration(duration time.Duration) {
	if d == nil || duration <= 0 {
		return
	}
	d.timeoutMu.Lock()
	d.lastDuration = duration
	d.timeoutMu.Unlock()
}
