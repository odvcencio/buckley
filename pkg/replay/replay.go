// Package replay verifies that a Buckley run contains enough durable
// structure to be replayed without executing tools. It intentionally does
// not invoke a model, tool, workflow, or external process.
package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/durability/modelstep"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
)

const SchemaVersion = "m31.replay.report.v1"

const (
	SeverityWarning = "warning"
	SeverityError   = "error"
)

// Issue is one replay-readiness finding. Historical events without the new
// step metadata are warnings so old ledgers remain inspectable; malformed
// new step records are errors.
type Issue struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Sequence int64  `json:"sequence,omitempty"`
	StepID   string `json:"step_id,omitempty"`
}

// Report is the read-only result of verifying a run's event, step, and
// evidence relationships.
type Report struct {
	SchemaVersion           string  `json:"schema_version"`
	RunID                   string  `json:"run_id"`
	RunStatus               string  `json:"run_status"`
	Valid                   bool    `json:"valid"`
	StepEnumerationComplete bool    `json:"step_enumeration_complete"`
	EventCount              int     `json:"event_count"`
	TaskCount               int     `json:"task_count"`
	StepCount               int     `json:"step_count"`
	EvidenceCount           int     `json:"evidence_count"`
	SequenceGaps            []int64 `json:"sequence_gaps,omitempty"`
	Issues                  []Issue `json:"issues,omitempty"`
}

