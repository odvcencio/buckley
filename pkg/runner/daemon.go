package runner

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/odvcencio/buckley/pkg/orchestrator"
)

// Task is a unit of work for the daemon.
type Task struct {
	ID       string
	Prompt   string
	Priority int
	Deadline time.Time
	Channel  ChannelType
}

// TaskResult is the outcome of processing a task.
type TaskResult struct {
	ID     string
	Result *orchestrator.TurnSummary
	Error  string
}

// DaemonRunner processes tasks from a queue with per-task session isolation.
type DaemonRunner struct {
	config      *RunnerConfig
	deps        *RuntimeDeps
	inbox       chan Task
	outbox      chan TaskResult
	activeCount atomic.Int32
	mu          sync.RWMutex
}

// NewDaemonRunner creates a daemon with buffered channels.
func NewDaemonRunner(config *RunnerConfig, deps *RuntimeDeps) *DaemonRunner {
	return &DaemonRunner{
		config: config,
		deps:   deps,
		inbox:  make(chan Task, 20),
		outbox: make(chan TaskResult, 20),
	}
}

// Submit adds a task to the inbox.
func (d *DaemonRunner) Submit(task Task) {
	d.inbox <- task
}

// Results returns the outbox channel for receiving task results.
func (d *DaemonRunner) Results() <-chan TaskResult {
	return d.outbox
}

// Run processes tasks until ctx is cancelled.
func (d *DaemonRunner) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case task := <-d.inbox:
			// Arbiter intake check
			if d.deps.Evaluator != nil {
				result, err := d.deps.Evaluator.EvalStrategy("autonomous/sessions", "intake_policy", map[string]any{
					"task_id":      task.ID,
					"priority":     task.Priority,
					"channel":      task.Channel.String(),
					"active_tasks": int(d.activeCount.Load()),
					"budget_util":  0.0, // caller wires real values
				})
				if err == nil {
					switch result.String("action") {
					case "reject":
						d.outbox <- TaskResult{ID: task.ID, Error: result.String("reason")}
						continue
					case "queue":
						delay := result.Int("delay_seconds")
						go func() {
							time.Sleep(time.Duration(delay) * time.Second)
							d.inbox <- task
						}()
						continue
					}
				}
			}

			go d.executeTask(ctx, task)
		}
	}
}

func (d *DaemonRunner) executeTask(ctx context.Context, task Task) {
	d.activeCount.Add(1)
	defer d.activeCount.Add(-1)

	loop := orchestrator.NewRuntimeLoop(d.deps.Api, d.deps.Tools, d.deps.Escalator, d.deps.Sandbox, d.deps.Evaluator)
	if d.config.MaxTurns > 0 {
		loop.SetMaxIterations(d.config.MaxTurns)
	}
	loop.SetRole(d.config.Role)

	summary, err := loop.RunTurn(ctx, task.Prompt)

	result := TaskResult{ID: task.ID, Result: summary}
	if err != nil {
		result.Error = err.Error()
	}
	d.outbox <- result
}
