package machine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/machine/locks"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

const defaultMaxIterations = 100

// ModelCaller performs model inference.
type ModelCaller interface {
	Call(ctx context.Context, action CallModel) (Event, error)
}

// ToolBatchExecutor runs a batch of tool calls.
type ToolBatchExecutor interface {
	Execute(ctx context.Context, calls []ToolCallRequest) ToolsCompleted
}

// Compactor summarizes context to reduce token usage.
type Compactor interface {
	Compact(ctx context.Context, action Compact) (CompactionCompleted, error)
}

const defaultMaxDelegationDepth = 3

// RuntimeConfig holds dependencies for the Runtime.
type RuntimeConfig struct {
	Hub                *telemetry.Hub
	ModelClient        ModelCaller
	ToolExecutor       ToolBatchExecutor
	LockManager        *locks.Manager
	Compactor          Compactor
	CommitExecutor     CommitExecutor
	ShellExecutor      ShellExecutor
	MaxIterations      int
	MaxDelegationDepth int
}

// RuntimeResult is the output of a completed machine run.
type RuntimeResult struct {
	Content    string
	FinalState State
	TokensUsed int
	Iterations int
}

// Runtime drives a Machine through state transitions by executing Actions.
type Runtime struct {
	cfg   RuntimeConfig
	depth int // current delegation nesting depth

	mu       sync.Mutex
	steering map[string]string // agentID → queued steering
}

// NewRuntime creates a new runtime executor.
func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = defaultMaxIterations
	}
	if cfg.MaxDelegationDepth <= 0 {
		cfg.MaxDelegationDepth = defaultMaxDelegationDepth
	}
	return &Runtime{
		cfg:      cfg,
		steering: make(map[string]string),
	}
}

// Steer queues a steering message for the given agent.
func (r *Runtime) Steer(agentID, content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steering[agentID] = content
}

func (r *Runtime) consumeSteering(agentID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.steering[agentID]
	delete(r.steering, agentID)
	return s
}

// Run creates an Observable machine and drives it to completion.
func (r *Runtime) Run(ctx context.Context, id string, modality Modality, input string) (*RuntimeResult, error) {
	m := NewObservable(id, modality, r.cfg.Hub)

	// Inject any queued steering
	if s := r.consumeSteering(id); s != "" {
		m.Transition(UserSteering{Content: s})
	}

	return r.drive(ctx, m, input)
}

// drive executes the core machine loop. Shared by Run and runChild.
func (r *Runtime) drive(ctx context.Context, m *Observable, input string) (*RuntimeResult, error) {
	_, actions := m.Transition(UserInput{Content: input})

	iterations := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("machine %s cancelled: %w", m.ID(), err)
		}

		iterations++
		if iterations > r.cfg.MaxIterations {
			m.Transition(Cancelled{})
			return &RuntimeResult{
				FinalState: m.State(),
				Iterations: iterations,
			}, nil
		}

		if m.State().IsTerminal() {
			return r.buildResult(m, actions, iterations), nil
		}

		var event Event

		// Execute actions and collect resulting events.
		// Keep the first non-nil event as the transition trigger;
		// subsequent actions (ReleaseLocks, EmitResult, etc.) are
		// side-effects that must not overwrite it.
		for _, action := range actions {
			e, execErr := r.executeAction(ctx, m, action)
			if execErr != nil {
				return nil, fmt.Errorf("machine %s action failed: %w", m.ID(), execErr)
			}
			if event == nil && e != nil {
				event = e
			}
		}

		if event == nil {
			// No event from actions — machine should be terminal or waiting
			if m.State().IsTerminal() {
				return r.buildResult(m, nil, iterations), nil
			}
			return nil, fmt.Errorf("machine %s stuck in state %s with no event", m.ID(), m.State())
		}

		// Inject steering if queued
		if s := r.consumeSteering(m.ID()); s != "" {
			m.Transition(UserSteering{Content: s})
		}

		_, actions = m.Transition(event)
	}
}

func (r *Runtime) executeAction(ctx context.Context, m *Observable, action Action) (Event, error) {
	switch act := action.(type) {
	case CallModel:
		return r.cfg.ModelClient.Call(ctx, act)

	case ExecuteToolBatch:
		if r.cfg.ToolExecutor == nil {
			return ToolsCompleted{}, nil
		}
		result := r.cfg.ToolExecutor.Execute(ctx, act.Calls)
		return result, nil

	case AcquireLockBatch:
		return r.executeLockBatch(ctx, m, act)

	case ReleaseLocks:
		if r.cfg.LockManager != nil {
			r.cfg.LockManager.ReleaseAll(m.ID())
		}
		return nil, nil

	case Compact:
		if r.cfg.Compactor == nil {
			return CompactionCompleted{}, nil
		}
		result, err := r.cfg.Compactor.Compact(ctx, act)
		return result, err

	case EmitResult:
		// Terminal action — no event to feed back
		return nil, nil

	case EmitError:
		// Terminal action — no event to feed back
		return nil, nil

	case CommitChanges:
		return r.executeCommit(ctx, act)

	case RunVerification:
		return r.executeVerification(ctx, act)

	case ResetContext:
		return r.executeResetContext(act)

	case DelegateToSubAgents:
		return r.executeDelegation(ctx, m, act)

	case ReviewSubAgentOutput:
		return r.executeReview(act), nil

	case SaveCheckpoint:
		return CheckpointSaved{}, nil

	default:
		return nil, fmt.Errorf("unknown action type: %T", action)
	}
}