// Verify loads only durable records and validates their replay contract. It
// never executes a tool or model call. A nil journal or evidence store makes
// the corresponding checks unavailable, which is useful for legacy exports
// but should not be used as the production goal CLI path.
func Verify(ctx context.Context, ledger runledger.Store, journal runledger.StepJournal, evidenceStore evidence.Store, runID string) (Report, error) {
	if ledger == nil {
		return Report{}, fmt.Errorf("replay: ledger is required")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Report{}, fmt.Errorf("replay: run ID is required")
	}

	run, err := ledger.GetRun(ctx, runID)
	if err != nil {
		return Report{}, err
	}
	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID})
	if err != nil {
		return Report{}, err
	}

	report := Report{
		SchemaVersion: SchemaVersion,
		RunID:         runID,
		RunStatus:     run.Status,
		EventCount:    len(events),
	}
	state, materializeErr := runledger.MaterializeRun(runID, events)
	if materializeErr != nil {
		report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "event_order", Message: materializeErr.Error()})
	} else {
		report.TaskCount = len(state.Tasks)
		report.SequenceGaps = append(report.SequenceGaps, state.SequenceGaps...)
		for _, gap := range state.SequenceGaps {
			report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "sequence_gap", Message: fmt.Sprintf("missing event sequence %d", gap)})
		}
	}

	var enumeratedSteps []runledger.ExecutionStep
	enumeratedStepByID := map[string]runledger.ExecutionStep{}
	if journal != nil {
		if enumerator, ok := journal.(runledger.StepEnumerator); ok {
			steps, enumerateErr := enumerator.ListSteps(ctx, runID)
			if enumerateErr != nil {
				report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "step_enumeration_failed", Message: enumerateErr.Error()})
			} else {
				report.StepEnumerationComplete = true
				for _, step := range steps {
					enumeratedSteps = append(enumeratedSteps, step)
					enumeratedStepByID[step.StepID] = step
				}
			}
		} else {
			report.Issues = append(report.Issues, Issue{Severity: SeverityWarning, Code: "step_enumeration_unavailable", Message: "step journal cannot enumerate execution steps; verification is partial"})
		}
	}

	seenEventIDs := map[string]bool{}
	stepIDs := map[string]bool{}
	evidenceIDs := map[string]bool{}
	validatedModelSteps := map[string]bool{}
	blockedMarkers := map[string]modelstep.BlockedMarker{}
	for _, event := range events {
		if event.ID != "" && seenEventIDs[event.ID] {
			report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "duplicate_event_id", Message: fmt.Sprintf("event ID %s appears more than once", event.ID), Sequence: event.Sequence})
		}
		seenEventIDs[event.ID] = true

		stepID := payloadString(event.Payload, "step_id")
		durableReceiptSchema := ""
		if event.Type == runledger.EventDurableTurn {
			durableReceiptSchema = payloadString(event.Payload, "receipt_schema")
			if validationErr := validateDurableTurnReceiptShape(event); validationErr != nil {
				report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "durable_turn_receipt_schema", Message: validationErr.Error(), Sequence: event.Sequence, StepID: stepID})
			}
		}
		if stepID != "" {
			stepIDs[stepID] = true
			if payloadString(event.Payload, "input_digest") == "" && requiresInputDigest(event.Type) {
				report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "missing_input_digest", Message: "step event has no input digest", Sequence: event.Sequence, StepID: stepID})
			}
		} else if requiresStepID(event.Type) {
			report.Issues = append(report.Issues, Issue{Severity: SeverityWarning, Code: "legacy_step_event", Message: "event predates stable step metadata", Sequence: event.Sequence})
		}

		for _, id := range eventEvidenceIDs(event) {
			evidenceIDs[id] = true
		}
		retryBlocked := event.Type == runledger.EventModelRequestReplayed && payloadBool(event.Payload, "retry_blocked")
		if payloadBool(event.Payload, "retry_blocked") && event.Type != runledger.EventModelRequestReplayed {
			report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "retry_blocked_shape", Message: "retry_blocked is only valid on model.request_replayed", Sequence: event.Sequence, StepID: stepID})
		}

		// Only step-bearing events fail closed on missing output evidence.
		// Legacy events already carry a legacy_step_event warning and must
		// keep old ledgers inspectable. A retry-blocked model replay may have
		// no response evidence when the evidence write itself failed.
		if requiresOutputEvidence(event.Type) && !retryBlocked && stepID != "" && payloadEvidenceID(event) == "" {
			report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "missing_output_evidence", Message: "completed replayable step has no output evidence", Sequence: event.Sequence, StepID: stepID})
		}

		if journal != nil && stepID != "" {
			var step runledger.ExecutionStep
			var err error
			if report.StepEnumerationComplete {
				var found bool
				step, found = enumeratedStepByID[stepID]
				if !found {
					err = runledger.ErrStepNotFound
				}
			} else {
				step, err = journal.GetStep(ctx, runID, stepID)
			}
			if err != nil {
				report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "missing_step_record", Message: err.Error(), Sequence: event.Sequence, StepID: stepID})
			} else {
				if eventDigest := payloadString(event.Payload, "input_digest"); eventDigest != step.InputDigest {
					report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "step_input_digest_mismatch", Message: "event input digest does not match the execution step", Sequence: event.Sequence, StepID: stepID})
				}
				attempt, ok := payloadInteger(event.Payload, "attempt")
				if !ok || attempt <= 0 {
					report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "missing_step_attempt", Message: "step event has no valid attempt", Sequence: event.Sequence, StepID: stepID})
				} else if attempt > step.Attempt {
					report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "step_attempt_mismatch", Message: "event attempt is newer than the durable execution step", Sequence: event.Sequence, StepID: stepID})
				} else if requiresCurrentAttempt(event.Type) && attempt != step.Attempt {
					report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "step_attempt_mismatch", Message: "event attempt does not match the execution step", Sequence: event.Sequence, StepID: stepID})
				}
				if requiresOutputEvidence(event.Type) || retryBlocked {
					issueCode := "step_output_evidence_mismatch"
					if retryBlocked {
						issueCode = "retry_blocked_evidence"
					}
					if validationErr := validateEventOutputEvidence(ctx, evidenceStore, event, step); validationErr != nil {
						report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: issueCode, Message: validationErr.Error(), Sequence: event.Sequence, StepID: stepID})
					}
				}
				if event.Type == runledger.EventDurableTurn && durableReceiptSchema == runledger.DurableTurnReceiptSchemaV1 {
					if validationErr := validateDurableTurnReceipt(event, step); validationErr != nil {
						report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "durable_turn_receipt", Message: validationErr.Error(), Sequence: event.Sequence, StepID: stepID})
					}
				}
				if step.Kind == "model" && !validatedModelSteps[stepID] {
					validatedModelSteps[stepID] = true
					switch {
					case step.Status == runledger.StepBlocked && strings.HasPrefix(step.Error, modelstep.BlockedMarkerPrefix):
						issueCode := "blocked_model_record"
						if retryBlocked {
							issueCode = "retry_blocked_record"
						}
						marker, validationErr := modelstep.ValidateBlockedStep(step)
						if validationErr == nil {
							blockedMarkers[stepID] = marker
							if marker.ResponseEvidenceID != "" {
								evidenceIDs[marker.ResponseEvidenceID] = true
								if evidenceStore != nil {
									object, loadErr := evidenceStore.Get(ctx, marker.ResponseEvidenceID)
									if loadErr != nil {
										validationErr = loadErr
									} else if _, replayErr := modelstep.ValidateBlockedReplay(step, &object); replayErr != nil {
										validationErr = replayErr
									}
								}
							}
						}
						if validationErr != nil {
							report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: issueCode, Message: validationErr.Error(), Sequence: event.Sequence, StepID: stepID})
						}
					case step.Status == runledger.StepCompleted && step.OutputEvidenceID != "":
						evidenceIDs[step.OutputEvidenceID] = true
						if evidenceStore != nil {
							object, loadErr := evidenceStore.Get(ctx, step.OutputEvidenceID)
							if loadErr != nil {
								report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "model_response_record", Message: loadErr.Error(), Sequence: event.Sequence, StepID: stepID})
							} else if _, validationErr := modelstep.ValidateResponseEvidence(step.OutputEvidenceID, step.OutputDigest, object); validationErr != nil {
								report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "model_response_record", Message: validationErr.Error(), Sequence: event.Sequence, StepID: stepID})
							}
						}
					}
				}
			}
			if err == nil && retryBlocked {
				marker, markerOK := blockedMarkers[stepID]
				if markerOK && payloadString(event.Payload, "provider_error") != marker.ProviderError {
					report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "retry_blocked_record", Message: "retry-blocked event provider error does not match the blocked marker", Sequence: event.Sequence, StepID: stepID})
				}
				if !markerOK && (step.Status != runledger.StepBlocked || !strings.HasPrefix(step.Error, modelstep.BlockedMarkerPrefix)) {
					report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "retry_blocked_record", Message: "retry-blocked replay does not reference a versioned blocked model record", Sequence: event.Sequence, StepID: stepID})
				}
			} else if err == nil && requiresOutputEvidence(event.Type) && step.Status != runledger.StepCompleted {
				report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "step_status_mismatch", Message: fmt.Sprintf("event says completed but step status is %s", step.Status), Sequence: event.Sequence, StepID: stepID})
			}
		}
		if event.Type == runledger.EventDurableTurn && durableReceiptSchema == runledger.DurableTurnReceiptSchemaV1 && journal == nil {
			report.Issues = append(report.Issues, Issue{Severity: SeverityWarning, Code: "durable_turn_receipt_unverified", Message: "durable turn receipt cannot be matched without a step journal", Sequence: event.Sequence, StepID: stepID})
		}
	}

	for _, step := range enumeratedSteps {
		stepID := step.StepID
		if !stepIDs[stepID] {
			report.Issues = append(report.Issues, Issue{
				Severity: SeverityError,
				Code:     "orphan_step_record",
				Message:  fmt.Sprintf("execution step has status %q and dispatch state %q but no ledger event", step.Status, step.DispatchState),
				StepID:   stepID,
			})
		}
		stepIDs[stepID] = true
	}
	report.StepCount = len(stepIDs)
	report.EvidenceCount = len(evidenceIDs)
	if evidenceStore != nil {
		ids := make([]string, 0, len(evidenceIDs))
		for id := range evidenceIDs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			obj, err := evidenceStore.Get(ctx, id)
			if err != nil {
				report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "missing_evidence", Message: err.Error()})
				continue
			}
			if obj.ID != id {
				report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "evidence_identity", Message: fmt.Sprintf("evidence lookup %s returned %s", id, obj.ID)})
			}
		}
	}

	report.Valid = true
	for _, issue := range report.Issues {
		if issue.Severity == SeverityError {
			report.Valid = false
			break
		}
	}
	return report, nil
}

