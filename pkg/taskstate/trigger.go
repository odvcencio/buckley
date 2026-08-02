package taskstate

// TriggerReason names why a checkpoint fired (section 15.4). Reasons are
// recorded on runledger.TaskCheckpoint.Reason, so they are part of the
// durable record, not just logging.
type TriggerReason string

const (
	TriggerShutdown         TriggerReason = "shutdown"
	TriggerEpochBoundary    TriggerReason = "epoch_boundary"
	TriggerBlocker          TriggerReason = "blocker"
	TriggerModelChange      TriggerReason = "model_change"
	TriggerTestStateChange  TriggerReason = "test_state_change"
	TriggerDecisionRecorded TriggerReason = "decision_recorded"
	TriggerEditBatchEnd     TriggerReason = "edit_batch_end"
	TriggerPressure         TriggerReason = "pressure"
)

// Signals carries the observable facts one turn boundary produces.
// Checkpoint triggers are event-driven: the loop reports what happened
// and Evaluate decides whether that warrants a checkpoint.
type Signals struct {
	// PressureRatio is provider-reported context usage over the model's
	// window (0..1). Zero means unknown or not applicable.
	PressureRatio    float64
	EditBatchEnded   bool
	TestStateChanged bool
	DecisionRecorded bool
	BlockerRaised    bool
	ModelChanged     bool
	EpochBoundary    bool
	ShuttingDown     bool
}

// DefaultPressureThreshold is the section 27 checkpoint threshold.
const DefaultPressureThreshold = 0.65

// TriggerEvaluator decides whether a turn boundary checkpoints. The zero
// value uses DefaultPressureThreshold.
type TriggerEvaluator struct {
	// PressureThreshold overrides the checkpoint pressure cut when > 0
	// (config: context_fabric.pressure.checkpoint).
	PressureThreshold float64
}

// Evaluate returns the highest-priority trigger the signals justify.
// Priority is deterministic: shutdown, epoch boundary, blocker, model
// change, test-state change, decision recorded, edit-batch end, pressure.
// ok is false when nothing warrants a checkpoint.
func (e TriggerEvaluator) Evaluate(sig Signals) (TriggerReason, bool) {
	switch {
	case sig.ShuttingDown:
		return TriggerShutdown, true
	case sig.EpochBoundary:
		return TriggerEpochBoundary, true
	case sig.BlockerRaised:
		return TriggerBlocker, true
	case sig.ModelChanged:
		return TriggerModelChange, true
	case sig.TestStateChanged:
		return TriggerTestStateChange, true
	case sig.DecisionRecorded:
		return TriggerDecisionRecorded, true
	case sig.EditBatchEnded:
		return TriggerEditBatchEnd, true
	}
	threshold := e.PressureThreshold
	if threshold <= 0 {
		threshold = DefaultPressureThreshold
	}
	if sig.PressureRatio >= threshold {
		return TriggerPressure, true
	}
	return "", false
}
