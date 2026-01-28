// pkg/ralph/session_test.go
package ralph

import (
	"testing"
)

func TestSession_StateTransitions(t *testing.T) {
	sess := NewSession(SessionConfig{
		Prompt:    "Build a todo app",
		Sandbox:   "/tmp/test-sandbox",
		SessionID: "test-123",
	})

	if sess.State() != StateInit {
		t.Fatalf("expected StateInit, got %s", sess.State())
	}

	// Transition to running
	if err := sess.TransitionTo(StateRunning); err != nil {
		t.Fatalf("transition to running failed: %v", err)
	}
	if sess.State() != StateRunning {
		t.Fatalf("expected StateRunning, got %s", sess.State())
	}

	// Pause
	if err := sess.TransitionTo(StatePaused); err != nil {
		t.Fatalf("transition to paused failed: %v", err)
	}

	// Resume
	if err := sess.TransitionTo(StateRunning); err != nil {
		t.Fatalf("transition to running failed: %v", err)
	}

	// Complete
	if err := sess.TransitionTo(StateCompleted); err != nil {
		t.Fatalf("transition to completed failed: %v", err)
	}

	// Cannot transition from completed
	if err := sess.TransitionTo(StateRunning); err == nil {
		t.Fatal("expected error transitioning from completed state")
	}
}

func TestSession_InvalidTransition(t *testing.T) {
	sess := NewSession(SessionConfig{
		Prompt:    "test",
		Sandbox:   "/tmp/test",
		SessionID: "test-456",
	})

	// Cannot go directly from init to completed
	if err := sess.TransitionTo(StateCompleted); err == nil {
		t.Fatal("expected error for invalid transition")
	}
}

func TestSession_RefiningPath(t *testing.T) {
	sess := NewSession(SessionConfig{
		Prompt:    "test",
		Sandbox:   "/tmp/test",
		SessionID: "test-789",
	})

	// Init -> Refining -> Running -> Completed
	if err := sess.TransitionTo(StateRefining); err != nil {
		t.Fatalf("transition to refining failed: %v", err)
	}
	if err := sess.TransitionTo(StateRunning); err != nil {
		t.Fatalf("transition to running failed: %v", err)
	}
	if err := sess.TransitionTo(StateCompleted); err != nil {
		t.Fatalf("transition to completed failed: %v", err)
	}
}

func TestSession_Iteration(t *testing.T) {
	sess := NewSession(SessionConfig{
		Prompt:    "test",
		Sandbox:   "/tmp/test",
		SessionID: "test-iter",
	})

	if sess.Iteration() != 0 {
		t.Fatalf("expected iteration 0, got %d", sess.Iteration())
	}

	n := sess.IncrementIteration()
	if n != 1 {
		t.Fatalf("expected increment to return 1, got %d", n)
	}
	if sess.Iteration() != 1 {
		t.Fatalf("expected iteration 1, got %d", sess.Iteration())
	}

	sess.IncrementIteration()
	sess.IncrementIteration()
	if sess.Iteration() != 3 {
		t.Fatalf("expected iteration 3, got %d", sess.Iteration())
	}
}

func TestSession_Stats(t *testing.T) {
	sess := NewSession(SessionConfig{
		Prompt:    "test",
		Sandbox:   "/tmp/test",
		SessionID: "test-stats",
	})

	sess.Start()
	sess.IncrementIteration()
	sess.AddTokens(1000, 0.05)
	sess.AddTokens(500, 0.025)
	sess.AddModifiedFile("/tmp/test/main.go")
	sess.AddModifiedFile("/tmp/test/utils.go")

	stats := sess.Stats()

	if stats.Iteration != 1 {
		t.Errorf("expected iteration 1, got %d", stats.Iteration)
	}
	if stats.TotalTokens != 1500 {
		t.Errorf("expected 1500 tokens, got %d", stats.TotalTokens)
	}
	expectedCost := 0.075
	if stats.TotalCost < expectedCost-0.0001 || stats.TotalCost > expectedCost+0.0001 {
		t.Errorf("expected cost ~0.075, got %f", stats.TotalCost)
	}
	if stats.FilesModified != 2 {
		t.Errorf("expected 2 files modified, got %d", stats.FilesModified)
	}
	if stats.Elapsed <= 0 {
		t.Errorf("expected positive elapsed time, got %v", stats.Elapsed)
	}
}

func TestState_CanTransitionTo(t *testing.T) {
	tests := []struct {
		name     string
		from     State
		to       State
		expected bool
	}{
		{"init to refining", StateInit, StateRefining, true},
		{"init to running", StateInit, StateRunning, true},
		{"init to paused", StateInit, StatePaused, false},
		{"init to completed", StateInit, StateCompleted, false},
		{"refining to running", StateRefining, StateRunning, true},
		{"refining to completed", StateRefining, StateCompleted, true},
		{"running to paused", StateRunning, StatePaused, true},
		{"running to completed", StateRunning, StateCompleted, true},
		{"running to init", StateRunning, StateInit, false},
		{"paused to running", StatePaused, StateRunning, true},
		{"paused to completed", StatePaused, StateCompleted, true},
		{"completed to anything", StateCompleted, StateRunning, false},
		{"unknown state", State("unknown"), StateRunning, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.from.CanTransitionTo(tt.to)
			if got != tt.expected {
				t.Errorf("CanTransitionTo(%s, %s) = %v, want %v", tt.from, tt.to, got, tt.expected)
			}
		})
	}
}

func TestErrInvalidTransition_Error(t *testing.T) {
	err := ErrInvalidTransition{From: StateInit, To: StateCompleted}
	expected := "invalid state transition: init -> completed"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestState_String(t *testing.T) {
	tests := []struct {
		state    State
		expected string
	}{
		{StateInit, "init"},
		{StateRefining, "refining"},
		{StateRunning, "running"},
		{StatePaused, "paused"},
		{StateCompleted, "completed"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if tt.state.String() != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, tt.state.String())
			}
		})
	}
}
