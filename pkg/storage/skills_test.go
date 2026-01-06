package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSaveSessionSkill(t *testing.T) {
	store := setupTestStore(t)
	defer cleanupTestStore(store)

	sessionID := "test-session-1"
	skillName := "test-skill"

	// Create session first
	err := createTestSession(store, sessionID)
	if err != nil {
		t.Fatalf("Failed to create test session: %v", err)
	}

	// Save skill activation
	err = store.SaveSessionSkill(sessionID, skillName, "user", "test-scope")
	if err != nil {
		t.Errorf("SaveSessionSkill() error = %v", err)
	}

	// Verify skill was saved
	skills, err := store.GetActiveSessionSkills(sessionID)
	if err != nil {
		t.Fatalf("GetActiveSessionSkills() error = %v", err)
	}

	if len(skills) != 1 {
		t.Fatalf("GetActiveSessionSkills() returned %d skills, want 1", len(skills))
	}

	skill := skills[0]
	if skill.SkillName != skillName {
		t.Errorf("SkillName = %s, want %s", skill.SkillName, skillName)
	}
	if skill.ActivatedBy != "user" {
		t.Errorf("ActivatedBy = %s, want user", skill.ActivatedBy)
	}
	if skill.Scope != "test-scope" {
		t.Errorf("Scope = %s, want test-scope", skill.Scope)
	}
	if !skill.IsActive {
		t.Error("IsActive = false, want true")
	}
}

func TestSaveSessionSkill_UpdateExisting(t *testing.T) {
	store := setupTestStore(t)
	defer cleanupTestStore(store)

	sessionID := "test-session-2"
	skillName := "test-skill"

	createTestSession(store, sessionID)

	// Save initial activation
	err := store.SaveSessionSkill(sessionID, skillName, "user", "scope-1")
	if err != nil {
		t.Fatalf("First SaveSessionSkill() error = %v", err)
	}

	// Wait a bit to ensure timestamp difference
	time.Sleep(10 * time.Millisecond)

	// Update with new scope and activatedBy
	err = store.SaveSessionSkill(sessionID, skillName, "model", "scope-2")
	if err != nil {
		t.Errorf("Second SaveSessionSkill() error = %v", err)
	}

	// Verify only one active skill exists with updated values
	skills, err := store.GetActiveSessionSkills(sessionID)
	if err != nil {
		t.Fatalf("GetActiveSessionSkills() error = %v", err)
	}

	if len(skills) != 1 {
		t.Fatalf("GetActiveSessionSkills() returned %d skills, want 1", len(skills))
	}

	skill := skills[0]
	if skill.ActivatedBy != "model" {
		t.Errorf("ActivatedBy = %s, want model", skill.ActivatedBy)
	}
	if skill.Scope != "scope-2" {
		t.Errorf("Scope = %s, want scope-2", skill.Scope)
	}
	if !skill.IsActive {
		t.Error("IsActive = false, want true after update")
	}
}

func TestDeactivateSessionSkill(t *testing.T) {
	store := setupTestStore(t)
	defer cleanupTestStore(store)

	sessionID := "test-session-3"
	skillName := "test-skill"

	createTestSession(store, sessionID)
	store.SaveSessionSkill(sessionID, skillName, "user", "test-scope")

	// Deactivate skill
	err := store.DeactivateSessionSkill(sessionID, skillName)
	if err != nil {
		t.Errorf("DeactivateSessionSkill() error = %v", err)
	}

	// Verify no active skills
	skills, err := store.GetActiveSessionSkills(sessionID)
	if err != nil {
		t.Fatalf("GetActiveSessionSkills() error = %v", err)
	}

	if len(skills) != 0 {
		t.Errorf("GetActiveSessionSkills() returned %d skills, want 0 after deactivation", len(skills))
	}

	// Verify skill exists in history as inactive
	history, err := store.GetSessionSkillHistory(sessionID)
	if err != nil {
		t.Fatalf("GetSessionSkillHistory() error = %v", err)
	}

	if len(history) != 1 {
		t.Fatalf("GetSessionSkillHistory() returned %d skills, want 1", len(history))
	}

	if history[0].IsActive {
		t.Error("Skill still active in history")
	}
	if history[0].DeactivatedAt == nil {
		t.Error("DeactivatedAt is nil, want timestamp")
	}
}

func TestDeactivateSessionSkill_NotFound(t *testing.T) {
	store := setupTestStore(t)
	defer cleanupTestStore(store)

	sessionID := "test-session-4"
	createTestSession(store, sessionID)

	// Try to deactivate non-existent skill
	err := store.DeactivateSessionSkill(sessionID, "nonexistent-skill")
	if err == nil {
		t.Error("DeactivateSessionSkill() error = nil, want error for non-existent skill")
	}
}

