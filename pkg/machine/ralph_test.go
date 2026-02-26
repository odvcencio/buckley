package machine

import "testing"

func TestMachine_Ralph_FullCycle(t *testing.T) {
	m := New("ralph-1", Ralph)
	m.Transition(UserInput{Content: "implement feature X"})

	// Model produces tool calls
	m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: "tc1", Name: "edit_file", Paths: []string{"x.go"}, Mode: LockWrite}},
	})
	m.Transition(LocksAcquired{})

	// Tools complete → Ralph commits (not loops)
	next, actions := m.Transition(ToolsCompleted{
		Results: []ToolCallResult{{ID: "tc1", Success: true}},
	})
	if next != CommittingWork {
		t.Errorf("state = %s, want committing_work", next)
	}
	// Should have ReleaseLocks + CommitChanges
	hasRelease, hasCommit := false, false
	for _, a := range actions {
		switch a.(type) {
		case ReleaseLocks:
			hasRelease = true
		case CommitChanges:
			hasCommit = true
		}
	}
	if !hasRelease || !hasCommit {
		t.Errorf("expected ReleaseLocks + CommitChanges, got %v", actions)
	}

	// Commit completes → verify
	next, actions = m.Transition(CommitCompleted{Hash: "abc123"})
	if next != Verifying {
		t.Errorf("state = %s, want verifying", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if _, ok := actions[0].(RunVerification); !ok {
		t.Errorf("action type = %T, want RunVerification", actions[0])
	}

	// Verification fails → reset context
	next, actions = m.Transition(VerificationResult{Passed: false, Output: "test failed"})
	if next != ResettingContext {
		t.Errorf("state = %s, want resetting_context", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	rc, ok := actions[0].(ResetContext)
	if !ok {
		t.Fatalf("action type = %T, want ResetContext", actions[0])
	}
	if rc.LastError != "test failed" {
		t.Errorf("last error = %q, want %q", rc.LastError, "test failed")
	}
	if rc.Iteration != 1 {
		t.Errorf("iteration = %d, want 1", rc.Iteration)
	}

	// Context reset → back to calling model
	next, actions = m.Transition(ContextResetDone{Iteration: 1})
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

func TestMachine_Ralph_VerificationPasses(t *testing.T) {
	m := New("ralph-1", Ralph)
	m.Transition(UserInput{Content: "fix bug"})
	m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: "1", Name: "edit_file"}},
	})
	m.Transition(LocksAcquired{})
	m.Transition(ToolsCompleted{Results: []ToolCallResult{{ID: "1", Success: true}}})
	m.Transition(CommitCompleted{Hash: "abc"})

	next, actions := m.Transition(VerificationResult{Passed: true, Output: "all tests pass"})
	if next != Done {
		t.Errorf("state = %s, want done", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if _, ok := actions[0].(EmitResult); !ok {
		t.Errorf("action type = %T, want EmitResult", actions[0])
	}
}

func TestMachine_Ralph_ModelEndTurnCommits(t *testing.T) {
	m := New("ralph-1", Ralph)
	m.Transition(UserInput{Content: "fix bug"})

	// Model says end_turn — Ralph still commits+verifies
	next, actions := m.Transition(ModelCompleted{
		Content:      "I've fixed it",
		FinishReason: "end_turn",
	})
	if next != CommittingWork {
		t.Errorf("state = %s, want committing_work (Ralph always commits)", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if _, ok := actions[0].(CommitChanges); !ok {
		t.Errorf("action type = %T, want CommitChanges", actions[0])
	}
}

func TestMachine_Ralph_IterationCounts(t *testing.T) {
	m := New("ralph-1", Ralph)

	// First iteration
	m.Transition(UserInput{Content: "fix"})
	m.Transition(ModelCompleted{FinishReason: "tool_use", ToolCalls: []ToolCallRequest{{ID: "1", Name: "e"}}})
	m.Transition(LocksAcquired{})
	m.Transition(ToolsCompleted{Results: []ToolCallResult{{ID: "1", Success: true}}})
	m.Transition(CommitCompleted{Hash: "a"})
	m.Transition(VerificationResult{Passed: false, Output: "fail"})
	if m.Iteration() != 1 {
		t.Errorf("iteration = %d, want 1 after first failure", m.Iteration())
	}

	// Second iteration
	m.Transition(ContextResetDone{Iteration: 1})
	m.Transition(ModelCompleted{FinishReason: "tool_use", ToolCalls: []ToolCallRequest{{ID: "2", Name: "e"}}})
	m.Transition(LocksAcquired{})
	m.Transition(ToolsCompleted{Results: []ToolCallResult{{ID: "2", Success: true}}})
	m.Transition(CommitCompleted{Hash: "b"})
	m.Transition(VerificationResult{Passed: false, Output: "fail again"})
	if m.Iteration() != 2 {
		t.Errorf("iteration = %d, want 2 after second failure", m.Iteration())
	}
}