func (r *Runtime) executeLockBatch(_ context.Context, m *Observable, act AcquireLockBatch) (Event, error) {
	if r.cfg.LockManager == nil {
		return LocksAcquired{}, nil
	}

	var acquired []LockRequest
	for _, lock := range act.Locks {
		var err error
		switch lock.Mode {
		case LockRead:
			err = r.cfg.LockManager.AcquireRead(m.ID(), lock.Path)
		case LockWrite:
			err = r.cfg.LockManager.AcquireWrite(m.ID(), lock.Path)
		}
		if err != nil {
			// Rollback any locks we already acquired in this batch
			for _, held := range acquired {
				switch held.Mode {
				case LockRead:
					r.cfg.LockManager.ReleaseRead(m.ID(), held.Path)
				case LockWrite:
					r.cfg.LockManager.ReleaseWrite(m.ID(), held.Path)
				}
			}
			if conflictErr, ok := err.(*locks.LockConflictError); ok {
				return LockWaiting{
					Path:   conflictErr.Path,
					HeldBy: conflictErr.Holder,
					Mode:   lock.Mode,
				}, nil
			}
			return nil, err
		}
		acquired = append(acquired, lock)
	}

	return LocksAcquired{}, nil
}

// executeDelegation spawns child machines in parallel for each sub-agent task.
func (r *Runtime) executeDelegation(ctx context.Context, parent *Observable, act DelegateToSubAgents) (Event, error) {
	if len(act.Tasks) == 0 {
		return SubAgentsCompleted{}, nil
	}

	if r.depth >= r.cfg.MaxDelegationDepth {
		return SubAgentsCompleted{Results: []SubAgentResult{{
			AgentID: parent.ID(),
			Summary: fmt.Sprintf("delegation depth limit reached (%d)", r.cfg.MaxDelegationDepth),
			Success: false,
		}}}, nil
	}

	results := make([]SubAgentResult, len(act.Tasks))
	var wg sync.WaitGroup

	for i, task := range act.Tasks {
		i, task := i, task
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Append index to prevent collisions when tasks share names
			childID := fmt.Sprintf("%s/%s-%d", parent.ID(), task.Task, i)
			modality := task.Modality
			if modality == 0 {
				modality = Classic
			}

			// Apply budget constraints
			childCtx := ctx
			if task.Budget.MaxWallTime > 0 {
				var cancel context.CancelFunc
				childCtx, cancel = context.WithTimeout(ctx, time.Duration(task.Budget.MaxWallTime)*time.Second)
				defer cancel()
			}

			// Create a child runtime with budget-scoped iteration limit and incremented depth
			childCfg := r.cfg
			if task.Budget.MaxIterations > 0 {
				childCfg.MaxIterations = task.Budget.MaxIterations
			}
			childRT := &Runtime{cfg: childCfg, depth: r.depth + 1, steering: make(map[string]string)}

			result, err := childRT.runChild(childCtx, childID, modality, parent.ID(), task)

			// Release any locks held by this child
			if r.cfg.LockManager != nil {
				r.cfg.LockManager.ReleaseAll(childID)
			}

			if err != nil {
				results[i] = SubAgentResult{
					AgentID: childID,
					Summary: fmt.Sprintf("failed: %v", err),
					Success: false,
				}
			} else {
				results[i] = SubAgentResult{
					AgentID:    childID,
					Summary:    result.Content,
					TokensUsed: result.TokensUsed,
					Success:    result.FinalState == Done,
				}
			}
		}()
	}

	wg.Wait()
	return SubAgentsCompleted{Results: results}, nil
}

// runChild creates and drives a child machine with parent telemetry tracking.
func (r *Runtime) runChild(ctx context.Context, id string, modality Modality, parentID string, task SubAgentTask) (*RuntimeResult, error) {
	m := NewObservableWithParent(id, modality, parentID, task.Task, task.Model, r.cfg.Hub)

	input := task.Spec
	if input == "" {
		input = task.Task
	}

	return r.drive(ctx, m, input)
}

// executeReview checks sub-agent results and returns a pass/fail verdict.
func (r *Runtime) executeReview(act ReviewSubAgentOutput) Event {
	var failedAgents []string
	for _, result := range act.Results {
		if !result.Success {
			failedAgents = append(failedAgents, result.AgentID)
		}
	}

	if len(failedAgents) > 0 {
		return ReviewResult{
			Passed: false,
			Reason: fmt.Sprintf("sub-agents failed: %s", strings.Join(failedAgents, ", ")),
		}
	}

	return ReviewResult{Passed: true}
}

func (r *Runtime) buildResult(m *Observable, actions []Action, iterations int) *RuntimeResult {
	result := &RuntimeResult{
		FinalState: m.State(),
		Iterations: iterations,
	}
	for _, a := range actions {
		if emit, ok := a.(EmitResult); ok {
			result.Content = emit.Content
			result.TokensUsed = emit.TokensUsed
		}
	}
	return result
}
