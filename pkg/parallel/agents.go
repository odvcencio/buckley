// Package parallel provides parallel agent execution using git worktrees.
// It allows running multiple independent agent tasks simultaneously.
package parallel

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
	"m31labs.dev/buckley/pkg/worktree"
)

// AgentTask represents a task to be executed by an agent
type AgentTask struct {
	ID          string
	Name        string
	Description string
	Branch      string            // Git branch for this task
	Prompt      string            // The task prompt for the agent
	Context     map[string]string // Additional context
	Priority    int               // Higher priority tasks run first
}

// AgentResult contains the result of an agent task
type AgentResult struct {
	TaskID       string
	Success      bool
	Output       string
	Error        error
	Duration     time.Duration
	Branch       string
	WorktreePath string
	Files        []string       // Files modified
	Metrics      map[string]int // Metrics like tokens used
	TotalCost    float64
	Usage        *transparency.TokenUsage
	CostUnknown  bool
	// ModelExecutions carries observed model-response identities in call order.
	// Empty means unavailable, not inferred from the task request.
	ModelExecutions []model.ExecutionIdentity
}

// AgentStatus represents the current status of an agent
type AgentStatus int

const (
	StatusIdle AgentStatus = iota
	StatusRunning
	StatusCompleted
	StatusFailed
	StatusCancelled
)

// Agent represents a single agent worker
type Agent struct {
	ID        string
	Worktree  *worktree.Worktree
	Status    AgentStatus
	Task      *AgentTask
	StartedAt time.Time
	Error     error
}

// Orchestrator manages parallel agent execution
type Orchestrator struct {
	mu              sync.RWMutex
	worktreeManager WorktreeManager
	agents          map[string]*Agent
	tasks           chan *AgentTask
	results         chan *AgentResult
	maxAgents       int
	executor        TaskExecutor
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	stopOnce        sync.Once
}

// TaskExecutor executes a task in a worktree
type TaskExecutor interface {
	Execute(ctx context.Context, task *AgentTask, wtPath string) (*AgentResult, error)
}

// WorktreeManager interface for worktree operations (enables testing)
type WorktreeManager interface {
	Create(branch string) (*worktree.Worktree, error)
	Remove(branch string, force bool) error
}

// Config configures the parallel orchestrator
type Config struct {
	MaxAgents       int
	WorktreeRoot    string
	TaskQueueSize   int
	ResultQueueSize int
}

// NewOrchestrator creates a new parallel orchestrator
func NewOrchestrator(wtManager WorktreeManager, executor TaskExecutor, cfg Config) *Orchestrator {
	if cfg.MaxAgents <= 0 {
		cfg.MaxAgents = 4
	}
	if cfg.TaskQueueSize <= 0 {
		cfg.TaskQueueSize = 100
	}
	if cfg.ResultQueueSize <= 0 {
		cfg.ResultQueueSize = 100
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Orchestrator{
		worktreeManager: wtManager,
		agents:          make(map[string]*Agent),
		tasks:           make(chan *AgentTask, cfg.TaskQueueSize),
		results:         make(chan *AgentResult, cfg.ResultQueueSize),
		maxAgents:       cfg.MaxAgents,
		executor:        executor,
		ctx:             ctx,
		cancel:          cancel,
	}
}

// Start starts the orchestrator and agent workers
func (o *Orchestrator) Start() {
	for i := 0; i < o.maxAgents; i++ {
		o.wg.Add(1)
		go o.worker(i)
	}
}

// Stop stops all agents and waits for completion
func (o *Orchestrator) Stop() {
	o.stopOnce.Do(func() {
		o.cancel()
		close(o.tasks)
		o.wg.Wait()
		close(o.results)
	})
}

// Submit submits a task for execution
func (o *Orchestrator) Submit(task *AgentTask) error {
	if task == nil {
		return fmt.Errorf("task is nil")
	}
	if task.ID == "" {
		task.ID = generateTaskID()
	}
	if task.Branch == "" {
		task.Branch = fmt.Sprintf("agent-%s", task.ID)
	}

	select {
	case o.tasks <- task:
		return nil
	case <-o.ctx.Done():
		return fmt.Errorf("orchestrator stopped")
	default:
		return fmt.Errorf("task queue full")
	}
}

// Results returns the results channel
func (o *Orchestrator) Results() <-chan *AgentResult {
	return o.results
}

// worker processes tasks from the queue
func (o *Orchestrator) worker(id int) {
	defer o.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			// Log but don't crash the program
			fmt.Fprintf(os.Stderr, "worker %d panic: %v\n", id, r)
		}
	}()

	agentID := fmt.Sprintf("agent-%d", id)

	for {
		select {
		case task, ok := <-o.tasks:
			if !ok {
				return
			}

			result := o.executeTask(agentID, task)

			select {
			case o.results <- result:
			case <-o.ctx.Done():
				return
			}

		case <-o.ctx.Done():
			return
		}
	}
}

