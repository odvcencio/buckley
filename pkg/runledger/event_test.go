package runledger

import (
	"encoding/json"
	"testing"
)

func TestNewEventID_Unique(t *testing.T) {
	a := NewEventID()
	b := NewEventID()
	if a == "" || b == "" {
		t.Fatalf("expected non-empty event IDs")
	}
	if a == b {
		t.Fatalf("expected distinct event IDs, both = %q", a)
	}
}

func TestEvent_JSONRoundTrip(t *testing.T) {
	ev := Event{
		SchemaVersion: SchemaVersion,
		ID:            NewEventID(),
		Sequence:      1,
		Type:          EventToolCompleted,
		SessionID:     "sess-1",
		RunID:         "run-1",
		TaskID:        "task-1",
		EvidenceIDs:   []string{"ev_a", "ev_b"},
		Payload:       map[string]any{"exit_code": float64(0)},
		Redaction:     DefaultRedactionVersion,
	}

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded Event
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded.Type != ev.Type || decoded.RunID != ev.RunID || decoded.Sequence != ev.Sequence {
		t.Fatalf("round-tripped event = %+v, want %+v", decoded, ev)
	}
	if len(decoded.EvidenceIDs) != 2 {
		t.Fatalf("round-tripped EvidenceIDs = %v", decoded.EvidenceIDs)
	}

	// Empty optional fields must be omitted, not serialized as null/"".
	minimal := Event{Type: EventRunStarted, RunID: "run-1", Redaction: DefaultRedactionVersion}
	raw, err = json.Marshal(minimal)
	if err != nil {
		t.Fatalf("json.Marshal(minimal) error = %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal(minimal) error = %v", err)
	}
	for _, omitted := range []string{"parent_run_id", "task_id", "agent_id", "model_id", "provider_id", "backend", "snapshot_id", "evidence_ids", "receipt_ids", "payload"} {
		if _, present := m[omitted]; present {
			t.Fatalf("expected %q to be omitted for a minimal event, got %v", omitted, m[omitted])
		}
	}
}

func TestEventTypeConstants_MatchSpecStrings(t *testing.T) {
	// Spot-check a representative sample against section 14.3's literal
	// strings, since every downstream consumer (materialize.go's
	// runStatusEvents/taskStatusEvents maps, future telemetry bridges)
	// depends on these exact values.
	cases := map[string]string{
		EventRunStarted:             "run.started",
		EventTaskBlocked:            "task.blocked",
		EventContextReceiptReused:   "context.receipt_reused",
		EventToolApprovalResolved:   "tool.approval_resolved",
		EventSubagentHandoffCreated: "subagent.handoff_created",
		EventBudgetExhausted:        "budget.exhausted",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("event constant = %q, want %q", got, want)
		}
	}
}
