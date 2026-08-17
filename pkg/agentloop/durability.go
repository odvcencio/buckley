package agentloop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/durability/modelstep"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
)

// StableStepID identifies a logical step independently of an execution
// attempt. It is safe to use as a local idempotency key and is the identity a
// future Dapr activity should preserve across retries.
func StableStepID(runID, taskID, turnID string, round int, kind string, ordinal int) string {
	if strings.TrimSpace(runID) == "" {
		runID = "run-unknown"
	}
	if strings.TrimSpace(taskID) == "" {
		taskID = "task-unknown"
	}
	if strings.TrimSpace(turnID) == "" {
		turnID = "turn-unknown"
	}
	if round <= 0 {
		round = 1
	}
	if ordinal < 0 {
		ordinal = 0
	}
	return fmt.Sprintf("%s/%s/%s/round-%03d/%s-%03d", runID, taskID, turnID, round, kind, ordinal)
}

func jsonDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func (c *Controller) recordJSONEvidence(ctx context.Context, kind evidence.Kind, value any, stepID string, metadata map[string]any) (string, string, error) {
	if c == nil || c.cfg.Evidence == nil {
		return "", "", nil
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "", "", fmt.Errorf("agentloop: marshal %s evidence: %w", kind, err)
	}
	return c.recordEvidenceBody(ctx, kind, body, stepID, metadata)
}

func (c *Controller) recordEvidenceBody(ctx context.Context, kind evidence.Kind, body []byte, stepID string, metadata map[string]any) (string, string, error) {
	if c == nil || c.cfg.Evidence == nil {
		return "", "", nil
	}
	obj, err := c.cfg.Evidence.Put(ctx, evidence.Object{
		Kind:       kind,
		MediaType:  "application/json",
		InlineBody: body,
		Metadata:   evidenceMetadata(c, stepID, metadata),
	})
	if err != nil {
		return "", "", fmt.Errorf("agentloop: store %s evidence: %w", kind, err)
	}
	// Step evidence is pinned for the run's lifetime so retention sweeps
	// can never invalidate replay; pruning the run releases the pin
	// (spec.durable-execution-dapr, evidence retention decision).
	if c.cfg.RunID != "" {
		if err := c.cfg.Evidence.Pin(ctx, obj.ID, RunPinReason(c.cfg.RunID)); err != nil {
			return "", "", fmt.Errorf("agentloop: pin %s evidence: %w", kind, err)
		}
	}
	return obj.ID, obj.ContentSHA256, nil
}

// RunPinReason is the evidence pin reason for one run's replayable step
// evidence. Releasing every pin with this reason is part of pruning the
// run.
func RunPinReason(runID string) string {
	return "run:" + runID
}

func (c *Controller) loadJSONEvidence(ctx context.Context, evidenceID string, target any) error {
	if c == nil || c.cfg.Evidence == nil {
		return fmt.Errorf("agentloop: evidence store is required to replay %s", evidenceID)
	}
	if strings.TrimSpace(evidenceID) == "" {
		return fmt.Errorf("agentloop: replay evidence ID is empty")
	}
	obj, err := c.cfg.Evidence.Get(ctx, evidenceID)
	if err != nil {
		return fmt.Errorf("agentloop: load replay evidence %s: %w", evidenceID, err)
	}
	if err := json.Unmarshal(obj.InlineBody, target); err != nil {
		return fmt.Errorf("agentloop: decode replay evidence %s: %w", evidenceID, err)
	}
	return nil
}

func (c *Controller) beginStep(ctx context.Context, stepID, kind, inputDigest string) (runledger.ExecutionStep, bool, error) {
	step := runledger.ExecutionStep{
		RunID:          c.cfg.RunID,
		TaskID:         c.cfg.TaskID,
		StepID:         stepID,
		Kind:           kind,
		IdempotencyKey: stepID,
		Attempt:        1,
		InputDigest:    inputDigest,
		StartedAt:      time.Now().UTC(),
	}
	if c.cfg.StepJournal == nil {
		return step, false, nil
	}
	got, replay, err := c.cfg.StepJournal.BeginStep(ctx, step)
	if err != nil {
		var recovery *runledger.StepRecoveryError
		if errors.As(err, &recovery) && recovery.Action == runledger.StepRecoveryResume {
			reclaimed, reclaimErr := c.cfg.StepJournal.ReclaimStep(ctx, got, time.Now().UTC())
			if reclaimErr != nil {
				return runledger.ExecutionStep{}, false, fmt.Errorf("agentloop: resume %s step: %w", stepID, reclaimErr)
			}
			return reclaimed, false, nil
		}
		return runledger.ExecutionStep{}, false, fmt.Errorf("agentloop: begin %s step: %w", stepID, err)
	}
	return got, replay, nil
}

func (c *Controller) completeStep(ctx context.Context, step runledger.ExecutionStep, evidenceID, outputDigest string) error {
	if c.cfg.StepJournal == nil {
		return nil
	}
	if err := c.cfg.StepJournal.CompleteStepAttempt(ctx, step, evidenceID, outputDigest, time.Now().UTC()); err != nil {
		return fmt.Errorf("agentloop: complete %s step: %w", step.StepID, err)
	}
	return nil
}

func (c *Controller) markStepDispatched(ctx context.Context, step runledger.ExecutionStep) error {
	if c == nil || c.cfg.StepJournal == nil {
		return nil
	}
	if err := c.cfg.StepJournal.MarkStepDispatched(ctx, step, time.Now().UTC()); err != nil {
		return fmt.Errorf("agentloop: mark %s step dispatched: %w", step.StepID, err)
	}
	return nil
}

func (c *Controller) blockDispatchedStep(ctx context.Context, step runledger.ExecutionStep, failure error, evidenceID, outputDigest string) error {
	if c == nil || c.cfg.StepJournal == nil || step.StepID == "" {
		return nil
	}
	message := "external effect outcome requires reconciliation"
	if failure != nil {
		message = modelstep.NormalizeError(failure)
	}
	if err := c.cfg.StepJournal.BlockStep(ctx, step, message, evidenceID, outputDigest, time.Now().UTC()); err != nil {
		return fmt.Errorf("agentloop: block dispatched %s step: %w", step.StepID, err)
	}
	return nil
}

func (c *Controller) failStep(ctx context.Context, step runledger.ExecutionStep, failure error) {
	if c == nil || c.cfg.StepJournal == nil || step.StepID == "" {
		return
	}
	message := "step failed"
	if failure != nil {
		message = modelstep.NormalizeError(failure)
	}
	_ = c.cfg.StepJournal.FailStepAttempt(ctx, step, message, time.Now().UTC())
}

func evidenceMetadata(c *Controller, stepID string, metadata map[string]any) map[string]any {
	out := make(map[string]any, len(metadata)+3)
	for key, value := range metadata {
		out[key] = value
	}
	if c != nil {
		if c.cfg.RunID != "" {
			out[evidence.MetaRunID] = c.cfg.RunID
		}
		if c.cfg.SessionID != "" {
			out[evidence.MetaSessionID] = c.cfg.SessionID
		}
		if c.cfg.TaskID != "" {
			out[evidence.MetaTaskID] = c.cfg.TaskID
		}
	}
	if stepID != "" {
		out["step_id"] = stepID
	}
	return out
}

func stepPayload(stepID string, attempt int, inputDigest string) map[string]any {
	return map[string]any{
		"step_id":         stepID,
		"idempotency_key": stepID,
		"attempt":         attempt,
		"input_digest":    inputDigest,
	}
}

func evidenceIDs(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}
