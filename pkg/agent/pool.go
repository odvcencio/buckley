package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

// WorkerPool manages a pool of executor agents pulling from a TaskQueue.
type WorkerPool struct {
	bus      bus.MessageBus
	models   *model.Manager
	tools    *tool.Registry
	config   PoolConfig
	executor *TaskExecutor

	mu       sync.RWMutex
	workers  map[string]*worker
	stats    PoolStats
	running  atomic.Bool
	stopChan chan struct{}
}

// PoolConfig configures the worker pool.
type PoolConfig struct {
	// Workers is the number of concurrent workers
	Workers int

	// QueueName is the name of the task queue to pull from
	QueueName string

	// Role is the role for all workers in this pool
	Role Role

	// AgentConfig is applied to all worker agents
	AgentConfig AgentConfig

	// ExecutorConfig is applied to all executors
	ExecutorConfig ExecutorConfig

	// PullTimeout is how long to wait for a task before checking stop signal
	PullTimeout time.Duration
}

// DefaultPoolConfig returns sensible defaults.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		Workers:        4,
		Role:           RoleExecutor,
		AgentConfig:    DefaultAgentConfig(),
		ExecutorConfig: DefaultExecutorConfig(),
		PullTimeout:    5 * time.Second,
	}
}

// PoolStats tracks pool performance metrics using atomics for thread-safety.
type PoolStats struct {
	TasksCompleted  atomic.Int64
	TasksFailed     atomic.Int64
	TasksProcessing atomic.Int64
	TotalDuration   atomic.Int64 // nanoseconds
	TotalTokens     atomic.Int64
}

// PoolStatsSnapshot is a point-in-time copy of pool stats for reporting.
type PoolStatsSnapshot struct {
	TasksCompleted  int64 `json:"tasksCompleted"`
	TasksFailed     int64 `json:"tasksFailed"`
	TasksProcessing int64 `json:"tasksProcessing"`
	TotalDuration   int64 `json:"totalDuration"` // nanoseconds
	TotalTokens     int64 `json:"totalTokens"`
}

// worker represents a single worker in the pool.
type worker struct {
	id       string
	pool     *WorkerPool
	queue    bus.TaskQueue
	stopChan chan struct{}
	running  atomic.Bool
}

// NewWorkerPool creates a new worker pool.
func NewWorkerPool(b bus.MessageBus, models *model.Manager, tools *tool.Registry, cfg PoolConfig) *WorkerPool {
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.PullTimeout == 0 {
		cfg.PullTimeout = 5 * time.Second
	}

	executor := NewTaskExecutor(b, models, tools, cfg.ExecutorConfig)

	return &WorkerPool{
		bus:      b,
		models:   models,
		tools:    tools,
		config:   cfg,
		executor: executor,
		workers:  make(map[string]*worker),
		stopChan: make(chan struct{}),
	}
}

// Start begins processing tasks from the queue.
func (p *WorkerPool) Start(ctx context.Context) error {
	if p.running.Swap(true) {
		return fmt.Errorf("pool already running")
	}

	queue := p.bus.Queue(p.config.QueueName)

	p.mu.Lock()
	for i := 0; i < p.config.Workers; i++ {
		w := &worker{
			id:       fmt.Sprintf("worker-%d", i),
			pool:     p,
			queue:    queue,
			stopChan: make(chan struct{}),
		}
		p.workers[w.id] = w
		go w.run(ctx)
	}
	p.mu.Unlock()

	// Publish pool started event
	p.publishEvent(ctx, "pool_started", map[string]any{
		"workers":    p.config.Workers,
		"queue_name": p.config.QueueName,
	})

	return nil
}

