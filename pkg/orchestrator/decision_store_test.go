package orchestrator

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test database: %v", err)
	}
	return db
}

func TestNewSQLiteDecisionStore(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store, err := NewSQLiteDecisionStore(db)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	if store == nil {
		t.Fatal("Expected non-nil store")
	}
}

func TestSQLiteDecisionStore_SaveAndGet(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store, err := NewSQLiteDecisionStore(db)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	decision := &Decision{
		SessionID:   "test-session",
		Timestamp:   time.Now(),
		Context:     "Test task context",
		Options:     []string{"Option A", "Option B", "Option C"},
		Selected:    1,
		Reasoning:   "Option B is best because...",
		AutoDecided: false,
	}

	// Save decision
	if err := store.SaveDecision(decision); err != nil {
		t.Fatalf("Failed to save decision: %v", err)
	}

	// Retrieve decisions
	decisions, err := store.GetDecisions("test-session")
	if err != nil {
		t.Fatalf("Failed to get decisions: %v", err)
	}

	if len(decisions) != 1 {
		t.Fatalf("Expected 1 decision, got %d", len(decisions))
	}

	d := decisions[0]
	if d.SessionID != "test-session" {
		t.Errorf("Expected session 'test-session', got %q", d.SessionID)
	}
	if d.Context != "Test task context" {
		t.Errorf("Expected context 'Test task context', got %q", d.Context)
	}
	if len(d.Options) != 3 {
		t.Errorf("Expected 3 options, got %d", len(d.Options))
	}
	if d.Selected != 1 {
		t.Errorf("Expected selected 1, got %d", d.Selected)
	}
	if d.Reasoning != "Option B is best because..." {
		t.Errorf("Expected reasoning 'Option B is best because...', got %q", d.Reasoning)
	}
	if d.AutoDecided {
		t.Error("Expected AutoDecided to be false")
	}
}

func TestSQLiteDecisionStore_GetRecentDecisions(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store, err := NewSQLiteDecisionStore(db)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	// Save multiple decisions
	for i := 0; i < 5; i++ {
		decision := &Decision{
			SessionID:   "test-session",
			Timestamp:   time.Now().Add(time.Duration(i) * time.Minute),
			Context:     "Task " + string(rune('A'+i)),
			Options:     []string{"Option 1", "Option 2"},
			Selected:    0,
			AutoDecided: i%2 == 0,
		}
		if err := store.SaveDecision(decision); err != nil {
			t.Fatalf("Failed to save decision %d: %v", i, err)
		}
	}

	// Get recent 3
	decisions, err := store.GetRecentDecisions("test-session", 3)
	if err != nil {
		t.Fatalf("Failed to get recent decisions: %v", err)
	}

	if len(decisions) != 3 {
		t.Fatalf("Expected 3 decisions, got %d", len(decisions))
	}

	// Should be in chronological order (oldest first)
	if decisions[0].Context != "Task C" {
		t.Errorf("Expected first decision to be 'Task C', got %q", decisions[0].Context)
	}
	if decisions[2].Context != "Task E" {
		t.Errorf("Expected last decision to be 'Task E', got %q", decisions[2].Context)
	}
}

func TestSQLiteDecisionStore_ClearDecisions(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store, err := NewSQLiteDecisionStore(db)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	// Save some decisions
	for i := 0; i < 3; i++ {
		decision := &Decision{
			SessionID:   "test-session",
			Timestamp:   time.Now(),
			Context:     "Task",
			Options:     []string{"A", "B"},
			Selected:    0,
			AutoDecided: false,
		}
		store.SaveDecision(decision)
	}

	// Clear decisions
	if err := store.ClearDecisions("test-session"); err != nil {
		t.Fatalf("Failed to clear decisions: %v", err)
	}

	// Verify cleared
	decisions, err := store.GetDecisions("test-session")
	if err != nil {
		t.Fatalf("Failed to get decisions: %v", err)
	}
	if len(decisions) != 0 {
		t.Errorf("Expected 0 decisions after clear, got %d", len(decisions))
	}
}

