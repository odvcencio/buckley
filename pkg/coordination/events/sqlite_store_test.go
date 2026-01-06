package events

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// setupTestDB creates a temporary test database
func setupTestDB(t *testing.T) (*SQLiteEventStore, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}

	cleanup := func() {
		if err := store.Close(); err != nil {
			t.Errorf("failed to close store: %v", err)
		}
	}

	return store, cleanup
}

func TestNewSQLiteEventStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v, want nil", err)
	}
	defer store.Close()

	// Verify database file was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Error("database file was not created")
	}

	// Verify tables exist
	db := store.DB()
	tables := []string{"events", "snapshots"}
	for _, table := range tables {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %s does not exist: %v", table, err)
		}
	}
}

func TestAppendSingleEvent(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	streamID := "test-stream-1"

	event := Event{
		StreamID: streamID,
		Type:     "TestEvent",
		Version:  1,
		Data: map[string]interface{}{
			"key":   "value",
			"count": float64(42),
		},
		Metadata: map[string]string{
			"user": "test-user",
		},
		Timestamp: time.Now().UTC(),
	}

	err := store.Append(ctx, streamID, []Event{event})
	if err != nil {
		t.Fatalf("Append() error = %v, want nil", err)
	}

	// Read back the event
	events, err := store.Read(ctx, streamID, 0)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}

	if len(events) != 1 {
		t.Fatalf("Read() returned %d events, want 1", len(events))
	}

	got := events[0]
	if got.StreamID != event.StreamID {
		t.Errorf("StreamID = %s, want %s", got.StreamID, event.StreamID)
	}
	if got.Type != event.Type {
		t.Errorf("Type = %s, want %s", got.Type, event.Type)
	}
	if got.Version != event.Version {
		t.Errorf("Version = %d, want %d", got.Version, event.Version)
	}
	if got.Data["key"] != event.Data["key"] {
		t.Errorf("Data[key] = %v, want %v", got.Data["key"], event.Data["key"])
	}
	if got.Data["count"] != event.Data["count"] {
		t.Errorf("Data[count] = %v, want %v", got.Data["count"], event.Data["count"])
	}
	if got.Metadata["user"] != event.Metadata["user"] {
		t.Errorf("Metadata[user] = %s, want %s", got.Metadata["user"], event.Metadata["user"])
	}
}

func TestAppendMultipleEvents(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	streamID := "test-stream-2"

	events := []Event{
		{
			StreamID:  streamID,
			Type:      "Event1",
			Version:   1,
			Data:      map[string]interface{}{"step": "first"},
			Metadata:  map[string]string{"phase": "init"},
			Timestamp: time.Now().UTC(),
		},
		{
			StreamID:  streamID,
			Type:      "Event2",
			Version:   2,
			Data:      map[string]interface{}{"step": "second"},
			Metadata:  map[string]string{"phase": "process"},
			Timestamp: time.Now().UTC(),
		},
		{
			StreamID:  streamID,
			Type:      "Event3",
			Version:   3,
			Data:      map[string]interface{}{"step": "third"},
			Metadata:  map[string]string{"phase": "complete"},
			Timestamp: time.Now().UTC(),
		},
	}

	err := store.Append(ctx, streamID, events)
	if err != nil {
		t.Fatalf("Append() error = %v, want nil", err)
	}

	// Read all events
	retrieved, err := store.Read(ctx, streamID, 0)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}

	if len(retrieved) != 3 {
		t.Fatalf("Read() returned %d events, want 3", len(retrieved))
	}

	// Verify order and versions
	for i, event := range retrieved {
		expectedVersion := int64(i + 1)
		if event.Version != expectedVersion {
			t.Errorf("Event %d: Version = %d, want %d", i, event.Version, expectedVersion)
		}
	}
}

func TestReadFromVersion(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	streamID := "test-stream-3"

	// Append 5 events
	events := make([]Event, 5)
	for i := 0; i < 5; i++ {
		events[i] = Event{
			StreamID:  streamID,
			Type:      "TestEvent",
			Version:   int64(i + 1),
			Data:      map[string]interface{}{"index": float64(i)},
			Metadata:  map[string]string{},
			Timestamp: time.Now().UTC(),
		}
	}

	if err := store.Append(ctx, streamID, events); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	// Read from version 3
	retrieved, err := store.Read(ctx, streamID, 3)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	// Should get versions 3, 4, 5
	if len(retrieved) != 3 {
		t.Fatalf("Read(fromVersion=3) returned %d events, want 3", len(retrieved))
	}

	for i, event := range retrieved {
		expectedVersion := int64(i + 3)
		if event.Version != expectedVersion {
			t.Errorf("Event %d: Version = %d, want %d", i, event.Version, expectedVersion)
		}
	}
}

