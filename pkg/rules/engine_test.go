package rules

import (
	"testing"
)

func mustNewTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

func TestEngine_EvalComplexity(t *testing.T) {
	e := mustNewTestEngine(t)

	tests := []struct {
		name       string
		facts      TaskFacts
		wantAction string
	}{
		{
			name: "high complexity triggers Plan",
			facts: TaskFacts{
				WordCount:    60,
				HasQuestions: true,
				Ambiguity:    0.8,
			},
			wantAction: "Plan",
		},
		{
			name: "simple task triggers Direct",
			facts: TaskFacts{
				WordCount: 3,
			},
			wantAction: "Direct",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matched, err := Eval(e, "complexity", tt.facts)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if len(matched) == 0 {
				t.Fatal("expected at least one matched rule")
			}
			if matched[0].Action != tt.wantAction {
				t.Errorf("got action %q, want %q", matched[0].Action, tt.wantAction)
			}
		})
	}
}

func TestEngine_EvalStrategy_Approval(t *testing.T) {
	e := mustNewTestEngine(t)

	result, err := e.EvalStrategy("approval", "approval_gate", map[string]any{
		"approval": map[string]any{"mode": "yolo"},
		"risk":     map[string]any{"level": "critical"},
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	action, ok := result.Params["action"]
	if !ok {
		t.Fatal("expected 'action' in result params")
	}
	if action != "allow" {
		t.Errorf("got action %q, want %q", action, "allow")
	}
}

func TestEngine_EvalRetry(t *testing.T) {
	e := mustNewTestEngine(t)

	facts := RetryFacts{
		Attempt:       3,
		MaxAttempts:   3,
		SameError:     true,
		NoFileChanges: true,
	}
	matched, err := Eval(e, "retry", facts)
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if len(matched) == 0 {
		t.Fatal("expected at least one matched rule")
	}
	if matched[0].Action != "Abort" {
		t.Errorf("got action %q, want %q", matched[0].Action, "Abort")
	}
}

func TestEngine_Reload(t *testing.T) {
	e := mustNewTestEngine(t)

	if err := e.Reload("complexity"); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	// Verify the reloaded domain still works.
	matched, err := Eval(e, "complexity", TaskFacts{WordCount: 3})
	if err != nil {
		t.Fatalf("Eval after reload: %v", err)
	}
	if len(matched) == 0 {
		t.Fatal("expected at least one matched rule after reload")
	}
}
