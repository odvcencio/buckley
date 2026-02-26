package machine

import "sort"

// Machine is a pure state machine with no I/O. Given a State and an Event,
// it produces a new State and zero or more Actions for the Runtime to execute.
type Machine struct {
	id       string
	modality Modality
	state    State

	// Pending tool calls from the last model response
	pendingCalls []ToolCallRequest

	// Queued user steering for the next model call
	pendingSteering string

	// Iteration counter (for RLM checkpoints and Ralph resets)
	iteration int
}

// New creates a Machine in the Idle state.
func New(id string, modality Modality) *Machine {
	return &Machine{
		id:       id,
		modality: modality,
		state:    Idle,
	}
}

func (m *Machine) ID() string               { return m.id }
func (m *Machine) Modality() Modality       { return m.modality }
func (m *Machine) State() State             { return m.state }
func (m *Machine) Iteration() int           { return m.iteration }
func (m *Machine) HasPendingSteering() bool { return m.pendingSteering != "" }

// Transition applies an event to the machine and returns the new state
// plus any actions the runtime should execute. This is the pure core —
// no I/O, no side effects, fully testable.
func (m *Machine) Transition(event Event) (State, []Action) {
	// Terminal states reject all events
	if m.state.IsTerminal() {
		return m.state, nil
	}

	// Global handlers that apply regardless of state
	switch e := event.(type) {
	case Cancelled:
		_ = e
		m.state = Error
		return m.state, []Action{EmitError{RetryStrategy: "none"}}

	case UserSteering:
		m.pendingSteering = e.Content
		return m.state, nil
	}

	// Delegate to modality-specific transitions for modality-owned states
	switch m.modality {
	case RLM:
		if next, actions, handled := m.transitionRLM(event); handled {
			return next, actions
		}
	case Ralph:
		if next, actions, handled := m.transitionRalph(event); handled {
			return next, actions
		}
	}

	// Shared transitions (all modalities)
	switch m.state {
	case Idle:
		return m.transitionIdle(event)
	case CallingModel:
		return m.transitionCallingModel(event)
	case AcquiringLocks:
		return m.transitionAcquiringLocks(event)
	case WaitingOnLock:
		return m.transitionWaitingOnLock(event)
	case ExecutingTools:
		return m.transitionExecutingTools(event)
	case Compacting:
		return m.transitionCompacting(event)
	}

	return m.state, nil
}

// --- Shared transitions ---

func (m *Machine) transitionIdle(event Event) (State, []Action) {
	switch event.(type) {
	case UserInput:
		m.state = CallingModel
		return m.state, []Action{m.buildCallModel()}
	}
	return m.state, nil
}

func (m *Machine) transitionCallingModel(event Event) (State, []Action) {
	switch e := event.(type) {
	case ModelCompleted:
		return m.handleModelCompleted(e)

	case ModelFailed:
		if e.Retryable {
			return m.state, []Action{m.buildCallModel()}
		}
		m.state = Error
		return m.state, []Action{EmitError{Err: e.Err, RetryStrategy: "none"}}

	case ContextPressure:
		m.state = Compacting
		return m.state, []Action{Compact{}}
	}

	return m.state, nil
}

func (m *Machine) handleModelCompleted(e ModelCompleted) (State, []Action) {
	if e.FinishReason == "tool_use" && len(e.ToolCalls) > 0 {
		m.pendingCalls = e.ToolCalls

		// Check for RLM delegation
		if m.modality == RLM && m.isRLMDelegation(e.ToolCalls) {
			m.state = Delegating
			return m.state, []Action{m.buildDelegation()}
		}

		locks := m.extractLocks(e.ToolCalls)
		if len(locks) > 0 {
			m.state = AcquiringLocks
			return m.state, []Action{AcquireLockBatch{Locks: locks}}
		}
		// No locks needed, execute directly
		m.state = ExecutingTools
		return m.state, []Action{ExecuteToolBatch{Calls: e.ToolCalls}}
	}

	// End turn — for Ralph, always commit+verify; for others, done
	if m.modality == Ralph {
		m.state = CommittingWork
		return m.state, []Action{CommitChanges{}}
	}

	m.state = Done
	return m.state, []Action{EmitResult{Content: e.Content, TokensUsed: e.TokensUsed}}
}

func (m *Machine) transitionAcquiringLocks(event Event) (State, []Action) {
	switch event.(type) {
	case LocksAcquired:
		m.state = ExecutingTools
		return m.state, []Action{ExecuteToolBatch{Calls: m.pendingCalls}}

	case LockWaiting:
		m.state = WaitingOnLock
		return m.state, nil
	}
	return m.state, nil
}

func (m *Machine) transitionWaitingOnLock(event Event) (State, []Action) {
	switch event.(type) {
	case LocksAcquired:
		m.state = ExecutingTools
		return m.state, []Action{ExecuteToolBatch{Calls: m.pendingCalls}}
	}
	return m.state, nil
}

func (m *Machine) transitionExecutingTools(event Event) (State, []Action) {
	switch event.(type) {
	case ToolsCompleted:
		m.pendingCalls = nil

		// Ralph: after tools, commit instead of looping
		if m.modality == Ralph {
			m.state = CommittingWork
			return m.state, []Action{ReleaseLocks{}, CommitChanges{}}
		}

		m.state = CallingModel
		return m.state, []Action{ReleaseLocks{}, m.buildCallModel()}
	}
	return m.state, nil
}

func (m *Machine) transitionCompacting(event Event) (State, []Action) {
	switch event.(type) {
	case CompactionCompleted:
		m.state = CallingModel
		return m.state, []Action{m.buildCallModel()}
	}
	return m.state, nil
}

// --- Helpers ---

func (m *Machine) buildCallModel() CallModel {
	cm := CallModel{}
	if m.pendingSteering != "" {
		cm.Steering = m.pendingSteering
		m.pendingSteering = ""
	}
	if m.modality == RLM {
		cm.EnableReasoning = true
	}
	return cm
}

// extractLocks deduplicates and sorts lock requests from tool calls.
// Sorted to prevent deadlocks when multiple machines acquire concurrently.
func (m *Machine) extractLocks(calls []ToolCallRequest) []LockRequest {
	seen := make(map[string]LockMode)
	for _, call := range calls {
		for _, path := range call.Paths {
			if existing, ok := seen[path]; ok {
				// Upgrade read to write if needed
				if call.Mode == LockWrite && existing == LockRead {
					seen[path] = LockWrite
				}
			} else {
				seen[path] = call.Mode
			}
		}
	}

	locks := make([]LockRequest, 0, len(seen))
	for path, mode := range seen {
		locks = append(locks, LockRequest{Path: path, Mode: mode})
	}
	sort.Slice(locks, func(i, j int) bool {
		return locks[i].Path < locks[j].Path
	})
	return locks
}
