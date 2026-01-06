package mission

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
)

// setupTestStore creates a test store with required sessions
func setupTestStore(t *testing.T) (*Store, *storage.Store) {
	t.Helper()
	dbPath := t.TempDir() + "/mission.db"
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	return NewStore(store.DB()), store
}

func TestNewStore(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	if missionStore == nil {
		t.Fatal("expected non-nil store")
	}
	if missionStore.db == nil {
		t.Fatal("expected db to be set")
	}
}

// ============================================================
// Pending Change Tests
// ============================================================

func TestStore_CreatePendingChange(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-create",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	change := &PendingChange{
		ID:        "change-create-1",
		AgentID:   "agent-1",
		SessionID: session.ID,
		FilePath:  "test.go",
		Diff:      "--- a/test.go\n+++ b/test.go\n@@ -1 +1 @@\n-old\n+new",
		Reason:    "refactor",
		Status:    "pending",
		CreatedAt: time.Now(),
	}

	err := missionStore.CreatePendingChange(change)
	if err != nil {
		t.Fatalf("failed to create pending change: %v", err)
	}

	// Verify it was created
	retrieved, err := missionStore.GetPendingChange(change.ID)
	if err != nil {
		t.Fatalf("failed to get pending change: %v", err)
	}
	if retrieved.FilePath != "test.go" {
		t.Errorf("expected FilePath 'test.go', got %s", retrieved.FilePath)
	}
	if retrieved.Status != "pending" {
		t.Errorf("expected Status 'pending', got %s", retrieved.Status)
	}
}

func TestStore_GetPendingChange_NotFound(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	_, err := missionStore.GetPendingChange("nonexistent-change")
	if err == nil {
		t.Fatal("expected error for nonexistent change")
	}
	if !strings.Contains(err.Error(), "pending change not found") {
		t.Errorf("expected 'pending change not found' error, got: %v", err)
	}
}

func TestStore_GetPendingChange_WithReviewedFields(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-reviewed",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	change := &PendingChange{
		ID:        "change-reviewed-1",
		AgentID:   "agent-1",
		SessionID: session.ID,
		FilePath:  "reviewed.go",
		Diff:      "diff content",
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	if err := missionStore.CreatePendingChange(change); err != nil {
		t.Fatalf("failed to create pending change: %v", err)
	}

	// Update with review
	if err := missionStore.UpdatePendingChangeStatus(change.ID, "approved", "tester"); err != nil {
		t.Fatalf("failed to update status: %v", err)
	}

	// Retrieve and check reviewed fields
	retrieved, err := missionStore.GetPendingChange(change.ID)
	if err != nil {
		t.Fatalf("failed to get pending change: %v", err)
	}
	if retrieved.Status != "approved" {
		t.Errorf("expected Status 'approved', got %s", retrieved.Status)
	}
	if retrieved.ReviewedBy != "tester" {
		t.Errorf("expected ReviewedBy 'tester', got %s", retrieved.ReviewedBy)
	}
	if retrieved.ReviewedAt == nil {
		t.Error("expected ReviewedAt to be set")
	}
}

func TestStore_ListPendingChanges_All(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-list-all",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Create multiple changes with different statuses
	changes := []*PendingChange{
		{ID: "list-1", AgentID: "agent-1", SessionID: session.ID, FilePath: "a.go", Diff: "d1", Status: "pending", CreatedAt: time.Now()},
		{ID: "list-2", AgentID: "agent-1", SessionID: session.ID, FilePath: "b.go", Diff: "d2", Status: "approved", CreatedAt: time.Now().Add(time.Second)},
		{ID: "list-3", AgentID: "agent-1", SessionID: session.ID, FilePath: "c.go", Diff: "d3", Status: "rejected", CreatedAt: time.Now().Add(2 * time.Second)},
	}
	for _, c := range changes {
		if err := missionStore.CreatePendingChange(c); err != nil {
			t.Fatalf("failed to create change: %v", err)
		}
	}

	// List all (no status filter)
	all, err := missionStore.ListPendingChanges("", 10)
	if err != nil {
		t.Fatalf("failed to list changes: %v", err)
	}
	if len(all) < 3 {
		t.Errorf("expected at least 3 changes, got %d", len(all))
	}
}

