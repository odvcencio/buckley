package machine

import (
	"context"
	"fmt"
	"sync"

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

// RuntimeConfig holds dependencies for the Runtime.
type RuntimeConfig struct {
	Hub            *telemetry.Hub
	ModelClient    ModelCaller
	ToolExecutor   ToolBatchExecutor
	LockManager    *locks.Manager
	Compactor      Compactor
	CommitExecutor CommitExecutor
	ShellExecutor  ShellExecutor
	MaxIterations  int
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
	cfg RuntimeConfig

	mu       sync.Mutex
	steering map[string]string // agentID → queued steering
}

// NewRuntime creates a new runtime executor.
func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = defaultMaxIterations
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

	// Start with user input
	_, actions := m.Transition(UserInput{Content: input})

	iterations := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("machine %s cancelled: %w", id, err)
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
		var err error

		// Execute actions and collect resulting events
		for _, action := range actions {
			event, err = r.executeAction(ctx, m, action)
			if err != nil {
				return nil, fmt.Errorf("machine %s action failed: %w", id, err)
			}
		}

		if event == nil {
			// No event from actions — machine should be terminal or waiting
			if m.State().IsTerminal() {
				return r.buildResult(m, nil, iterations), nil
			}
			return nil, fmt.Errorf("machine %s stuck in state %s with no event", id, m.State())
		}

		// Inject steering if queued
		if s := r.consumeSteering(id); s != "" {
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
		// TODO: spawn child machines
		var results []SubAgentResult
		for _, task := range act.Tasks {
			results = append(results, SubAgentResult{
				AgentID: task.Task,
				Summary: "pending implementation",
				Success: true,
			})
		}
		return SubAgentsCompleted{Results: results}, nil

	case ReviewSubAgentOutput:
		return ReviewResult{Passed: true}, nil

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

	for _, lock := range act.Locks {
		var err error
		switch lock.Mode {
		case LockRead:
			err = r.cfg.LockManager.AcquireRead(m.ID(), lock.Path)
		case LockWrite:
			err = r.cfg.LockManager.AcquireWrite(m.ID(), lock.Path)
		}
		if err != nil {
			// Could be a timeout — report as waiting
			if conflictErr, ok := err.(*locks.LockConflictError); ok {
				return LockWaiting{
					Path:   conflictErr.Path,
					HeldBy: conflictErr.Holder,
					Mode:   lock.Mode,
				}, nil
			}
			return nil, err
		}
	}

	return LocksAcquired{}, nil
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