func validateDurableTurnReceiptShape(event runledger.Event) error {
	schema := payloadString(event.Payload, "receipt_schema")
	if schema == "" {
		if payloadString(event.Payload, "activity") == "run_turn.v3" || durableTurnReceiptFieldsPresent(event.Payload) {
			return fmt.Errorf("V3 durable turn receipt has no schema")
		}
		return nil
	}
	if schema != runledger.DurableTurnReceiptSchemaV1 {
		return fmt.Errorf("unsupported durable turn receipt schema %q", schema)
	}
	if payloadString(event.Payload, "step_id") == "" {
		return fmt.Errorf("durable turn receipt has no step ID")
	}
	return nil
}

func durableTurnReceiptFieldsPresent(payload map[string]any) bool {
	for _, key := range []string{"step_id", "attempt", "input_digest", "response_json", "output_digest"} {
		if _, ok := payload[key]; ok {
			return true
		}
	}
	return false
}

func validateDurableTurnReceipt(event runledger.Event, step runledger.ExecutionStep) error {
	if step.Kind != "durable_turn" {
		return fmt.Errorf("durable turn receipt references step kind %q", step.Kind)
	}
	if step.RunID != event.RunID {
		return fmt.Errorf("durable turn receipt run does not match completed step")
	}
	if step.TaskID == "" || step.TaskID != event.TaskID {
		return fmt.Errorf("durable turn receipt task does not match completed step")
	}
	if step.Status != runledger.StepCompleted {
		return fmt.Errorf("durable turn receipt references step status %q", step.Status)
	}
	if step.DispatchState != runledger.StepDispatchDispatched {
		return fmt.Errorf("durable turn receipt references dispatch state %q", step.DispatchState)
	}
	attempt, ok := payloadInteger(event.Payload, "attempt")
	if !ok || attempt <= 0 || attempt != step.Attempt {
		return fmt.Errorf("durable turn receipt attempt does not match completed step attempt")
	}
	if payloadString(event.Payload, "input_digest") != step.InputDigest {
		return fmt.Errorf("durable turn receipt input digest does not match completed step")
	}
	workflowInstanceID := payloadString(event.Payload, "workflow_instance_id")
	activity := payloadString(event.Payload, "activity")
	generation, generationOK := payloadInteger(event.Payload, "generation")
	turnIndex, turnIndexOK := payloadInteger(event.Payload, "turn_index")
	if workflowInstanceID == "" || activity == "" || !generationOK || generation < 0 || !turnIndexOK || turnIndex < 0 {
		return fmt.Errorf("durable turn receipt has invalid stable identity coordinates")
	}
	expectedStepID := "turn_" + runledger.StableEventID(
		"durable-turn-step-v3", event.RunID, event.TaskID, workflowInstanceID,
		activity, fmt.Sprintf("%d", generation), fmt.Sprintf("%d", turnIndex),
	)
	if step.StepID != expectedStepID {
		return fmt.Errorf("durable turn receipt step ID is not canonical")
	}
	expectedEventID := runledger.StableEventID(
		runledger.EventDurableTurn, event.RunID, event.TaskID, workflowInstanceID,
		activity, fmt.Sprintf("%d", generation), fmt.Sprintf("%d", turnIndex),
	)
	if event.ID != expectedEventID {
		return fmt.Errorf("durable turn receipt event ID is not canonical")
	}
	responseJSON, _ := event.Payload["response_json"].(string)
	if strings.TrimSpace(responseJSON) == "" || !json.Valid([]byte(responseJSON)) {
		return fmt.Errorf("durable turn receipt response is not valid JSON")
	}
	var responseProjection struct {
		Kind     string `json:"kind"`
		Decision string `json:"decision,omitempty"`
	}
	if err := json.Unmarshal([]byte(responseJSON), &responseProjection); err != nil {
		return fmt.Errorf("durable turn receipt response projection is invalid")
	}
	projectedKind, kindOK := event.Payload["kind"].(string)
	projectedDecision, decisionOK := event.Payload["decision"].(string)
	if !kindOK || projectedKind != responseProjection.Kind || !decisionOK || projectedDecision != responseProjection.Decision {
		return fmt.Errorf("durable turn receipt duplicated response projection changed")
	}
	sum := sha256.Sum256([]byte(responseJSON))
	outputDigest := hex.EncodeToString(sum[:])
	if payloadString(event.Payload, "output_digest") != outputDigest {
		return fmt.Errorf("durable turn receipt response digest is invalid")
	}
	if event.ID != step.OutputEvidenceID {
		return fmt.Errorf("durable turn receipt event does not match completed step output")
	}
	if outputDigest != step.OutputDigest {
		return fmt.Errorf("durable turn receipt digest does not match completed step output")
	}
	return nil
}

