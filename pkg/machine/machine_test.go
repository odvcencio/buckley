package machine

import "testing"

func TestMachine_Classic_IdleToCallingModel(t *testing.T) {
	m := New("agent-1", Classic)
	if m.State() != Idle {
		t.Fatalf("initial state = %s, want idle", m.State())
	}

	next, actions := m.Transition(UserInput{Content: "hello"})
	if next != CallingModel {
		t.Errorf("state = %s, want calling_model", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if _, ok := actions[0].(CallModel); !ok {
		t.Errorf("action type = %T, want CallModel", actions[0])
	}
}

func TestMachine_Classic_ModelCompletedWithToolCalls(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "edit foo.go"})

	calls := []ToolCallRequest{{
		ID: "tc1", Name: "edit_file",
		Paths: []string{"foo.go"}, Mode: LockWrite,
	}}
	next, actions := m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    calls,
	})
	if next != AcquiringLocks {
		t.Errorf("state = %s, want acquiring_locks", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	batch, ok := actions[0].(AcquireLockBatch)
	if !ok {
		t.Fatalf("action type = %T, want AcquireLockBatch", actions[0])
	}
	if len(batch.Locks) != 1 || batch.Locks[0].Path != "foo.go" {
		t.Error("expected lock on foo.go")
	}
}

func TestMachine_Classic_ToolCallsNoPaths(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "think about it"})

	next, actions := m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: "tc1", Name: "think"}},
	})
	// No paths means no locks needed, go straight to executing
	if next != ExecutingTools {
		t.Errorf("state = %s, want executing_tools", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if _, ok := actions[0].(ExecuteToolBatch); !ok {
		t.Errorf("action type = %T, want ExecuteToolBatch", actions[0])
	}
}

func TestMachine_Classic_LocksAcquiredToExecutingTools(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "edit foo.go"})
	m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: "tc1", Name: "edit_file", Paths: []string{"foo.go"}, Mode: LockWrite}},
	})

	next, actions := m.Transition(LocksAcquired{})
	if next != ExecutingTools {
		t.Errorf("state = %s, want executing_tools", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if _, ok := actions[0].(ExecuteToolBatch); !ok {
		t.Errorf("action type = %T, want ExecuteToolBatch", actions[0])
	}
}

func TestMachine_Classic_ToolsCompletedLoopsBack(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "edit foo.go"})
	m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: "tc1", Name: "edit_file"}},
	})
	m.Transition(LocksAcquired{})

	next, actions := m.Transition(ToolsCompleted{
		Results: []ToolCallResult{{ID: "tc1", Success: true}},
	})
	if next != CallingModel {
		t.Errorf("state = %s, want calling_model", next)
	}
	if len(actions) != 2 {
		t.Fatalf("actions len = %d, want 2 (ReleaseLocks + CallModel)", len(actions))
	}
}

func TestMachine_Classic_ModelCompletedDone(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "hello"})

	next, actions := m.Transition(ModelCompleted{
		Content:      "Hi there!",
		FinishReason: "end_turn",
	})
	if next != Done {
		t.Errorf("state = %s, want done", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	result, ok := actions[0].(EmitResult)
	if !ok {
		t.Fatalf("action type = %T, want EmitResult", actions[0])
	}
	if result.Content != "Hi there!" {
		t.Errorf("content = %q, want %q", result.Content, "Hi there!")
	}
}

func TestMachine_Classic_WaitingOnLockThenAcquired(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "edit foo.go"})
	m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: "tc1", Name: "edit_file", Paths: []string{"foo.go"}, Mode: LockWrite}},
	})
	m.Transition(LockWaiting{Path: "foo.go", HeldBy: "agent-2"})

	if m.State() != WaitingOnLock {
		t.Fatalf("state = %s, want waiting_on_lock", m.State())
	}

	next, actions := m.Transition(LocksAcquired{})
	if next != ExecutingTools {
		t.Errorf("state = %s, want executing_tools", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
}

func TestMachine_Classic_ContextPressureTriggersCompaction(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "hello"})

	next, actions := m.Transition(ContextPressure{
		UsedTokens: 180000, MaxTokens: 200000, Ratio: 0.9,
	})
	if next != Compacting {
		t.Errorf("state = %s, want compacting", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if _, ok := actions[0].(Compact); !ok {
		t.Errorf("action type = %T, want Compact", actions[0])
	}
}

func TestMachine_Classic_CompactionCompletedResumes(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "hello"})
	m.Transition(ContextPressure{UsedTokens: 180000, MaxTokens: 200000, Ratio: 0.9})

	next, actions := m.Transition(CompactionCompleted{TokensSaved: 50000})
	if next != CallingModel {
		t.Errorf("state = %s, want calling_model", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if _, ok := actions[0].(CallModel); !ok {
		t.Errorf("action type = %T, want CallModel", actions[0])
	}
}

func TestMachine_Classic_SteeringQueued(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "edit foo.go"})
	m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: "tc1", Name: "edit_file"}},
	})
	m.Transition(LocksAcquired{})

	next, _ := m.Transition(UserSteering{Content: "use JWT instead"})
	if next != ExecutingTools {
		t.Errorf("state = %s, want executing_tools (steering should queue)", next)
	}
	if !m.HasPendingSteering() {
		t.Error("expected pending steering")
	}
}