// Stop gracefully shuts down the pool.
func (p *WorkerPool) Stop() {
	if !p.running.Swap(false) {
		return
	}

	close(p.stopChan)

	p.mu.Lock()
	for _, w := range p.workers {
		close(w.stopChan)
	}
	p.mu.Unlock()

	// Wait for workers to finish current tasks
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if p.stats.TasksProcessing.Load() == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Stats returns a snapshot of current pool statistics.
func (p *WorkerPool) Stats() PoolStatsSnapshot {
	return PoolStatsSnapshot{
		TasksCompleted:  p.stats.TasksCompleted.Load(),
		TasksFailed:     p.stats.TasksFailed.Load(),
		TasksProcessing: p.stats.TasksProcessing.Load(),
		TotalDuration:   p.stats.TotalDuration.Load(),
		TotalTokens:     p.stats.TotalTokens.Load(),
	}
}

// QueueTask adds a task to the pool's queue.
func (p *WorkerPool) QueueTask(ctx context.Context, task QueuedTask) error {
	data, err := json.Marshal(task)
	if err != nil {
		return err
	}

	queue := p.bus.Queue(p.config.QueueName)
	return queue.Push(ctx, data)
}

// QueuedTask represents a task in the queue.
type QueuedTask struct {
	ID          string         `json:"id"`
	Description string         `json:"description"`
	Priority    int            `json:"priority"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

// WaitEmpty blocks until the queue is empty.
func (p *WorkerPool) WaitEmpty(ctx context.Context) error {
	queue := p.bus.Queue(p.config.QueueName)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			length, err := queue.Len(ctx)
			if err != nil {
				return err
			}
			if length == 0 && p.stats.TasksProcessing.Load() == 0 {
				return nil
			}
		}
	}
}

func (p *WorkerPool) publishEvent(ctx context.Context, eventType string, data map[string]any) {
	event := map[string]any{
		"type":       eventType,
		"queue_name": p.config.QueueName,
		"data":       data,
		"timestamp":  time.Now(),
	}

	eventData, _ := json.Marshal(event)
	subject := fmt.Sprintf("buckley.pool.%s.events", p.config.QueueName)
	p.bus.Publish(ctx, subject, eventData)
}

// worker methods

func (w *worker) run(ctx context.Context) {
	w.running.Store(true)
	defer w.running.Store(false)

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopChan:
			return
		case <-w.pool.stopChan:
			return
		default:
		}

		// Try to pull a task
		pullCtx, cancel := context.WithTimeout(ctx, w.pool.config.PullTimeout)
		task, err := w.queue.Pull(pullCtx)
		cancel()

		if err != nil {
			// Timeout or empty queue, try again
			continue
		}

		// Process task
		w.processTask(ctx, task)
	}
}

func (w *worker) processTask(ctx context.Context, task *bus.Task) {
	w.pool.stats.TasksProcessing.Add(1)
	defer w.pool.stats.TasksProcessing.Add(-1)

	// Parse queued task
	var qt QueuedTask
	if err := json.Unmarshal(task.Data, &qt); err != nil {
		// Bad task data, ack and move on
		w.queue.Ack(ctx, task.ID)
		w.pool.stats.TasksFailed.Add(1)
		return
	}

	// Execute
	result, err := w.pool.executor.Execute(
		ctx,
		qt.ID,
		w.pool.config.Role,
		qt.Description,
		w.pool.config.AgentConfig,
	)

	if err != nil || !result.Success {
		// Task failed
		w.queue.Nack(ctx, task.ID) // Return to queue for retry
		w.pool.stats.TasksFailed.Add(1)

		w.pool.publishEvent(ctx, "task_failed", map[string]any{
			"task_id":   qt.ID,
			"worker_id": w.id,
			"error":     result.Error,
		})
		return
	}

	// Task succeeded
	w.queue.Ack(ctx, task.ID)
	w.pool.stats.TasksCompleted.Add(1)
	w.pool.stats.TotalDuration.Add(int64(result.Duration))
	w.pool.stats.TotalTokens.Add(int64(result.TokensUsed))

	w.pool.publishEvent(ctx, "task_completed", map[string]any{
		"task_id":     qt.ID,
		"worker_id":   w.id,
		"duration":    result.Duration.String(),
		"tokens_used": result.TokensUsed,
	})
}

// ScaleWorkers adjusts the number of workers dynamically.
func (p *WorkerPool) ScaleWorkers(ctx context.Context, count int) error {
	if count < 1 {
		return fmt.Errorf("worker count must be >= 1")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	currentCount := len(p.workers)

	if count > currentCount {
		// Scale up
		queue := p.bus.Queue(p.config.QueueName)
		for i := currentCount; i < count; i++ {
			w := &worker{
				id:       fmt.Sprintf("worker-%d", i),
				pool:     p,
				queue:    queue,
				stopChan: make(chan struct{}),
			}
			p.workers[w.id] = w
			go w.run(ctx)
		}
	} else if count < currentCount {
		// Scale down (stop excess workers)
		toRemove := currentCount - count
		for id, w := range p.workers {
			if toRemove <= 0 {
				break
			}
			close(w.stopChan)
			delete(p.workers, id)
			toRemove--
		}
	}

	return nil
}
