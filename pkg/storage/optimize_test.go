package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOptimizeDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.OptimizeDatabase(); err != nil {
		t.Fatalf("OptimizeDatabase failed: %v", err)
	}

	// Verify WAL mode is enabled
	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("Failed to check journal mode: %v", err)
	}

	if journalMode != "wal" {
		t.Errorf("Expected WAL journal mode, got %q", journalMode)
	}
}

func TestPrepareStatements(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ps, err := store.PrepareStatements()
	if err != nil {
		t.Fatalf("PrepareStatements failed: %v", err)
	}
	defer ps.Close()

	// Verify all statements are non-nil
	if ps.insertMessage == nil {
		t.Error("insertMessage statement is nil")
	}
	if ps.getMessage == nil {
		t.Error("getMessage statement is nil")
	}
	if ps.listMessages == nil {
		t.Error("listMessages statement is nil")
	}
	if ps.insertAPICall == nil {
		t.Error("insertAPICall statement is nil")
	}
	if ps.updateSession == nil {
		t.Error("updateSession statement is nil")
	}
	if ps.getSession == nil {
		t.Error("getSession statement is nil")
	}
}

func TestPreparedStatements_Close(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	ps, err := store.PrepareStatements()
	if err != nil {
		t.Fatalf("PrepareStatements failed: %v", err)
	}

	if err := ps.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

func TestVacuumDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.VacuumDatabase(); err != nil {
		t.Errorf("VacuumDatabase failed: %v", err)
	}
}

func TestAnalyzeDatabase(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	if err := store.AnalyzeDatabase(); err != nil {
		t.Errorf("AnalyzeDatabase failed: %v", err)
	}
}

func TestGetDatabaseStats(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	stats, err := store.GetDatabaseStats()
	if err != nil {
		t.Fatalf("GetDatabaseStats failed: %v", err)
	}

	// Check expected keys
	expectedKeys := []string{
		"sessions_count",
		"messages_count",
		"api_calls_count",
		"embeddings_count",
		"size_bytes",
		"size_mb",
	}

	for _, key := range expectedKeys {
		if _, exists := stats[key]; !exists {
			t.Errorf("Stats missing key: %s", key)
		}
	}

	// Verify size is reasonable
	sizeBytes, ok := stats["size_bytes"].(int)
	if !ok {
		t.Error("size_bytes should be int")
	}

	if sizeBytes <= 0 {
		t.Error("size_bytes should be positive")
	}
}

// Benchmark tests

func BenchmarkInsertMessage(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := New(dbPath)
	if err != nil {
		b.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	// Optimize first
	store.OptimizeDatabase()

	// Create session
	session := &Session{
		ID:          "bench-session",
		ProjectPath: "/test",
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
	}
	store.CreateSession(session)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		msg := &Message{
			SessionID: "bench-session",
			Role:      "user",
			Content:   "benchmark message",
			Timestamp: time.Now(),
			Tokens:    10,
		}
		store.SaveMessage(msg)
	}
}

func BenchmarkInsertMessagePrepared(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := New(dbPath)
	if err != nil {
		b.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	store.OptimizeDatabase()

	// Prepare statements
	ps, err := store.PrepareStatements()
	if err != nil {
		b.Fatalf("Failed to prepare: %v", err)
	}
	defer ps.Close()

	// Create session
	session := &Session{
		ID:          "bench-session",
		ProjectPath: "/test",
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
	}
	store.CreateSession(session)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := ps.insertMessage.Exec(
			"bench-session",
			"user",
			"benchmark message",
			time.Now(),
			10,
			false,
		)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkListMessages(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := New(dbPath)
	if err != nil {
		b.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	store.OptimizeDatabase()

	// Create session and messages
	session := &Session{
		ID:          "bench-session",
		ProjectPath: "/test",
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
	}
	store.CreateSession(session)

	// Insert 100 messages
	for i := 0; i < 100; i++ {
		msg := &Message{
			SessionID: "bench-session",
			Role:      "user",
			Content:   "test message",
			Timestamp: time.Now(),
			Tokens:    10,
		}
		store.SaveMessage(msg)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := store.GetAllMessages("bench-session")
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGetDatabaseStats(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")

	store, err := New(dbPath)
	if err != nil {
		b.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	store.OptimizeDatabase()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := store.GetDatabaseStats()
		if err != nil {
			b.Fatal(err)
		}
	}
}
