package machine

// transitionRalph handles Ralph-specific state transitions.
// Returns (state, actions, handled). If handled is false, the caller
// should fall through to shared transitions.
func (m *Machine) transitionRalph(event Event) (State, []Action, bool) {
	switch m.state {
	case CommittingWork:
		return m.transitionCommittingWork(event)
	case Verifying:
		return m.transitionVerifying(event)
	case ResettingContext:
		return m.transitionResettingContext(event)
	}
	return m.state, nil, false
}

func (m *Machine) transitionCommittingWork(event Event) (State, []Action, bool) {
	switch event.(type) {
	case CommitCompleted:
		m.state = Verifying
		return m.state, []Action{RunVerification{}}, true
	}
	return m.state, nil, true
}

func (m *Machine) transitionVerifying(event Event) (State, []Action, bool) {
	switch e := event.(type) {
	case VerificationResult:
		if e.Passed {
			m.state = Done
			return m.state, []Action{EmitResult{Content: "verification passed"}}, true
		}
		m.state = ResettingContext
		m.iteration++
		return m.state, []Action{ResetContext{
			LastError: e.Output,
			Iteration: m.iteration,
		}}, true
	}
	return m.state, nil, true
}

func (m *Machine) transitionResettingContext(event Event) (State, []Action, bool) {
	switch event.(type) {
	case ContextResetDone:
		m.state = CallingModel
		return m.state, []Action{m.buildCallModel()}, true
	}
	return m.state, nil, true
}
