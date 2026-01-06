package checkpoint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckpoint_Format(t *testing.T) {
	cp := &Checkpoint{
		ID:          "cp_123456789",
		Name:        "Test Checkpoint",
		Description: "A test checkpoint",
		CreatedAt:   time.Now(),
		SessionID:   "session-1",
		Branch:      "main",
		Messages:    make([]Message, 10),
		TokenCount:  5000,
	}

	formatted := cp.Format()

	if !strings.Contains(formatted, "Test Checkpoint") {
		t.Error("Format() should contain checkpoint name")
	}
	if !strings.Contains(formatted, "cp_123456789") {
		t.Error("Format() should contain checkpoint ID")
	}
	if !strings.Contains(formatted, "10") {
		t.Error("Format() should contain message count")
	}
	if !strings.Contains(formatted, "5000") {
		t.Error("Format() should contain token count")
	}
}

func TestCheckpoint_FormatCompact(t *testing.T) {
	cp := &Checkpoint{
		ID:        "cp_1234567890123",
		Name:      "Test",
		CreatedAt: time.Now().Add(-30 * time.Minute),
		Messages:  make([]Message, 5),
	}

	compact := cp.FormatCompact()

	if !strings.Contains(compact, "cp_123456789") {
		t.Error("FormatCompact() should contain truncated ID")
	}
	if !strings.Contains(compact, "Test") {
		t.Error("FormatCompact() should contain name")
	}
	if !strings.Contains(compact, "5 msgs") {
		t.Error("FormatCompact() should contain message count")
	}
}

func TestStore_SaveAndLoad(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "checkpoint-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store := NewStore(tmpDir)

	cp := &Checkpoint{
		Name:       "Test Checkpoint",
		CreatedAt:  time.Now(),
		SessionID:  "test-session",
		Branch:     "main",
		TokenCount: 1000,
		Messages: []Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
		},
	}

	// Save
	err = store.Save(cp)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if cp.ID == "" {
		t.Error("Save() should generate ID")
	}

	// Load
	loaded, err := store.Load(cp.ID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if loaded.Name != cp.Name {
		t.Errorf("Name = %v, want %v", loaded.Name, cp.Name)
	}
	if loaded.SessionID != cp.SessionID {
		t.Errorf("SessionID = %v, want %v", loaded.SessionID, cp.SessionID)
	}
	if len(loaded.Messages) != len(cp.Messages) {
		t.Errorf("Messages count = %v, want %v", len(loaded.Messages), len(cp.Messages))
	}
}

func TestStore_List(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "checkpoint-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store := NewStore(tmpDir)

	// Create multiple checkpoints
	for i := 0; i < 3; i++ {
		cp := &Checkpoint{
			Name:      "Checkpoint",
			CreatedAt: time.Now().Add(time.Duration(i) * time.Hour),
			SessionID: "test-session",
		}
		store.Save(cp)
	}

	// List
	checkpoints, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(checkpoints) != 3 {
		t.Errorf("List() returned %d checkpoints, want 3", len(checkpoints))
	}

	// Verify sorted by time (newest first)
	for i := 1; i < len(checkpoints); i++ {
		if checkpoints[i].CreatedAt.After(checkpoints[i-1].CreatedAt) {
			t.Error("List() should return checkpoints sorted by time (newest first)")
		}
	}
}

func TestStore_Delete(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "checkpoint-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store := NewStore(tmpDir)

	cp := &Checkpoint{
		Name:      "To Delete",
		CreatedAt: time.Now(),
	}
	store.Save(cp)

	// Delete
	err = store.Delete(cp.ID)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Verify deleted
	_, err = store.Load(cp.ID)
	if err == nil {
		t.Error("Load() should fail for deleted checkpoint")
	}

	// Delete non-existent should not error
	err = store.Delete("nonexistent")
	if err != nil {
		t.Errorf("Delete(nonexistent) error = %v, want nil", err)
	}
}

func TestStore_ListBySession(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "checkpoint-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store := NewStore(tmpDir)

	// Create checkpoints for different sessions
	sessions := []string{"session-1", "session-1", "session-2"}
	for _, s := range sessions {
		cp := &Checkpoint{
			Name:      "Checkpoint",
			CreatedAt: time.Now(),
			SessionID: s,
		}
		store.Save(cp)
	}

	// List by session
	bySession, err := store.ListBySession("session-1")
	if err != nil {
		t.Fatalf("ListBySession() error = %v", err)
	}

	if len(bySession) != 2 {
		t.Errorf("ListBySession() returned %d, want 2", len(bySession))
	}

	for _, cp := range bySession {
		if cp.SessionID != "session-1" {
			t.Errorf("SessionID = %v, want session-1", cp.SessionID)
		}
	}
}

