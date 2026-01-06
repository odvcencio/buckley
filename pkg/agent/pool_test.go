package agent

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
)

func TestNewWorkerPool(t *testing.T) {
	b := bus.NewMemoryBus()
	defer b.Close()

	cfg := DefaultPoolConfig()
	cfg.QueueName = "test-pool"
	pool := NewWorkerPool(b, nil, nil, cfg)

	if pool == nil {
		t.Fatal("Expected non-nil pool")
	}
	if pool.config.Workers != 4 {
		t.Errorf("Expected 4 workers, got %d", pool.config.Workers)
	}
}

func TestWorkerPoolStartStop(t *testing.T) {
	b := bus.NewMemoryBus()
	defer b.Close()

	ctx := context.Background()

	cfg := DefaultPoolConfig()
	cfg.QueueName = "test-start-stop"
	cfg.Workers = 2
	pool := NewWorkerPool(b, nil, nil, cfg)

	// Subscribe to pool events
	var started atomic.Int32
	sub, _ := b.Subscribe(ctx, "buckley.pool.test-start-stop.events", func(msg *bus.Message) []byte {
		var event map[string]any
		if err := json.Unmarshal(msg.Data, &event); err == nil {
			if event["type"] == "pool_started" {
				started.Add(1)
			}
		}
		return nil
	})
	defer sub.Unsubscribe()

	// Start
	if err := pool.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	if started.Load() != 1 {
		t.Errorf("Expected pool_started event")
	}

	// Double start should fail
	if err := pool.Start(ctx); err == nil {
		t.Error("Expected error on double start")
	}

	// Stop
	pool.Stop()
}

func TestWorkerPoolQueueTask(t *testing.T) {
	b := bus.NewMemoryBus()
	defer b.Close()

	ctx := context.Background()

	cfg := DefaultPoolConfig()
	cfg.QueueName = "test-queue-task"
	pool := NewWorkerPool(b, nil, nil, cfg)

	task := QueuedTask{
		ID:          "task-1",
		Description: "Test task",
		Priority:    1,
		CreatedAt:   time.Now(),
	}

	if err := pool.QueueTask(ctx, task); err != nil {
		t.Fatalf("QueueTask failed: %v", err)
	}

	// Verify task is in queue
	queue := b.Queue(cfg.QueueName)
	length, err := queue.Len(ctx)
	if err != nil {
		t.Fatalf("Len failed: %v", err)
	}
	if length != 1 {
		t.Errorf("Expected queue length 1, got %d", length)
	}
}

func TestWorkerPoolStats(t *testing.T) {
	b := bus.NewMemoryBus()
	defer b.Close()

	cfg := DefaultPoolConfig()
	cfg.QueueName = "test-stats"
	pool := NewWorkerPool(b, nil, nil, cfg)

	stats := pool.Stats()

	if stats.TasksCompleted != 0 {
		t.Errorf("Expected 0 completed, got %d", stats.TasksCompleted)
	}
	if stats.TasksFailed != 0 {
		t.Errorf("Expected 0 failed, got %d", stats.TasksFailed)
	}
	if stats.TasksProcessing != 0 {
		t.Errorf("Expected 0 processing, got %d", stats.TasksProcessing)
	}
}

func TestWorkerPoolScaleWorkers(t *testing.T) {
	b := bus.NewMemoryBus()
	defer b.Close()

	ctx := context.Background()

	cfg := DefaultPoolConfig()
	cfg.QueueName = "test-scale"
	cfg.Workers = 2
	pool := NewWorkerPool(b, nil, nil, cfg)

	if err := pool.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer pool.Stop()

	time.Sleep(50 * time.Millisecond)

	// Scale up
	if err := pool.ScaleWorkers(ctx, 5); err != nil {
		t.Fatalf("ScaleWorkers up failed: %v", err)
	}

	pool.mu.RLock()
	count := len(pool.workers)
	pool.mu.RUnlock()

	if count != 5 {
		t.Errorf("Expected 5 workers after scale up, got %d", count)
	}

	// Scale down
	if err := pool.ScaleWorkers(ctx, 3); err != nil {
		t.Fatalf("ScaleWorkers down failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)

	pool.mu.RLock()
	count = len(pool.workers)
	pool.mu.RUnlock()

	if count != 3 {
		t.Errorf("Expected 3 workers after scale down, got %d", count)
	}

	// Invalid scale
	if err := pool.ScaleWorkers(ctx, 0); err == nil {
		t.Error("Expected error for scale to 0")
	}
}

func TestDefaultPoolConfig(t *testing.T) {
	cfg := DefaultPoolConfig()

	if cfg.Workers != 4 {
		t.Errorf("Expected 4 workers, got %d", cfg.Workers)
	}
	if cfg.PullTimeout != 5*time.Second {
		t.Errorf("Expected 5s pull timeout, got %v", cfg.PullTimeout)
	}
	if cfg.Role != RoleExecutor {
		t.Errorf("Expected RoleExecutor, got %v", cfg.Role)
	}
}

func TestQueuedTaskSerialization(t *testing.T) {
	task := QueuedTask{
		ID:          "task-123",
		Description: "Build the thing",
		Priority:    5,
		Metadata:    map[string]any{"repo": "buckley"},
		CreatedAt:   time.Now(),
	}

	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded QueuedTask
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != task.ID {
		t.Errorf("Expected ID %q, got %q", task.ID, decoded.ID)
	}
	if decoded.Description != task.Description {
		t.Errorf("Expected Description %q, got %q", task.Description, decoded.Description)
	}
	if decoded.Priority != task.Priority {
		t.Errorf("Expected Priority %d, got %d", task.Priority, decoded.Priority)
	}
}
