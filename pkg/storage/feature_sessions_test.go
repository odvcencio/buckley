package storage

import "testing"

func TestLinkSessionToPlan(t *testing.T) {
	store := setupTestStore(t)
	defer cleanupTestStore(store)

	sessionID := "session-plan-1"
	createTestSession(store, sessionID)

	planID := "plan-123"
	if err := store.LinkSessionToPlan(sessionID, planID); err != nil {
		t.Fatalf("LinkSessionToPlan() error = %v", err)
	}

	got, err := store.GetSessionPlanID(sessionID)
	if err != nil {
		t.Fatalf("GetSessionPlanID() error = %v", err)
	}
	if got != planID {
		t.Fatalf("GetSessionPlanID() = %s, want %s", got, planID)
	}

	// Linking again should overwrite the previous association.
	secondPlan := "plan-456"
	if err := store.LinkSessionToPlan(sessionID, secondPlan); err != nil {
		t.Fatalf("LinkSessionToPlan() second error = %v", err)
	}
	got, err = store.GetSessionPlanID(sessionID)
	if err != nil {
		t.Fatalf("GetSessionPlanID() error = %v", err)
	}
	if got != secondPlan {
		t.Fatalf("GetSessionPlanID() after overwrite = %s, want %s", got, secondPlan)
	}
}

func TestGetSessionPlanIDNoRows(t *testing.T) {
	store := setupTestStore(t)
	defer cleanupTestStore(store)

	createTestSession(store, "session-plan-2")

	got, err := store.GetSessionPlanID("session-plan-2")
	if err != nil {
		t.Fatalf("GetSessionPlanID() error = %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty plan id, got %s", got)
	}
}