func TestSQLiteDecisionStore_SessionIsolation(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store, err := NewSQLiteDecisionStore(db)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	// Save decisions for different sessions
	store.SaveDecision(&Decision{
		SessionID: "session-a",
		Timestamp: time.Now(),
		Context:   "Task A",
		Options:   []string{"X"},
		Selected:  0,
	})
	store.SaveDecision(&Decision{
		SessionID: "session-b",
		Timestamp: time.Now(),
		Context:   "Task B",
		Options:   []string{"Y"},
		Selected:  0,
	})

	// Check session isolation
	decisionsA, _ := store.GetDecisions("session-a")
	decisionsB, _ := store.GetDecisions("session-b")

	if len(decisionsA) != 1 || decisionsA[0].Context != "Task A" {
		t.Error("Session A should only see its own decisions")
	}
	if len(decisionsB) != 1 || decisionsB[0].Context != "Task B" {
		t.Error("Session B should only see its own decisions")
	}
}

func TestInMemoryDecisionStore(t *testing.T) {
	store := NewInMemoryDecisionStore()

	decision := &Decision{
		SessionID:   "test-session",
		Timestamp:   time.Now(),
		Context:     "Test task",
		Options:     []string{"A", "B"},
		Selected:    0,
		AutoDecided: true,
	}

	// Save
	if err := store.SaveDecision(decision); err != nil {
		t.Fatalf("Failed to save: %v", err)
	}

	// Get
	decisions, err := store.GetDecisions("test-session")
	if err != nil {
		t.Fatalf("Failed to get: %v", err)
	}
	if len(decisions) != 1 {
		t.Fatalf("Expected 1 decision, got %d", len(decisions))
	}

	// Get recent
	store.SaveDecision(&Decision{SessionID: "test-session", Timestamp: time.Now(), Options: []string{"C"}})
	store.SaveDecision(&Decision{SessionID: "test-session", Timestamp: time.Now(), Options: []string{"D"}})

	recent, err := store.GetRecentDecisions("test-session", 2)
	if err != nil {
		t.Fatalf("Failed to get recent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("Expected 2 recent decisions, got %d", len(recent))
	}

	// Clear
	if err := store.ClearDecisions("test-session"); err != nil {
		t.Fatalf("Failed to clear: %v", err)
	}
	decisions, _ = store.GetDecisions("test-session")
	if len(decisions) != 0 {
		t.Errorf("Expected 0 decisions after clear, got %d", len(decisions))
	}
}

func TestFormatDecisionSummary_Empty(t *testing.T) {
	summary := FormatDecisionSummary(nil)
	if summary != "No planning decisions recorded" {
		t.Errorf("Unexpected summary for empty: %q", summary)
	}

	summary = FormatDecisionSummary([]Decision{})
	if summary != "No planning decisions recorded" {
		t.Errorf("Unexpected summary for empty slice: %q", summary)
	}
}

func TestFormatDecisionSummary_WithDecisions(t *testing.T) {
	decisions := []Decision{
		{
			Timestamp:   time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
			Context:     "Add user authentication system",
			Options:     []string{"JWT tokens", "Session cookies", "OAuth only"},
			Selected:    0,
			Reasoning:   "JWT provides stateless auth",
			AutoDecided: false,
		},
		{
			Timestamp:   time.Date(2024, 1, 1, 11, 0, 0, 0, time.UTC),
			Context:     "Database migration approach",
			Options:     []string{"Incremental", "Big bang"},
			Selected:    1,
			Reasoning:   "Faster completion",
			AutoDecided: true,
		},
	}

	summary := FormatDecisionSummary(decisions)

	// Check key elements are present
	if !containsString(summary, "Planning Decisions (2 total)") {
		t.Error("Expected total count in summary")
	}
	if !containsString(summary, "Auto-decided: 1") {
		t.Error("Expected auto-decided count")
	}
	if !containsString(summary, "Manual: 1") {
		t.Error("Expected manual count")
	}
	if !containsString(summary, "JWT tokens") {
		t.Error("Expected option in summary")
	}
	if !containsString(summary, "[Auto-decided in long-run mode]") {
		t.Error("Expected auto-decided label")
	}
}

func TestTruncateStringForDisplay(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10c", 10, "exactly10c"},
		{"this is a longer string", 10, "this is..."},
		{"", 10, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := truncateStringForDisplay(tt.input, tt.maxLen)
			if result != tt.expected {
				t.Errorf("truncateStringForDisplay(%q, %d) = %q, want %q",
					tt.input, tt.maxLen, result, tt.expected)
			}
		})
	}
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
