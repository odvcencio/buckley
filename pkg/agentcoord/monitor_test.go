package agentcoord

import (
	"encoding/base64"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRoutineCursor_RoundTripAndCanonicalBounds(t *testing.T) {
	startedAt := time.Date(2026, 8, 20, 15, 4, 5, 123456789, time.UTC)
	cursor, err := EncodeRoutineCursor(startedAt, "run-01")
	if err != nil {
		t.Fatal(err)
	}
	decodedAt, runID, err := DecodeRoutineCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !decodedAt.Equal(startedAt) || runID != "run-01" || strings.Contains(cursor, "=") {
		t.Fatalf("cursor round trip = %s %s %q", cursor, decodedAt, runID)
	}

	tests := []string{
		"", " ", cursor + "=", strings.Repeat("x", MaxRoutineCursorBytes+1),
		base64.RawURLEncoding.EncodeToString([]byte("v2\x002026-08-20T15:04:05Z\x00run-01")),
		base64.RawURLEncoding.EncodeToString([]byte("v1\x002026-08-20T15:04:05+00:00\x00run-01")),
		base64.RawURLEncoding.EncodeToString([]byte("v1\x002026-08-20T15:04:05Z\x00run-01\x00extra")),
		base64.RawURLEncoding.EncodeToString([]byte("v1\x002026-08-20T15:04:05Z\x00 run-01")),
	}
	for _, value := range tests {
		if _, _, err := DecodeRoutineCursor(value); !errors.Is(err, ErrMonitorValidation) {
			t.Fatalf("DecodeRoutineCursor(%q) error = %v", value, err)
		}
	}
}

func TestNormalizeRoutineQuery_DefaultsAndBounds(t *testing.T) {
	query, err := NormalizeRoutineQuery(RoutineQuery{SessionID: "session-01", ParentRunID: "run-parent"})
	if err != nil {
		t.Fatal(err)
	}
	if query.Limit != DefaultRoutineStatusLimit {
		t.Fatalf("default limit = %d", query.Limit)
	}
	for _, invalid := range []RoutineQuery{
		{},
		{SessionID: " session-01"},
		{SessionID: "session-01", ParentRunID: strings.Repeat("p", MaxMonitorIdentifierBytes+1)},
		{SessionID: "session-01", Limit: -1},
		{SessionID: "session-01", Limit: MaxRoutineStatusLimit + 1},
		{SessionID: "session-01", Before: "malformed"},
	} {
		if _, err := NormalizeRoutineQuery(invalid); !errors.Is(err, ErrMonitorValidation) {
			t.Fatalf("NormalizeRoutineQuery(%+v) error = %v", invalid, err)
		}
	}
}

func TestValidateMonitorIdentity_RequiresExactBoundedIdentifiers(t *testing.T) {
	if err := ValidateMonitorIdentity("session-01", "run-01"); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{"", "run-01"}, {"session-01", ""}, {"session-01", " run-01"},
		{"session-01", strings.Repeat("r", MaxMonitorIdentifierBytes+1)},
	} {
		if err := ValidateMonitorIdentity(pair[0], pair[1]); !errors.Is(err, ErrMonitorValidation) {
			t.Fatalf("ValidateMonitorIdentity(%q,%q) error=%v", pair[0], pair[1], err)
		}
	}
}

func TestValidateMonitorIdentifier_RejectsUnsafeTextWithoutReplacingUnicode(t *testing.T) {
	if err := ValidateMonitorIdentifier("run id", "routine-雪", true); err != nil {
		t.Fatalf("safe unicode identifier: %v", err)
	}
	for _, value := range []string{"run\x00id", "run\nid", string([]byte{'r', 'u', 'n', '-', 0xff})} {
		if err := ValidateMonitorIdentifier("run id", value, true); !errors.Is(err, ErrMonitorValidation) {
			t.Fatalf("unsafe identifier %q error=%v", value, err)
		}
	}
}