func TestMachine_Classic_SteeringIncludedInNextModelCall(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "edit foo.go"})

	// Queue steering while calling model
	m.Transition(UserSteering{Content: "use JWT instead"})
	if !m.HasPendingSteering() {
		t.Fatal("expected pending steering")
	}

	// Model completes with tool use, tools run, then next model call should include steering
	m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: "tc1", Name: "edit_file"}},
	})
	m.Transition(LocksAcquired{})
	_, actions := m.Transition(ToolsCompleted{Results: []ToolCallResult{{ID: "tc1", Success: true}}})

	// Find the CallModel action and check steering
	for _, a := range actions {
		if cm, ok := a.(CallModel); ok {
			if cm.Steering != "use JWT instead" {
				t.Errorf("steering = %q, want %q", cm.Steering, "use JWT instead")
			}
			if m.HasPendingSteering() {
				t.Error("steering should be consumed after inclusion in model call")
			}
			return
		}
	}
	t.Error("expected CallModel action")
}

func TestMachine_Classic_CancelledFromAnyState(t *testing.T) {
	states := []struct {
		name  string
		setup func(m *Machine)
	}{
		{"idle", func(m *Machine) {}},
		{"calling_model", func(m *Machine) { m.Transition(UserInput{Content: "hi"}) }},
		{"executing_tools", func(m *Machine) {
			m.Transition(UserInput{Content: "x"})
			m.Transition(ModelCompleted{FinishReason: "tool_use", ToolCalls: []ToolCallRequest{{ID: "1", Name: "t"}}})
			m.Transition(LocksAcquired{})
		}},
	}
	for _, tt := range states {
		t.Run(tt.name, func(t *testing.T) {
			m := New("a", Classic)
			tt.setup(m)
			next, actions := m.Transition(Cancelled{})
			if next != Error {
				t.Errorf("state = %s, want error", next)
			}
			if len(actions) < 1 {
				t.Fatal("expected at least EmitError action")
			}
		})
	}
}

func TestMachine_IsTerminal(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "hi"})
	m.Transition(ModelCompleted{Content: "hello", FinishReason: "end_turn"})

	if !m.State().IsTerminal() {
		t.Error("machine should be in terminal state")
	}

	next, actions := m.Transition(UserInput{Content: "more"})
	if next != Done {
		t.Errorf("state = %s, want done (no transition from terminal)", next)
	}
	if len(actions) != 0 {
		t.Error("expected no actions from terminal state")
	}
}

func TestMachine_ModelFailed_Retryable(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "hello"})

	next, actions := m.Transition(ModelFailed{Retryable: true})
	if next != CallingModel {
		t.Errorf("state = %s, want calling_model (retryable)", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if _, ok := actions[0].(CallModel); !ok {
		t.Errorf("action type = %T, want CallModel", actions[0])
	}
}

func TestMachine_ModelFailed_NotRetryable(t *testing.T) {
	m := New("agent-1", Classic)
	m.Transition(UserInput{Content: "hello"})

	next, actions := m.Transition(ModelFailed{Retryable: false})
	if next != Error {
		t.Errorf("state = %s, want error", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if _, ok := actions[0].(EmitError); !ok {
		t.Errorf("action type = %T, want EmitError", actions[0])
	}
}
