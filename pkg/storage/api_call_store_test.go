package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAPICallStoreTracking(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "api.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	session := &Session{
		ID:         "sess-api",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	now := time.Now().UTC()
	call := &APICall{
		SessionID:        session.ID,
		Model:            "openrouter/model",
		PromptTokens:     100,
		CompletionTokens: 50,
		Cost:             1.23,
		Timestamp:        now,
	}
	if err := store.SaveAPICall(call); err != nil {
		t.Fatalf("save api call: %v", err)
	}
	if call.ID == 0 {
		t.Fatalf("expected api call ID to be set")
	}

	sessionRecord, err := store.GetSession(session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sessionRecord.TotalCost <= 0 {
		t.Fatalf("expected session total cost to increase, got %+v", sessionRecord)
	}

	daily, err := store.GetDailyCost()
	if err != nil {
		t.Fatalf("daily cost: %v", err)
	}
	if daily < 1.23 {
		t.Fatalf("expected daily cost >= 1.23, got %f", daily)
	}

	monthly, err := store.GetMonthlyCost()
	if err != nil {
		t.Fatalf("monthly cost: %v", err)
	}
	if monthly < daily {
		t.Fatalf("expected monthly cost >= daily cost, got monthly=%f daily=%f", monthly, daily)
	}
}
