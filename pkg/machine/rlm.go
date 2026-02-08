package machine

// transitionRLM handles RLM-specific state transitions.
// Returns (state, actions, handled). If handled is false, the caller
// should fall through to shared transitions.
func (m *Machine) transitionRLM(event Event) (State, []Action, bool) {
	switch m.state {
	case Delegating:
		return m.transitionDelegating(event)
	case AwaitingSubAgents:
		return m.transitionAwaitingSubAgents(event)
	case Reviewing:
		return m.transitionReviewing(event)
	case Rejecting:
		return m.transitionRejecting(event)
	case Synthesizing:
		return m.transitionSynthesizing(event)
	case CheckpointingProgress:
		return m.transitionCheckpointing(event)
	}
	return m.state, nil, false
}

func (m *Machine) transitionDelegating(event Event) (State, []Action, bool) {
	switch e := event.(type) {
	case SubAgentsCompleted:
		m.state = Reviewing
		return m.state, []Action{ReviewSubAgentOutput{Results: e.Results}}, true
	}
	return m.state, nil, true
}

func (m *Machine) transitionAwaitingSubAgents(event Event) (State, []Action, bool) {
	switch e := event.(type) {
	case SubAgentsCompleted:
		m.state = Reviewing
		return m.state, []Action{ReviewSubAgentOutput{Results: e.Results}}, true
	}
	return m.state, nil, true
}

func (m *Machine) transitionReviewing(event Event) (State, []Action, bool) {
	switch e := event.(type) {
	case ReviewResult:
		if e.Passed {
			m.state = Synthesizing
			// Produce a CallModel so the runtime can synthesize a final summary.
			return m.state, []Action{CallModel{EnableReasoning: true}}, true
		}
		m.state = Rejecting
		return m.state, nil, true
	}
	return m.state, nil, true
}

func (m *Machine) transitionRejecting(_ Event) (State, []Action, bool) {
	// After rejection, re-delegate with feedback
	m.state = Delegating
	m.iteration++
	return m.state, []Action{m.buildDelegation()}, true
}

func (m *Machine) transitionSynthesizing(event Event) (State, []Action, bool) {
	switch e := event.(type) {
	case SynthesisCompleted:
		m.state = CheckpointingProgress
		m.iteration++
		return m.state, []Action{
			EmitResult{Content: e.Content},
			SaveCheckpoint{Iteration: m.iteration},
		}, true
	case ModelCompleted:
		// CallModel produces ModelCompleted; treat end_turn as synthesis done.
		if e.FinishReason == "end_turn" || e.FinishReason == "" {
			m.state = CheckpointingProgress
			m.iteration++
			return m.state, []Action{
				EmitResult{Content: e.Content, TokensUsed: e.TokensUsed},
				SaveCheckpoint{Iteration: m.iteration},
			}, true
		}
	}
	return m.state, nil, true
}

func (m *Machine) transitionCheckpointing(event Event) (State, []Action, bool) {
	switch event.(type) {
	case CheckpointSaved:
		m.state = Done
		return m.state, nil, true
	}
	return m.state, nil, true
}

// isRLMDelegation detects if tool calls contain a delegation request.
func (m *Machine) isRLMDelegation(calls []ToolCallRequest) bool {
	for _, call := range calls {
		if call.Name == "delegate" || call.Name == "delegate_batch" {
			return true
		}
	}
	return false
}

// buildDelegation creates a DelegateToSubAgents action from pending calls.
func (m *Machine) buildDelegation() DelegateToSubAgents {
	var tasks []SubAgentTask
	for _, call := range m.pendingCalls {
		task := SubAgentTask{
			Task: call.Name,
		}
		if t, ok := call.Params["task"].(string); ok {
			task.Task = t
		}
		if model, ok := call.Params["model"].(string); ok {
			task.Model = model
		}
		tasks = append(tasks, task)
	}
	return DelegateToSubAgents{Tasks: tasks}
}