func (o *Orchestrator) executeTask(agentID string, task *AgentTask) *AgentResult {
	start := time.Now()

	// Register agent. The Worktree field is set in a separate critical section
	// below because worktree creation is a blocking I/O operation that should
	// not be performed under the lock. Readers should handle Worktree == nil
	// gracefully during the window between registration and worktree assignment.
	o.mu.Lock()
	o.agents[agentID] = &Agent{
		ID:        agentID,
		Status:    StatusRunning,
		Task:      task,
		StartedAt: start,
	}
	o.mu.Unlock()

	// Create worktree (blocking I/O, intentionally outside lock)
	wt, err := o.worktreeManager.Create(task.Branch)
	if err != nil {
		o.updateAgentStatus(agentID, StatusFailed, err)
		return &AgentResult{
			TaskID:   task.ID,
			Success:  false,
			Error:    fmt.Errorf("failed to create worktree: %w", err),
			Duration: time.Since(start),
			Branch:   task.Branch,
		}
	}

	o.mu.Lock()
	if agent, ok := o.agents[agentID]; ok {
		agent.Worktree = wt
	}
	o.mu.Unlock()

	// Execute task
	result, err := o.executor.Execute(o.ctx, task, wt.Path)
	if err != nil {
		o.updateAgentStatus(agentID, StatusFailed, err)
		return &AgentResult{
			TaskID:       task.ID,
			Success:      false,
			Error:        err,
			Duration:     time.Since(start),
			Branch:       task.Branch,
			WorktreePath: wt.Path,
		}
	}

	// Update result
	result.TaskID = task.ID
	result.Duration = time.Since(start)
	result.Branch = task.Branch
	result.WorktreePath = wt.Path

	if result.Success {
		o.updateAgentStatus(agentID, StatusCompleted, nil)
	} else {
		o.updateAgentStatus(agentID, StatusFailed, result.Error)
	}

	return result
}

func (o *Orchestrator) updateAgentStatus(agentID string, status AgentStatus, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if agent, ok := o.agents[agentID]; ok {
		agent.Status = status
		agent.Error = err
	}
}

var taskIDCounter atomic.Int64

func generateTaskID() string {
	return fmt.Sprintf("task_%d", taskIDCounter.Add(1))
}

// Cleanup removes worktrees for completed tasks
func (o *Orchestrator) Cleanup() error {
	o.mu.Lock()
	defer o.mu.Unlock()

	var errors []string
	for _, agent := range o.agents {
		if agent.Status == StatusCompleted || agent.Status == StatusFailed {
			if agent.Worktree != nil {
				if err := o.worktreeManager.Remove(agent.Worktree.Branch, false); err != nil {
					errors = append(errors, fmt.Sprintf("%s: %v", agent.ID, err))
				}
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("cleanup errors: %v", errors)
	}
	return nil
}

// Summary returns a summary of orchestrator state
type Summary struct {
	TotalAgents    int
	ActiveAgents   int
	CompletedTasks int
	FailedTasks    int
	PendingTasks   int
}
