package agentloop

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestControllerFinalizationFailureKeepsRawCauseOutOfLifecycleStopReason(t *testing.T) {
	const causeText = "provider finalization failed with MODEL_OUTPUT_SENTINEL"
	var events []LifecycleEvent
	controller := &Controller{cfg: ControllerConfig{
		LifecycleObserver: func(event LifecycleEvent) {
			events = append(events, event)
		},
	}}
	result := &Result{
		CompletionStatus: CompletionIncomplete,
		Termination: Termination{
			Kind:   "round_limit",
			Reason: "operator-facing reason remains available",
		},
	}

	_, err := controller.failFinalization(context.Background(), result, errors.New(causeText))
	if err == nil || !strings.Contains(err.Error(), causeText) {
		t.Fatalf("normal error path = %v, want raw cause retained", err)
	}
	if result.Termination.FinalizationError != causeText {
		t.Fatalf("result finalization error = %q, want raw cause retained", result.Termination.FinalizationError)
	}
	if len(events) != 1 {
		t.Fatalf("lifecycle events = %+v, want one decision", events)
	}
	if events[0].StopReason != "finalization_failed" {
		t.Fatalf("lifecycle stop reason = %q, want categorical kind", events[0].StopReason)
	}
	if strings.Contains(events[0].StopReason, causeText) {
		t.Fatalf("lifecycle stop reason leaked cause: %+v", events[0])
	}
}