func TestNormalizeMailboxStatusQuery_DeduplicatesWithoutAliasing(t *testing.T) {
	states := []MailboxState{MailboxQueued, MailboxClaimed, MailboxQueued, MailboxProcessed}
	query := MailboxStatusQuery{SessionID: "session-01", RunID: "run-01", States: states}
	normalized, err := NormalizeMailboxStatusQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Limit != DefaultMailboxStatusLimit || !reflect.DeepEqual(normalized.States,
		[]MailboxState{MailboxQueued, MailboxClaimed, MailboxProcessed}) {
		t.Fatalf("normalized query = %+v", normalized)
	}
	normalized.States[0] = MailboxDeadLetter
	if states[0] != MailboxQueued {
		t.Fatal("normalization aliased caller states")
	}

	for _, invalid := range []MailboxStatusQuery{
		{},
		{SessionID: "session-01"},
		{SessionID: "session-01", RunID: "run-01", AfterSequence: -1},
		{SessionID: "session-01", RunID: "run-01", AfterSequence: MaxMonitorSequence + 1},
		{SessionID: "session-01", RunID: "run-01", Limit: MaxMailboxStatusLimit + 1},
		{SessionID: "session-01", RunID: "run-01", States: []MailboxState{"unknown"}},
		{SessionID: "session-01", RunID: "run-01", States: []MailboxState{
			MailboxQueued, MailboxClaimed, MailboxProcessed, MailboxDeadLetter, MailboxQueued,
		}},
	} {
		if _, err := NormalizeMailboxStatusQuery(invalid); !errors.Is(err, ErrMonitorValidation) {
			t.Fatalf("NormalizeMailboxStatusQuery(%+v) error = %v", invalid, err)
		}
	}
}

func TestMonitorProjectionValidation_StateAndChronology(t *testing.T) {
	started := time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)
	heartbeat := started.Add(time.Second)
	expires := heartbeat.Add(time.Minute)
	detached := heartbeat.Add(time.Second)
	validAttempt := AttemptStatus{
		Number: 1, State: AttemptAttached, AttachedAt: &started,
		HeartbeatAt: &heartbeat, LeaseExpiresAt: &expires,
	}
	valid := RoutineStatus{
		SessionID: "session-01", RunID: "run-01", State: RunRunning,
		StartedAt: started, Attempt: validAttempt,
	}
	if err := ValidateRoutineStatus(valid); err != nil {
		t.Fatal(err)
	}

	invalidTerminal := valid
	invalidTerminal.State = RunCompleted
	if err := ValidateRoutineStatus(invalidTerminal); !errors.Is(err, ErrMonitorIntegrity) {
		t.Fatalf("terminal error = %v", err)
	}
	invalidDetached := validAttempt
	invalidDetached.State = AttemptDetached
	invalidDetached.DetachedAt = &started
	if err := ValidateAttemptStatus(invalidDetached); !errors.Is(err, ErrMonitorIntegrity) {
		t.Fatalf("detached error = %v", err)
	}
	validDetached := validAttempt
	validDetached.State = AttemptDetached
	validDetached.DetachedAt = &detached
	if err := ValidateAttemptStatus(validDetached); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMailboxSummary(MailboxSummary{Queued: 1, LastSequence: 2}); !errors.Is(err, ErrMonitorIntegrity) {
		t.Fatalf("summary error = %v", err)
	}
}

func TestValidateMailboxSummary_RejectsNativeIntOverflow(t *testing.T) {
	if total, ok := monitorCheckedCountSum(MaxMonitorSequence, math.MaxInt, math.MaxInt); ok || total != 0 {
		t.Fatalf("overflow sum=(%d,%v), want zero,false", total, ok)
	}
	for _, summary := range []MailboxSummary{
		{Queued: math.MaxInt, Claimed: math.MaxInt, LastSequence: MaxMonitorSequence},
		{Queued: 1, Claimed: math.MaxInt, Processed: math.MaxInt, DeadLetter: math.MaxInt, LastSequence: 1},
		{Queued: 1, LastSequence: MaxMonitorSequence + 1},
	} {
		if err := ValidateMailboxSummary(summary); !errors.Is(err, ErrMonitorIntegrity) {
			t.Fatalf("ValidateMailboxSummary(%+v) error=%v", summary, err)
		}
	}
}

func TestValidateMailboxStatus_DirectionAndTerminalTimestamps(t *testing.T) {
	created := time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)
	processed := created.Add(time.Second)
	valid := MailboxStatus{
		SessionID: "session-01", RunID: "run-01", MessageID: "message-01",
		PeerRunID: "run-parent", Kind: "message", Direction: MailboxFromParent,
		State: MailboxProcessed, Sequence: 1, ByteCount: 10,
		CreatedAt: created, ProcessedAt: &processed,
	}
	if err := ValidateMailboxStatus(valid); err != nil {
		t.Fatal(err)
	}
	operator := valid
	operator.Direction = MailboxFromOperator
	operator.PeerRunID = ""
	operator.Kind = OperatorSteerKind
	if err := ValidateMailboxStatus(operator); err != nil {
		t.Fatal(err)
	}
	operator.PeerRunID = "fabricated-peer"
	if err := ValidateMailboxStatus(operator); !errors.Is(err, ErrMonitorIntegrity) {
		t.Fatalf("operator peer error = %v", err)
	}
	invalid := valid
	invalid.State = MailboxQueued
	if err := ValidateMailboxStatus(invalid); !errors.Is(err, ErrMonitorIntegrity) {
		t.Fatalf("queued terminal timestamp error = %v", err)
	}
}