func TestGetActiveSessionSkills_MultipleSkills(t *testing.T) {
	store := setupTestStore(t)
	defer cleanupTestStore(store)

	sessionID := "test-session-5"
	createTestSession(store, sessionID)

	// Activate multiple skills
	skills := []string{"skill-1", "skill-2", "skill-3"}
	for i, name := range skills {
		time.Sleep(2 * time.Millisecond) // Ensure different timestamps
		err := store.SaveSessionSkill(sessionID, name, "user", "scope")
		if err != nil {
			t.Fatalf("SaveSessionSkill(%s) error = %v", name, err)
		}

		// Deactivate skill-2 to test filtering
		if i == 1 {
			store.DeactivateSessionSkill(sessionID, name)
		}
	}

	// Get active skills (should not include skill-2)
	activeSkills, err := store.GetActiveSessionSkills(sessionID)
	if err != nil {
		t.Fatalf("GetActiveSessionSkills() error = %v", err)
	}

	if len(activeSkills) != 2 {
		t.Errorf("GetActiveSessionSkills() returned %d skills, want 2", len(activeSkills))
	}

	// Verify ordering (should be by activation time)
	if len(activeSkills) >= 2 {
		if activeSkills[0].ActivatedAt.After(activeSkills[1].ActivatedAt) {
			t.Error("Skills not ordered by activation time (oldest first)")
		}
	}

	// Verify skill-2 is not in active list
	for _, skill := range activeSkills {
		if skill.SkillName == "skill-2" {
			t.Error("skill-2 should not be in active skills list")
		}
	}
}

func TestGetSessionSkillHistory(t *testing.T) {
	store := setupTestStore(t)
	defer cleanupTestStore(store)

	sessionID := "test-session-6"
	createTestSession(store, sessionID)

	// Activate, deactivate, and reactivate a skill
	store.SaveSessionSkill(sessionID, "skill-1", "user", "scope-1")
	time.Sleep(5 * time.Millisecond)
	store.DeactivateSessionSkill(sessionID, "skill-1")
	time.Sleep(5 * time.Millisecond)
	store.SaveSessionSkill(sessionID, "skill-1", "model", "scope-2")

	// Also add another skill
	store.SaveSessionSkill(sessionID, "skill-2", "phase", "scope-3")

	// Get history (should include all activations, ordered by most recent first)
	history, err := store.GetSessionSkillHistory(sessionID)
	if err != nil {
		t.Fatalf("GetSessionSkillHistory() error = %v", err)
	}

	// Should have 2 entries (skill-1 latest activation and skill-2)
	if len(history) != 2 {
		t.Errorf("GetSessionSkillHistory() returned %d entries, want 2", len(history))
	}

	// Verify ordering (most recent first)
	if len(history) >= 2 {
		if history[0].ActivatedAt.Before(history[1].ActivatedAt) {
			t.Error("History not ordered by activation time (newest first)")
		}
	}

	// Verify skill-1 is currently active with new scope
	var skill1Found bool
	for _, skill := range history {
		if skill.SkillName == "skill-1" && skill.IsActive {
			skill1Found = true
			if skill.Scope != "scope-2" {
				t.Errorf("skill-1 Scope = %s, want scope-2", skill.Scope)
			}
			if skill.ActivatedBy != "model" {
				t.Errorf("skill-1 ActivatedBy = %s, want model", skill.ActivatedBy)
			}
		}
	}
	if !skill1Found {
		t.Error("skill-1 not found as active in history")
	}
}

