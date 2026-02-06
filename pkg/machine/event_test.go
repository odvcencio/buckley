package machine

import "testing"

func TestEvent_Type(t *testing.T) {
	tests := []struct {
		event Event
		want  string
	}{
		{ModelCompleted{FinishReason: "end_turn"}, "model_completed"},
		{ModelFailed{Retryable: true}, "model_failed"},
		{ToolsCompleted{}, "tools_completed"},
		{LockAcquired{Path: "foo.go"}, "lock_acquired"},
		{LocksAcquired{}, "locks_acquired"},
		{LockWaiting{Path: "foo.go", HeldBy: "b"}, "lock_waiting"},
		{LockReleased{Path: "foo.go"}, "lock_released"},
		{CompactionCompleted{TokensSaved: 100}, "compaction_completed"},
		{ContextPressure{Ratio: 0.9}, "context_pressure"},
		{UserSteering{Content: "try X"}, "user_steering"},
		{UserInput{Content: "hello"}, "user_input"},
		{SubAgentsCompleted{}, "sub_agents_completed"},
		{ReviewResult{Passed: true}, "review_result"},
		{SynthesisCompleted{Content: "done"}, "synthesis_completed"},
		{CheckpointSaved{}, "checkpoint_saved"},
		{CommitCompleted{Hash: "abc"}, "commit_completed"},
		{VerificationResult{Passed: true}, "verification_result"},
		{ContextResetDone{Iteration: 1}, "context_reset_done"},
		{Cancelled{}, "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.event.eventType(); got != tt.want {
				t.Errorf("event.eventType() = %q, want %q", got, tt.want)
			}
		})
	}
}
