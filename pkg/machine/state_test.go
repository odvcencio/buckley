package machine

import "testing"

func TestState_String(t *testing.T) {
	tests := []struct {
		state State
		want  string
	}{
		{Idle, "idle"},
		{CallingModel, "calling_model"},
		{ProcessingResponse, "processing_response"},
		{AcquiringLocks, "acquiring_locks"},
		{ExecutingTools, "executing_tools"},
		{WaitingOnLock, "waiting_on_lock"},
		{Compacting, "compacting"},
		{Delegating, "delegating"},
		{AwaitingSubAgents, "awaiting_sub_agents"},
		{Reviewing, "reviewing"},
		{Rejecting, "rejecting"},
		{Synthesizing, "synthesizing"},
		{CheckpointingProgress, "checkpointing_progress"},
		{CommittingWork, "committing_work"},
		{Verifying, "verifying"},
		{ResettingContext, "resetting_context"},
		{Done, "done"},
		{Error, "error"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.state.String(); got != tt.want {
				t.Errorf("State.String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestState_IsTerminal(t *testing.T) {
	if !Done.IsTerminal() {
		t.Error("Done should be terminal")
	}
	if !Error.IsTerminal() {
		t.Error("Error should be terminal")
	}
	if CallingModel.IsTerminal() {
		t.Error("CallingModel should not be terminal")
	}
}