func TestDeactivateAllSessionSkills(t *testing.T) {
	store := setupTestStore(t)
	defer cleanupTestStore(store)

	sessionID := "test-session-7"
	createTestSession(store, sessionID)

	// Activate multiple skills
	skills := []string{"skill-1", "skill-2", "skill-3"}
	for _, name := range skills {
		store.SaveSessionSkill(sessionID, name, "user", "scope")
	}

	eventCh := make(chan Event, len(skills))
	store.AddObserver(ObserverFunc(func(e Event) {
		if e.Type == EventSkillDeactivated && e.SessionID == sessionID {
			eventCh <- e
		}
	}))

	// Verify all are active
	activeSkills, _ := store.GetActiveSessionSkills(sessionID)
	if len(activeSkills) != 3 {
		t.Fatalf("Setup: expected 3 active skills, got %d", len(activeSkills))
	}

	// Deactivate all
	err := store.DeactivateAllSessionSkills(sessionID)
	if err != nil {
		t.Errorf("DeactivateAllSessionSkills() error = %v", err)
	}

	// Verify no active skills
	activeSkills, err = store.GetActiveSessionSkills(sessionID)
	if err != nil {
		t.Fatalf("GetActiveSessionSkills() error = %v", err)
	}

	if len(activeSkills) != 0 {
		t.Errorf("GetActiveSessionSkills() returned %d skills after DeactivateAll, want 0", len(activeSkills))
	}

	// Verify all exist in history as inactive
	history, err := store.GetSessionSkillHistory(sessionID)
	if err != nil {
		t.Fatalf("GetSessionSkillHistory() error = %v", err)
	}

	if len(history) != 3 {
		t.Errorf("GetSessionSkillHistory() returned %d entries, want 3", len(history))
	}

	for _, skill := range history {
		if skill.IsActive {
			t.Errorf("Skill %s still active after DeactivateAll", skill.SkillName)
		}
		if skill.DeactivatedAt == nil {
			t.Errorf("Skill %s has nil DeactivatedAt", skill.SkillName)
		}
	}

	// Ensure events were emitted for each skill
	events := map[string]bool{}
	timeout := time.After(500 * time.Millisecond)
	for len(events) < len(skills) {
		select {
		case evt := <-eventCh:
			events[evt.EntityID] = true
		case <-timeout:
			t.Fatalf("timed out waiting for skill deactivation events, got %d", len(events))
		}
	}
}

func TestSessionSkill_CascadeDelete(t *testing.T) {
	store := setupTestStore(t)
	defer cleanupTestStore(store)

	sessionID := "test-session-8"
	createTestSession(store, sessionID)

	// Activate skills
	store.SaveSessionSkill(sessionID, "skill-1", "user", "scope")
	store.SaveSessionSkill(sessionID, "skill-2", "model", "scope")

	// Delete session (should cascade to skills)
	_, err := store.db.Exec("DELETE FROM sessions WHERE session_id = ?", sessionID)
	if err != nil {
		t.Fatalf("Failed to delete session: %v", err)
	}

	// Verify skills were deleted
	var count int
	err = store.db.QueryRow("SELECT COUNT(*) FROM session_skills WHERE session_id = ?", sessionID).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count session skills: %v", err)
	}

	if count != 0 {
		t.Errorf("Found %d session_skills after session deletion, want 0 (cascade delete failed)", count)
	}
}

func TestSessionSkill_UniqueConstraint(t *testing.T) {
	store := setupTestStore(t)
	defer cleanupTestStore(store)

	sessionID := "test-session-9"
	createTestSession(store, sessionID)

	// Save skill
	err := store.SaveSessionSkill(sessionID, "skill-1", "user", "scope-1")
	if err != nil {
		t.Fatalf("First SaveSessionSkill() error = %v", err)
	}

	// Save same skill again (should update, not insert)
	err = store.SaveSessionSkill(sessionID, "skill-1", "model", "scope-2")
	if err != nil {
		t.Fatalf("Second SaveSessionSkill() error = %v", err)
	}

	// Verify only one record exists
	var count int
	err = store.db.QueryRow("SELECT COUNT(*) FROM session_skills WHERE session_id = ? AND skill_name = ?",
		sessionID, "skill-1").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to count records: %v", err)
	}

	if count != 1 {
		t.Errorf("Found %d records for skill-1, want 1 (unique constraint should prevent duplicates)", count)
	}
}

func TestGetActiveSessionSkills_EmptySession(t *testing.T) {
	store := setupTestStore(t)
	defer cleanupTestStore(store)

	sessionID := "empty-session"
	createTestSession(store, sessionID)

	// Get skills from session with no activations
	skills, err := store.GetActiveSessionSkills(sessionID)
	if err != nil {
		t.Errorf("GetActiveSessionSkills() error = %v for empty session", err)
	}

	if len(skills) != 0 {
		t.Errorf("GetActiveSessionSkills() returned %d skills for empty session, want 0", len(skills))
	}
}

// Helper functions

func setupTestStore(t *testing.T) *Store {
	t.Helper()

	// Create temporary database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}

	return store
}

func cleanupTestStore(store *Store) {
	if store != nil && store.db != nil {
		store.Close()
	}
}

func createTestSession(store *Store, sessionID string) error {
	_, err := store.db.Exec(`
		INSERT INTO sessions (session_id, project_path, created_at)
		VALUES (?, '/test/path', CURRENT_TIMESTAMP)
	`, sessionID)
	return err
}
