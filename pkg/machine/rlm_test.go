package machine

import "testing"

func TestMachine_RLM_DelegateFlow(t *testing.T) {
	m := New("coord", RLM)
	m.Transition(UserInput{Content: "build auth system"})

	next, actions := m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls: []ToolCallRequest{{
			ID:     "tc1",
			Name:   "delegate",
			Params: map[string]any{"task": "implement JWT auth", "model": "gpt-4"},
		}},
	})
	if next != Delegating {
		t.Errorf("state = %s, want delegating", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	del, ok := actions[0].(DelegateToSubAgents)
	if !ok {
		t.Fatalf("action type = %T, want DelegateToSubAgents", actions[0])
	}
	if len(del.Tasks) != 1 {
		t.Fatalf("tasks len = %d, want 1", len(del.Tasks))
	}
	if del.Tasks[0].Task != "implement JWT auth" {
		t.Errorf("task = %q, want %q", del.Tasks[0].Task, "implement JWT auth")
	}
}

func TestMachine_RLM_ReviewPassFlow(t *testing.T) {
	m := New("coord", RLM)
	m.Transition(UserInput{Content: "build auth"})
	m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: "1", Name: "delegate", Params: map[string]any{"task": "t"}}},
	})

	// Sub-agents complete
	m.Transition(SubAgentsCompleted{
		Results: []SubAgentResult{{AgentID: "sub-1", Summary: "done", Success: true}},
	})
	if m.State() != Reviewing {
		t.Fatalf("state = %s, want reviewing", m.State())
	}

	// Review passes
	next, _ := m.Transition(ReviewResult{Passed: true})
	if next != Synthesizing {
		t.Errorf("state = %s, want synthesizing", next)
	}
}

func TestMachine_RLM_ReviewRejectFlow(t *testing.T) {
	m := New("coord", RLM)
	m.Transition(UserInput{Content: "build auth"})
	m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: "1", Name: "delegate", Params: map[string]any{"task": "t"}}},
	})
	m.Transition(SubAgentsCompleted{
		Results: []SubAgentResult{{AgentID: "sub-1", Summary: "bad work", Success: false}},
	})

	// Review fails
	next, _ := m.Transition(ReviewResult{Passed: false, Reason: "incomplete"})
	if next != Rejecting {
		t.Errorf("state = %s, want rejecting", next)
	}

	// Rejection triggers re-delegation
	next, actions := m.Transition(ReviewResult{}) // any event triggers re-delegation from Rejecting
	if next != Delegating {
		t.Errorf("state = %s, want delegating", next)
	}
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	if _, ok := actions[0].(DelegateToSubAgents); !ok {
		t.Errorf("action type = %T, want DelegateToSubAgents", actions[0])
	}
	if m.Iteration() != 1 {
		t.Errorf("iteration = %d, want 1", m.Iteration())
	}
}

func TestMachine_RLM_SynthesizeToDone(t *testing.T) {
	m := New("coord", RLM)
	m.Transition(UserInput{Content: "build auth"})
	m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: "1", Name: "delegate", Params: map[string]any{"task": "t"}}},
	})
	m.Transition(SubAgentsCompleted{
		Results: []SubAgentResult{{AgentID: "sub-1", Success: true}},
	})
	m.Transition(ReviewResult{Passed: true})

	// Synthesize
	next, actions := m.Transition(SynthesisCompleted{Content: "auth system complete"})
	if next != CheckpointingProgress {
		t.Errorf("state = %s, want checkpointing_progress", next)
	}
	if len(actions) != 2 {
		t.Fatalf("actions len = %d, want 2 (EmitResult + SaveCheckpoint)", len(actions))
	}
	if _, ok := actions[0].(EmitResult); !ok {
		t.Errorf("action[0] type = %T, want EmitResult", actions[0])
	}

	// Checkpoint saved
	next, _ = m.Transition(CheckpointSaved{})
	if next != Done {
		t.Errorf("state = %s, want done", next)
	}
}

func TestMachine_RLM_NonDelegateToolCallsUseSharedPath(t *testing.T) {
	m := New("coord", RLM)
	m.Transition(UserInput{Content: "read file"})

	// Regular tool call (not delegate) should use shared lock path
	next, _ := m.Transition(ModelCompleted{
		FinishReason: "tool_use",
		ToolCalls:    []ToolCallRequest{{ID: "1", Name: "read_file", Paths: []string{"foo.go"}, Mode: LockRead}},
	})
	if next != AcquiringLocks {
		t.Errorf("state = %s, want acquiring_locks (shared path for non-delegate)", next)
	}
}

func TestMachine_RLM_EnablesReasoning(t *testing.T) {
	m := New("coord", RLM)
	_, actions := m.Transition(UserInput{Content: "plan"})
	if len(actions) != 1 {
		t.Fatalf("actions len = %d, want 1", len(actions))
	}
	cm, ok := actions[0].(CallModel)
	if !ok {
		t.Fatalf("action type = %T, want CallModel", actions[0])
	}
	if !cm.EnableReasoning {
		t.Error("RLM should enable reasoning")
	}
}