func requiresStepID(eventType string) bool {
	switch eventType {
	case runledger.EventModelRequestPlanned, runledger.EventModelRequestStarted, runledger.EventModelRequestCompleted, runledger.EventModelRequestFailed, runledger.EventModelRequestReplayed,
		runledger.EventToolStarted, runledger.EventToolCompleted, runledger.EventToolFailed, runledger.EventToolReplayed:
		return true
	default:
		return false
	}
}

func requiresInputDigest(eventType string) bool {
	return requiresStepID(eventType)
}

func requiresOutputEvidence(eventType string) bool {
	switch eventType {
	case runledger.EventModelRequestCompleted, runledger.EventModelRequestReplayed, runledger.EventToolCompleted, runledger.EventToolReplayed:
		return true
	default:
		return false
	}
}

func requiresCurrentAttempt(eventType string) bool {
	switch eventType {
	case runledger.EventModelRequestCompleted, runledger.EventModelRequestReplayed,
		runledger.EventToolCompleted, runledger.EventToolReplayed:
		return true
	default:
		return false
	}
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func payloadBool(payload map[string]any, key string) bool {
	if payload == nil {
		return false
	}
	value, _ := payload[key].(bool)
	return value
}

func payloadInteger(payload map[string]any, key string) (int, bool) {
	if payload == nil {
		return 0, false
	}
	switch value := payload[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), int64(int(value)) == value
	case float64:
		integer := int(value)
		return integer, float64(integer) == value
	default:
		return 0, false
	}
}