func TestStore_ListByBranch(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "checkpoint-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store := NewStore(tmpDir)

	branches := []string{"main", "main", "feature"}
	for _, b := range branches {
		cp := &Checkpoint{
			Name:      "Checkpoint",
			CreatedAt: time.Now(),
			Branch:    b,
		}
		store.Save(cp)
	}

	byBranch, err := store.ListByBranch("main")
	if err != nil {
		t.Fatalf("ListByBranch() error = %v", err)
	}

	if len(byBranch) != 2 {
		t.Errorf("ListByBranch() returned %d, want 2", len(byBranch))
	}
}

func TestStore_GetLatest(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "checkpoint-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store := NewStore(tmpDir)

	// Empty store
	_, err = store.GetLatest()
	if err == nil {
		t.Error("GetLatest() should fail for empty store")
	}

	// Add checkpoints
	for i := 0; i < 3; i++ {
		cp := &Checkpoint{
			Name:      "Checkpoint",
			CreatedAt: time.Now().Add(time.Duration(i) * time.Hour),
		}
		store.Save(cp)
		time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	}

	latest, err := store.GetLatest()
	if err != nil {
		t.Fatalf("GetLatest() error = %v", err)
	}

	// Should be the most recent
	all, _ := store.List()
	if latest.ID != all[0].ID {
		t.Error("GetLatest() should return the most recent checkpoint")
	}
}

func TestStore_Prune(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "checkpoint-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store := NewStore(tmpDir)

	// Create 5 checkpoints
	for i := 0; i < 5; i++ {
		cp := &Checkpoint{
			Name:      "Checkpoint",
			CreatedAt: time.Now().Add(time.Duration(i) * time.Hour),
		}
		store.Save(cp)
	}

	// Prune to keep 2
	deleted, err := store.Prune(2)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}

	if deleted != 3 {
		t.Errorf("Prune() deleted %d, want 3", deleted)
	}

	remaining, _ := store.List()
	if len(remaining) != 2 {
		t.Errorf("After Prune(), %d remaining, want 2", len(remaining))
	}
}

func TestManager_CreateAndRestore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "checkpoint-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store := NewStore(tmpDir)
	manager := NewManager(store, "test-session", "main")

	messages := []Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi!"},
	}

	// Create
	cp, err := manager.CreateCheckpoint("Test", "A test checkpoint", messages, 100)
	if err != nil {
		t.Fatalf("CreateCheckpoint() error = %v", err)
	}

	if cp.Name != "Test" {
		t.Errorf("Name = %v, want Test", cp.Name)
	}

	// Restore
	restored, err := manager.RestoreCheckpoint(cp.ID)
	if err != nil {
		t.Fatalf("RestoreCheckpoint() error = %v", err)
	}

	if len(restored.Messages) != 2 {
		t.Errorf("Messages count = %d, want 2", len(restored.Messages))
	}
}

func TestNewStore_DefaultPath(t *testing.T) {
	t.Setenv(envBuckleyCheckpointsDir, "")
	t.Setenv(envBuckleyDataDir, "")
	store := NewStore("")

	home, _ := os.UserHomeDir()
	expectedPath := filepath.Join(home, ".buckley", "checkpoints")

	if store.baseDir != expectedPath {
		t.Errorf("baseDir = %v, want %v", store.baseDir, expectedPath)
	}
}

func TestNewStore_RespectsBuckleyDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyCheckpointsDir, "")
	t.Setenv(envBuckleyDataDir, "~/data")

	store := NewStore("")
	expectedPath := filepath.Join(home, "data", "checkpoints")
	if store.baseDir != expectedPath {
		t.Errorf("baseDir = %v, want %v", store.baseDir, expectedPath)
	}
}

func TestNewStore_RespectsExplicitCheckpointsDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(envBuckleyCheckpointsDir, "~/cp")
	t.Setenv(envBuckleyDataDir, "~/ignored")

	store := NewStore("")
	expectedPath := filepath.Join(home, "cp")
	if store.baseDir != expectedPath {
		t.Errorf("baseDir = %v, want %v", store.baseDir, expectedPath)
	}
}

func TestFormatAge(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{2 * time.Hour, "2h"},
		{48 * time.Hour, "2d"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatAge(tt.duration); got != tt.want {
				t.Errorf("formatAge(%v) = %v, want %v", tt.duration, got, tt.want)
			}
		})
	}
}

func TestMessage(t *testing.T) {
	msg := Message{
		Role:      "user",
		Content:   "Hello, world!",
		Timestamp: time.Now().Unix(),
	}

	if msg.Role != "user" {
		t.Errorf("Role = %v, want user", msg.Role)
	}
	if msg.Content != "Hello, world!" {
		t.Errorf("Content = %v", msg.Content)
	}
}