func TestStore_ListPendingChanges_FilteredByStatus(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-list-filtered",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Create changes
	changes := []*PendingChange{
		{ID: "filter-1", AgentID: "agent-1", SessionID: session.ID, FilePath: "a.go", Diff: "d1", Status: "pending", CreatedAt: time.Now()},
		{ID: "filter-2", AgentID: "agent-1", SessionID: session.ID, FilePath: "b.go", Diff: "d2", Status: "pending", CreatedAt: time.Now()},
		{ID: "filter-3", AgentID: "agent-1", SessionID: session.ID, FilePath: "c.go", Diff: "d3", Status: "approved", CreatedAt: time.Now()},
	}
	for _, c := range changes {
		if err := missionStore.CreatePendingChange(c); err != nil {
			t.Fatalf("failed to create change: %v", err)
		}
	}

	// List only pending
	pending, err := missionStore.ListPendingChanges("pending", 10)
	if err != nil {
		t.Fatalf("failed to list pending changes: %v", err)
	}
	for _, p := range pending {
		if p.Status != "pending" {
			t.Errorf("expected all results to be pending, got %s", p.Status)
		}
	}
}

func TestStore_ListPendingChanges_Limit(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-list-limit",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Create more changes than the limit
	for i := 0; i < 5; i++ {
		change := &PendingChange{
			ID:        "limit-" + string(rune('a'+i)),
			AgentID:   "agent-1",
			SessionID: session.ID,
			FilePath:  "file.go",
			Diff:      "diff",
			Status:    "pending",
			CreatedAt: time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := missionStore.CreatePendingChange(change); err != nil {
			t.Fatalf("failed to create change: %v", err)
		}
	}

	// List with limit of 2
	limited, err := missionStore.ListPendingChanges("", 2)
	if err != nil {
		t.Fatalf("failed to list changes: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("expected 2 changes, got %d", len(limited))
	}
}

func TestStore_UpdatePendingChangeStatus(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-update",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	change := &PendingChange{
		ID:        "update-1",
		AgentID:   "agent-1",
		SessionID: session.ID,
		FilePath:  "update.go",
		Diff:      "diff",
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	if err := missionStore.CreatePendingChange(change); err != nil {
		t.Fatalf("failed to create pending change: %v", err)
	}

	// Update to approved
	if err := missionStore.UpdatePendingChangeStatus(change.ID, "approved", "approver"); err != nil {
		t.Fatalf("failed to update status: %v", err)
	}

	// Verify
	updated, err := missionStore.GetPendingChange(change.ID)
	if err != nil {
		t.Fatalf("failed to get change: %v", err)
	}
	if updated.Status != "approved" {
		t.Errorf("expected status 'approved', got %s", updated.Status)
	}
	if updated.ReviewedBy != "approver" {
		t.Errorf("expected reviewedBy 'approver', got %s", updated.ReviewedBy)
	}
}

func TestStore_UpdatePendingChangeStatus_NotFound(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	err := missionStore.UpdatePendingChangeStatus("nonexistent", "approved", "reviewer")
	if err == nil {
		t.Fatal("expected error for nonexistent change")
	}
	if !strings.Contains(err.Error(), "pending change not found") {
		t.Errorf("expected 'pending change not found' error, got: %v", err)
	}
}

func TestStore_UpdatePendingChangeStatus_Rejected(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-reject",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	change := &PendingChange{
		ID:        "reject-1",
		AgentID:   "agent-1",
		SessionID: session.ID,
		FilePath:  "reject.go",
		Diff:      "diff",
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	if err := missionStore.CreatePendingChange(change); err != nil {
		t.Fatalf("failed to create pending change: %v", err)
	}

	// Reject
	if err := missionStore.UpdatePendingChangeStatus(change.ID, "rejected", "reviewer"); err != nil {
		t.Fatalf("failed to update status: %v", err)
	}

	// Verify
	updated, err := missionStore.GetPendingChange(change.ID)
	if err != nil {
		t.Fatalf("failed to get change: %v", err)
	}
	if updated.Status != "rejected" {
		t.Errorf("expected status 'rejected', got %s", updated.Status)
	}
}

// ============================================================
// Agent Activity Tests
// ============================================================

func TestStore_RecordAgentActivity(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-record",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	activity := &AgentActivity{
		AgentID:   "agent-record-1",
		SessionID: session.ID,
		AgentType: "builder",
		Action:    "execute",
		Details:   "running tests",
		Status:    "working",
		Timestamp: time.Now(),
	}

	err := missionStore.RecordAgentActivity(activity)
	if err != nil {
		t.Fatalf("failed to record activity: %v", err)
	}

	if activity.ID == 0 {
		t.Error("expected ID to be set after recording")
	}
}

func TestStore_RecordAgentActivity_MultipleActivities(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-multi-activity",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Record multiple activities for the same agent
	for i := 0; i < 3; i++ {
		activity := &AgentActivity{
			AgentID:   "agent-multi",
			SessionID: session.ID,
			AgentType: "builder",
			Action:    "step",
			Details:   "step " + string(rune('1'+i)),
			Status:    "working",
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := missionStore.RecordAgentActivity(activity); err != nil {
			t.Fatalf("failed to record activity: %v", err)
		}
	}

	// Verify all were recorded
	activities, err := missionStore.GetAgentActivity("agent-multi", 10)
	if err != nil {
		t.Fatalf("failed to get activities: %v", err)
	}
	if len(activities) != 3 {
		t.Errorf("expected 3 activities, got %d", len(activities))
	}
}

func TestStore_GetAgentStatus(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-status",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Record activity
	activity := &AgentActivity{
		AgentID:   "agent-status-1",
		SessionID: session.ID,
		AgentType: "reviewer",
		Action:    "review",
		Details:   "reviewing code",
		Status:    "working",
		Timestamp: time.Now(),
	}
	if err := missionStore.RecordAgentActivity(activity); err != nil {
		t.Fatalf("failed to record activity: %v", err)
	}

	// Create a pending change for this agent
	change := &PendingChange{
		ID:        "status-change-1",
		AgentID:   "agent-status-1",
		SessionID: session.ID,
		FilePath:  "status.go",
		Diff:      "diff",
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	if err := missionStore.CreatePendingChange(change); err != nil {
		t.Fatalf("failed to create pending change: %v", err)
	}

	// Get status
	status, err := missionStore.GetAgentStatus("agent-status-1")
	if err != nil {
		t.Fatalf("failed to get agent status: %v", err)
	}

	if status.AgentID != "agent-status-1" {
		t.Errorf("expected AgentID 'agent-status-1', got %s", status.AgentID)
	}
	if status.AgentType != "reviewer" {
		t.Errorf("expected AgentType 'reviewer', got %s", status.AgentType)
	}
	if status.Status != "working" {
		t.Errorf("expected Status 'working', got %s", status.Status)
	}
	if status.ActivityCount != 1 {
		t.Errorf("expected ActivityCount 1, got %d", status.ActivityCount)
	}
	if status.PendingChanges != 1 {
		t.Errorf("expected PendingChanges 1, got %d", status.PendingChanges)
	}
}

func TestStore_GetAgentStatus_NotFound(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	_, err := missionStore.GetAgentStatus("nonexistent-agent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
	if !strings.Contains(err.Error(), "agent not found") {
		t.Errorf("expected 'agent not found' error, got: %v", err)
	}
}

func TestStore_ListActiveAgents(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-active-agents",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Create activities for multiple agents
	agents := []string{"active-agent-1", "active-agent-2", "active-agent-3"}
	for _, agentID := range agents {
		activity := &AgentActivity{
			AgentID:   agentID,
			SessionID: session.ID,
			AgentType: "builder",
			Action:    "work",
			Status:    "working",
			Timestamp: time.Now(),
		}
		if err := missionStore.RecordAgentActivity(activity); err != nil {
			t.Fatalf("failed to record activity: %v", err)
		}
	}

	// List active agents within the last hour
	statuses, err := missionStore.ListActiveAgents(time.Hour)
	if err != nil {
		t.Fatalf("failed to list active agents: %v", err)
	}

	if len(statuses) < 3 {
		t.Errorf("expected at least 3 active agents, got %d", len(statuses))
	}
}

func TestStore_ListActiveAgents_Empty(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	// List with no activity recorded
	statuses, err := missionStore.ListActiveAgents(time.Hour)
	if err != nil {
		t.Fatalf("failed to list active agents: %v", err)
	}

	// Should return empty slice, not error
	if statuses == nil {
		// Nil is acceptable for empty result
	}
}

func TestStore_ListActiveAgents_OldActivity(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-old-activity",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Create activity from 2 hours ago
	activity := &AgentActivity{
		AgentID:   "old-agent",
		SessionID: session.ID,
		AgentType: "builder",
		Action:    "work",
		Status:    "idle",
		Timestamp: time.Now().Add(-2 * time.Hour),
	}
	if err := missionStore.RecordAgentActivity(activity); err != nil {
		t.Fatalf("failed to record activity: %v", err)
	}

	// List agents active in the last hour (should not include old-agent)
	statuses, err := missionStore.ListActiveAgents(time.Hour)
	if err != nil {
		t.Fatalf("failed to list active agents: %v", err)
	}

	for _, s := range statuses {
		if s.AgentID == "old-agent" {
			t.Error("old-agent should not be in active list")
		}
	}
}

func TestStore_GetAgentActivity(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-get-activity",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Record multiple activities
	for i := 0; i < 5; i++ {
		activity := &AgentActivity{
			AgentID:   "activity-agent",
			SessionID: session.ID,
			AgentType: "builder",
			Action:    "step " + string(rune('A'+i)),
			Status:    "working",
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		}
		if err := missionStore.RecordAgentActivity(activity); err != nil {
			t.Fatalf("failed to record activity: %v", err)
		}
	}

	// Get activities with limit
	activities, err := missionStore.GetAgentActivity("activity-agent", 3)
	if err != nil {
		t.Fatalf("failed to get activities: %v", err)
	}

	if len(activities) != 3 {
		t.Errorf("expected 3 activities, got %d", len(activities))
	}

	// Verify order (most recent first due to ORDER BY DESC)
	if activities[0].Timestamp.Before(activities[1].Timestamp) {
		t.Error("activities should be ordered by timestamp DESC")
	}
}

func TestStore_GetAgentActivity_Empty(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	activities, err := missionStore.GetAgentActivity("nonexistent-agent", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(activities) != 0 {
		t.Errorf("expected empty activities, got %d", len(activities))
	}
}

// ============================================================
// WaitForDecision Tests
// ============================================================

func TestStore_WaitForDecision_AlreadyDecided(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-already-decided",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	change := &PendingChange{
		ID:        "already-decided-1",
		AgentID:   "agent-1",
		SessionID: session.ID,
		FilePath:  "file.go",
		Diff:      "diff",
		Status:    "approved", // Already decided
		CreatedAt: time.Now(),
	}
	if err := missionStore.CreatePendingChange(change); err != nil {
		t.Fatalf("failed to create pending change: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Should return immediately since already decided
	decision, err := missionStore.WaitForDecision(ctx, change.ID, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Status != "approved" {
		t.Errorf("expected approved, got %s", decision.Status)
	}
}

func TestStore_WaitForDecision_Timeout(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-timeout",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	change := &PendingChange{
		ID:        "timeout-1",
		AgentID:   "agent-1",
		SessionID: session.ID,
		FilePath:  "file.go",
		Diff:      "diff",
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	if err := missionStore.CreatePendingChange(change); err != nil {
		t.Fatalf("failed to create pending change: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Should timeout since we never update status
	_, err := missionStore.WaitForDecision(ctx, change.ID, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if err != context.DeadlineExceeded {
		t.Errorf("expected DeadlineExceeded, got: %v", err)
	}
}

func TestStore_WaitForDecision_Cancelled(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-cancelled",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	change := &PendingChange{
		ID:        "cancelled-1",
		AgentID:   "agent-1",
		SessionID: session.ID,
		FilePath:  "file.go",
		Diff:      "diff",
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	if err := missionStore.CreatePendingChange(change); err != nil {
		t.Fatalf("failed to create pending change: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := missionStore.WaitForDecision(ctx, change.ID, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if err != context.Canceled {
		t.Errorf("expected Canceled, got: %v", err)
	}
}

func TestStore_WaitForDecision_NotFound(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()
	_, err := missionStore.WaitForDecision(ctx, "nonexistent-change", 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected error for nonexistent change")
	}
	if !strings.Contains(err.Error(), "pending change not found") {
		t.Errorf("expected 'pending change not found' error, got: %v", err)
	}
}

func TestStore_WaitForDecision_Rejected(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-wait-rejected",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	change := &PendingChange{
		ID:        "wait-reject-1",
		AgentID:   "agent-1",
		SessionID: session.ID,
		FilePath:  "file.go",
		Diff:      "diff",
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	if err := missionStore.CreatePendingChange(change); err != nil {
		t.Fatalf("failed to create pending change: %v", err)
	}

	// Update to rejected in background
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = missionStore.UpdatePendingChangeStatus(change.ID, "rejected", "rejector")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	decision, err := missionStore.WaitForDecision(ctx, change.ID, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Status != "rejected" {
		t.Errorf("expected rejected, got %s", decision.Status)
	}
}

func TestStore_WaitForDecision_DefaultPollInterval(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-default-poll",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	change := &PendingChange{
		ID:        "default-poll-1",
		AgentID:   "agent-1",
		SessionID: session.ID,
		FilePath:  "file.go",
		Diff:      "diff",
		Status:    "approved", // Already decided
		CreatedAt: time.Now(),
	}
	if err := missionStore.CreatePendingChange(change); err != nil {
		t.Fatalf("failed to create pending change: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Pass 0 poll interval to use default
	decision, err := missionStore.WaitForDecision(ctx, change.ID, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision.Status != "approved" {
		t.Errorf("expected approved, got %s", decision.Status)
	}
}

// ============================================================
// Session Activity Tests
// ============================================================

func TestStore_ListSessionActivity_Empty(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	activities, err := missionStore.ListSessionActivity("nonexistent-session", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(activities) != 0 {
		t.Errorf("expected empty activities, got %d", len(activities))
	}
}

func TestStore_ListSessionActivity_DefaultLimit(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-default-limit",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	activity := &AgentActivity{
		AgentID:   "agent-default-limit",
		SessionID: session.ID,
		AgentType: "builder",
		Action:    "work",
		Status:    "working",
		Timestamp: time.Now(),
	}
	if err := missionStore.RecordAgentActivity(activity); err != nil {
		t.Fatalf("failed to record activity: %v", err)
	}

	// Pass 0 or negative limit to use default (50)
	activities, err := missionStore.ListSessionActivity(session.ID, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(activities) != 1 {
		t.Errorf("expected 1 activity, got %d", len(activities))
	}
}

func TestStore_ListSessionActivity_MultipleAgents(t *testing.T) {
	missionStore, store := setupTestStore(t)
	defer store.Close()

	session := &storage.Session{
		ID:         "session-multi-agents",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Create activities from different agents in the same session
	agents := []string{"agent-a", "agent-b", "agent-c"}
	for _, agentID := range agents {
		activity := &AgentActivity{
			AgentID:   agentID,
			SessionID: session.ID,
			AgentType: "builder",
			Action:    "work",
			Status:    "working",
			Timestamp: time.Now(),
		}
		if err := missionStore.RecordAgentActivity(activity); err != nil {
			t.Fatalf("failed to record activity: %v", err)
		}
	}

	activities, err := missionStore.ListSessionActivity(session.ID, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(activities) != 3 {
		t.Errorf("expected 3 activities, got %d", len(activities))
	}
}