func payloadEvidenceID(event runledger.Event) string {
	for _, key := range []string{"response_evidence_id", "output_evidence_id", "evidence_id"} {
		if id := payloadString(event.Payload, key); id != "" {
			return id
		}
	}
	if len(event.EvidenceIDs) > 0 {
		return event.EvidenceIDs[0]
	}
	return ""
}

func validateEventOutputEvidence(ctx context.Context, evidenceStore evidence.Store, event runledger.Event, step runledger.ExecutionStep) error {
	eventEvidenceID := payloadEvidenceID(event)
	if eventEvidenceID != step.OutputEvidenceID {
		return fmt.Errorf("event output evidence %q does not match execution step output evidence %q", eventEvidenceID, step.OutputEvidenceID)
	}
	for _, key := range []string{"response_evidence_id", "output_evidence_id", "evidence_id"} {
		if id := payloadString(event.Payload, key); id != "" && id != step.OutputEvidenceID {
			return fmt.Errorf("event %s %q does not match execution step output evidence %q", key, id, step.OutputEvidenceID)
		}
	}
	for _, id := range event.EvidenceIDs {
		if step.OutputEvidenceID == "" || id != step.OutputEvidenceID {
			return fmt.Errorf("event evidence %q does not match execution step output evidence %q", id, step.OutputEvidenceID)
		}
	}
	if eventEvidenceID == "" || evidenceStore == nil {
		return nil
	}
	object, err := evidenceStore.Get(ctx, eventEvidenceID)
	if err != nil {
		return nil
	}
	if object.ID != step.OutputEvidenceID || object.ContentSHA256 != step.OutputDigest {
		return fmt.Errorf("event output evidence identity does not match the execution step output digest")
	}
	return nil
}

func eventEvidenceIDs(event runledger.Event) []string {
	ids := append([]string(nil), event.EvidenceIDs...)
	for _, key := range []string{"request_evidence_id", "response_evidence_id", "output_evidence_id", "evidence_id"} {
		if id := payloadString(event.Payload, key); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