func TestReadEmptyStream(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	events, err := store.Read(ctx, "nonexistent-stream", 0)
	if err != nil {
		t.Fatalf("Read() error = %v, want nil", err)
	}

	if len(events) != 0 {
		t.Errorf("Read() returned %d events, want 0", len(events))
	}
}

func TestConcurrentAppendsToSameStream(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	streamID := "concurrent-stream"

	// Concurrent appends should serialize properly
	const numGoroutines = 10
	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	errChan := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(idx int) {
			defer wg.Done()

			event := Event{
				StreamID:  streamID,
				Type:      "ConcurrentEvent",
				Version:   int64(idx + 1),
				Data:      map[string]interface{}{"goroutine": float64(idx)},
				Metadata:  map[string]string{},
				Timestamp: time.Now().UTC(),
			}

			if err := store.Append(ctx, streamID, []Event{event}); err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	for err := range errChan {
		t.Errorf("Concurrent Append() error: %v", err)
	}

	// Verify all events were written
	events, err := store.Read(ctx, streamID, 0)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if len(events) != numGoroutines {
		t.Errorf("Read() returned %d events, want %d", len(events), numGoroutines)
	}
}

func TestConcurrentAppendsToDifferentStreams(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	const numStreams = 10
	var wg sync.WaitGroup
	wg.Add(numStreams)

	errChan := make(chan error, numStreams)

	for i := 0; i < numStreams; i++ {
		go func(idx int) {
			defer wg.Done()

			streamID := "stream-" + string(rune('A'+idx))
			events := []Event{
				{
					StreamID:  streamID,
					Type:      "Event1",
					Version:   1,
					Data:      map[string]interface{}{"stream": float64(idx)},
					Metadata:  map[string]string{},
					Timestamp: time.Now().UTC(),
				},
				{
					StreamID:  streamID,
					Type:      "Event2",
					Version:   2,
					Data:      map[string]interface{}{"stream": float64(idx)},
					Metadata:  map[string]string{},
					Timestamp: time.Now().UTC(),
				},
			}

			if err := store.Append(ctx, streamID, events); err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// Check for errors
	for err := range errChan {
		t.Errorf("Concurrent Append() error: %v", err)
	}

	// Verify each stream has 2 events
	for i := 0; i < numStreams; i++ {
		streamID := "stream-" + string(rune('A'+i))
		events, err := store.Read(ctx, streamID, 0)
		if err != nil {
			t.Errorf("Read(%s) error: %v", streamID, err)
			continue
		}
		if len(events) != 2 {
			t.Errorf("Stream %s has %d events, want 2", streamID, len(events))
		}
	}
}

func TestSnapshot(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	streamID := "snapshot-stream"

	// Create a snapshot
	state := map[string]interface{}{
		"counter": float64(100),
		"status":  "active",
		"items":   []interface{}{"a", "b", "c"},
		"nested": map[string]interface{}{
			"key": "value",
		},
	}

	err := store.Snapshot(ctx, streamID, 10, state)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	// Load the snapshot
	loadedState, version, err := store.LoadSnapshot(ctx, streamID)
	if err != nil {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}

	if version != 10 {
		t.Errorf("LoadSnapshot() version = %d, want 10", version)
	}

	// Convert to comparable format
	loadedMap, ok := loadedState.(map[string]interface{})
	if !ok {
		t.Fatalf("LoadSnapshot() returned type %T, want map[string]interface{}", loadedState)
	}

	if loadedMap["counter"] != float64(100) {
		t.Errorf("counter = %v, want 100", loadedMap["counter"])
	}
	if loadedMap["status"] != "active" {
		t.Errorf("status = %v, want active", loadedMap["status"])
	}
}

func TestSnapshotLatest(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	streamID := "multi-snapshot-stream"

	// Create multiple snapshots
	for i := 1; i <= 3; i++ {
		state := map[string]interface{}{
			"version": float64(i * 10),
		}
		err := store.Snapshot(ctx, streamID, int64(i*10), state)
		if err != nil {
			t.Fatalf("Snapshot(%d) error = %v", i, err)
		}
	}

	// Should load the latest snapshot (version 30)
	loadedState, version, err := store.LoadSnapshot(ctx, streamID)
	if err != nil {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}

	if version != 30 {
		t.Errorf("LoadSnapshot() version = %d, want 30", version)
	}

	loadedMap := loadedState.(map[string]interface{})
	if loadedMap["version"] != float64(30) {
		t.Errorf("version = %v, want 30", loadedMap["version"])
	}
}

func TestLoadSnapshotNotFound(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	_, _, err := store.LoadSnapshot(ctx, "nonexistent-stream")
	if err != sql.ErrNoRows {
		t.Errorf("LoadSnapshot() error = %v, want sql.ErrNoRows", err)
	}
}

func TestAppendWithInvalidJSON(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	streamID := "invalid-json-stream"

	// Create an event with data that can't be marshaled to JSON
	// Channels are not JSON-serializable
	event := Event{
		StreamID:  streamID,
		Type:      "InvalidEvent",
		Version:   1,
		Data:      map[string]interface{}{"channel": make(chan int)},
		Metadata:  map[string]string{},
		Timestamp: time.Now().UTC(),
	}

	err := store.Append(ctx, streamID, []Event{event})
	if err == nil {
		t.Error("Append() with invalid JSON data should return error")
	}
}

func TestSnapshotWithInvalidJSON(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	streamID := "invalid-snapshot"

	// Try to snapshot data that can't be marshaled to JSON
	state := map[string]interface{}{
		"channel": make(chan int),
	}

	err := store.Snapshot(ctx, streamID, 1, state)
	if err == nil {
		t.Error("Snapshot() with invalid JSON should return error")
	}
}

func TestSubscribe(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	streamID := "subscribe-stream"

	// Test that Subscribe returns a valid subscription
	// Note: The basic implementation returns a no-op subscription
	callCount := 0
	handler := func(ctx context.Context, event Event) error {
		callCount++
		return nil
	}

	sub, err := store.Subscribe(ctx, streamID, handler)
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	if sub == nil {
		t.Fatal("Subscribe() returned nil subscription")
	}

	// Test unsubscribe
	err = sub.Unsubscribe()
	if err != nil {
		t.Errorf("Unsubscribe() error = %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	streamID := "cancelled-stream"

	event := Event{
		StreamID:  streamID,
		Type:      "TestEvent",
		Version:   1,
		Data:      map[string]interface{}{"test": "data"},
		Metadata:  map[string]string{},
		Timestamp: time.Now().UTC(),
	}

	// Operations with cancelled context should fail
	err := store.Append(ctx, streamID, []Event{event})
	if err == nil {
		t.Error("Append() with cancelled context should return error")
	}
}

func TestTransactionalIntegrity(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	streamID := "transactional-stream"

	// First append should succeed
	event1 := Event{
		StreamID:  streamID,
		Type:      "Event1",
		Version:   1,
		Data:      map[string]interface{}{"valid": "data"},
		Metadata:  map[string]string{},
		Timestamp: time.Now().UTC(),
	}

	err := store.Append(ctx, streamID, []Event{event1})
	if err != nil {
		t.Fatalf("First Append() error = %v", err)
	}

	// Mixed batch with one invalid event should fail completely
	events := []Event{
		{
			StreamID:  streamID,
			Type:      "Event2",
			Version:   2,
			Data:      map[string]interface{}{"valid": "data"},
			Metadata:  map[string]string{},
			Timestamp: time.Now().UTC(),
		},
		{
			StreamID:  streamID,
			Type:      "Event3",
			Version:   3,
			Data:      map[string]interface{}{"invalid": make(chan int)}, // Invalid JSON
			Metadata:  map[string]string{},
			Timestamp: time.Now().UTC(),
		},
	}

	err = store.Append(ctx, streamID, events)
	if err == nil {
		t.Error("Append() with invalid event should fail")
	}

	// Verify only the first event exists (transaction rolled back)
	retrieved, err := store.Read(ctx, streamID, 0)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if len(retrieved) != 1 {
		t.Errorf("After failed transaction, stream has %d events, want 1", len(retrieved))
	}
}

func TestClose(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteEventStore() error = %v", err)
	}

	// Close should succeed
	err = store.Close()
	if err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// Operations after close should fail
	ctx := context.Background()
	event := Event{
		StreamID:  "test",
		Type:      "Test",
		Version:   1,
		Data:      map[string]interface{}{},
		Metadata:  map[string]string{},
		Timestamp: time.Now().UTC(),
	}

	err = store.Append(ctx, "test", []Event{event})
	if err == nil {
		t.Error("Append() after Close() should return error")
	}
}

func TestDataTypePersistence(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	streamID := "data-types-stream"

	// Test various data types
	event := Event{
		StreamID: streamID,
		Type:     "DataTypesEvent",
		Version:  1,
		Data: map[string]interface{}{
			"string": "hello",
			"int":    float64(42),
			"float":  3.14,
			"bool":   true,
			"null":   nil,
			"array":  []interface{}{1.0, 2.0, 3.0},
			"object": map[string]interface{}{"nested": "value"},
		},
		Metadata: map[string]string{
			"key1": "value1",
			"key2": "value2",
		},
		Timestamp: time.Now().UTC(),
	}

	err := store.Append(ctx, streamID, []Event{event})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	// Read and verify
	events, err := store.Read(ctx, streamID, 0)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("Read() returned %d events, want 1", len(events))
	}

	got := events[0]

	// Check each data type
	if got.Data["string"] != "hello" {
		t.Errorf("string = %v, want hello", got.Data["string"])
	}
	if got.Data["int"] != float64(42) {
		t.Errorf("int = %v, want 42", got.Data["int"])
	}
	if got.Data["bool"] != true {
		t.Errorf("bool = %v, want true", got.Data["bool"])
	}
	if got.Data["null"] != nil {
		t.Errorf("null = %v, want nil", got.Data["null"])
	}

	// Verify array
	arr, ok := got.Data["array"].([]interface{})
	if !ok || len(arr) != 3 {
		t.Errorf("array = %v, want [1,2,3]", got.Data["array"])
	}

	// Verify nested object
	obj, ok := got.Data["object"].(map[string]interface{})
	if !ok || obj["nested"] != "value" {
		t.Errorf("object = %v, want {nested: value}", got.Data["object"])
	}

	// Verify metadata
	if got.Metadata["key1"] != "value1" || got.Metadata["key2"] != "value2" {
		t.Errorf("metadata = %v, want {key1: value1, key2: value2}", got.Metadata)
	}
}

func TestEmptyEventsArray(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()
	streamID := "empty-stream"

	// Appending empty array should be a no-op
	err := store.Append(ctx, streamID, []Event{})
	if err != nil {
		t.Errorf("Append() with empty events should not error, got: %v", err)
	}

	// Stream should still be empty
	events, err := store.Read(ctx, streamID, 0)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	if len(events) != 0 {
		t.Errorf("Read() returned %d events, want 0", len(events))
	}
}

func TestDB(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	db := store.DB()
	if db == nil {
		t.Error("DB() returned nil")
	}

	// Verify we can query the database
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM events").Scan(&count)
	if err != nil {
		t.Errorf("DB() returned non-functional database: %v", err)
	}
}

// Benchmark tests
func BenchmarkAppendSingleEvent(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")
	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	streamID := "bench-stream"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		event := Event{
			StreamID:  streamID,
			Type:      "BenchEvent",
			Version:   int64(i + 1),
			Data:      map[string]interface{}{"index": float64(i)},
			Metadata:  map[string]string{"bench": "true"},
			Timestamp: time.Now().UTC(),
		}
		if err := store.Append(ctx, streamID, []Event{event}); err != nil {
			b.Fatalf("Append() error: %v", err)
		}
	}
}

func BenchmarkReadEvents(b *testing.B) {
	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench.db")
	store, err := NewSQLiteEventStore(dbPath)
	if err != nil {
		b.Fatalf("failed to create store: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	streamID := "bench-stream"

	// Pre-populate with 1000 events
	events := make([]Event, 1000)
	for i := 0; i < 1000; i++ {
		events[i] = Event{
			StreamID:  streamID,
			Type:      "BenchEvent",
			Version:   int64(i + 1),
			Data:      map[string]interface{}{"index": float64(i)},
			Metadata:  map[string]string{},
			Timestamp: time.Now().UTC(),
		}
	}
	if err := store.Append(ctx, streamID, events); err != nil {
		b.Fatalf("failed to populate events: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := store.Read(ctx, streamID, 0)
		if err != nil {
			b.Fatalf("Read() error: %v", err)
		}
	}
}
