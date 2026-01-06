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
		res.Error = err.Error()
		return res, err
	}

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
	if err != nil {
		return res, err
	}
	return res, nil
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
