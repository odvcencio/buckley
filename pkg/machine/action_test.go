package machine

import "testing"

func TestAction_Types(t *testing.T) {
	actions := []Action{
		CallModel{},
		ExecuteToolBatch{},
		AcquireLockBatch{},
		ReleaseLocks{},
		Compact{},
		SpawnSubAgent{},
		CommitChanges{},
		RunVerification{},
		ResetContext{},
		EmitResult{},
		EmitError{},
		DelegateToSubAgents{},
		ReviewSubAgentOutput{},
		SaveCheckpoint{},
	}
	for _, a := range actions {
		if a.actionType() == "" {
			t.Errorf("action %T has empty type", a)
		}
	}
}
