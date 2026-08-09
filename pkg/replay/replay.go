// Package replay verifies that a Buckley run contains enough durable
// structure to be replayed without executing tools. It intentionally does
// not invoke a model, tool, workflow, or external process.
package replay

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
	SchemaVersion string  `json:"schema_version"`
	RunID         string  `json:"run_id"`
	RunStatus     string  `json:"run_status"`
	Valid         bool    `json:"valid"`
	EventCount    int     `json:"event_count"`
	TaskCount     int     `json:"task_count"`
	StepCount     int     `json:"step_count"`
	EvidenceCount int     `json:"evidence_count"`
	SequenceGaps  []int64 `json:"sequence_gaps,omitempty"`
	Issues        []Issue `json:"issues,omitempty"`
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

	seenEventIDs := map[string]bool{}
	stepIDs := map[string]bool{}
	evidenceIDs := map[string]bool{}
	for _, event := range events {
		if event.ID != "" && seenEventIDs[event.ID] {
			report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "duplicate_event_id", Message: fmt.Sprintf("event ID %s appears more than once", event.ID), Sequence: event.Sequence})
		}
		seenEventIDs[event.ID] = true

		stepID := payloadString(event.Payload, "step_id")
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
		if requiresOutputEvidence(event.Type) && payloadEvidenceID(event) == "" {
			report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "missing_output_evidence", Message: "completed replayable step has no output evidence", Sequence: event.Sequence, StepID: stepID})
		}

		if journal != nil && stepID != "" {
			step, err := journal.GetStep(ctx, runID, stepID)
			if err != nil {
				report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "missing_step_record", Message: err.Error(), Sequence: event.Sequence, StepID: stepID})
			} else if requiresOutputEvidence(event.Type) && step.Status != runledger.StepCompleted {
				report.Issues = append(report.Issues, Issue{Severity: SeverityError, Code: "step_status_mismatch", Message: fmt.Sprintf("event says completed but step status is %s", step.Status), Sequence: event.Sequence, StepID: stepID})
			}
		}
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

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
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

func eventEvidenceIDs(event runledger.Event) []string {
	ids := append([]string(nil), event.EvidenceIDs...)
	for _, key := range []string{"request_evidence_id", "response_evidence_id", "output_evidence_id", "evidence_id"} {
		if id := payloadString(event.Payload, key); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}
