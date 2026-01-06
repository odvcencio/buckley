package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

func newTestRunner(t *testing.T, root string) (*Runner, *storage.Store, *telemetry.Hub, func()) {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "store.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	hub := telemetry.NewHub()
	runner := NewRunner(store, root, hub)
	cleanup := func() {
		_ = store.Close()
	}
	return runner, store, hub, cleanup
}

func TestNewRunner(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "store.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	hub := telemetry.NewHub()
	root := "/test/root"

	runner := NewRunner(store, root, hub)
	if runner == nil {
		t.Fatal("expected non-nil runner")
	}
	if runner.store != store {
		t.Error("runner store not set correctly")
	}
	if runner.root != root {
		t.Errorf("expected root %s, got %s", root, runner.root)
	}
	if runner.telemetry != hub {
		t.Error("runner telemetry not set correctly")
	}
	if runner.running {
		t.Error("expected running to be false initially")
	}
}

func TestRunner_Rebuild_Success(t *testing.T) {
	tempDir := t.TempDir()

	// Create a test Go file
	testFile := filepath.Join(tempDir, "test.go")
	if err := os.WriteFile(testFile, []byte("package test\n\nfunc Foo() {}"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	runner, store, hub, cleanup := newTestRunner(t, tempDir)
	defer cleanup()

	// Subscribe to telemetry events
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	ctx := context.Background()
	if err := runner.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild failed: %v", err)
	}

	// Verify the file was indexed
	files, err := store.SearchFiles(ctx, "", "test.go", 10)
	if err != nil {
		t.Fatalf("SearchFiles failed: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != "test.go" {
		t.Errorf("expected path test.go, got %s", files[0].Path)
	}

	// Verify telemetry events were published
	timeout := time.After(1 * time.Second)
	var startedEvent, completedEvent bool
	for !startedEvent || !completedEvent {
		select {
		case event := <-events:
			switch event.Type {
			case telemetry.EventIndexStarted:
				startedEvent = true
				if event.Data["root"] == nil {
					t.Error("EventIndexStarted missing root")
				}
			case telemetry.EventIndexCompleted:
				completedEvent = true
				if event.Data["root"] == nil {
					t.Error("EventIndexCompleted missing root")
				}
				if event.Data["duration_ms"] == nil {
					t.Error("EventIndexCompleted missing duration_ms")
				}
			}
		case <-timeout:
			if !startedEvent {
				t.Error("did not receive EventIndexStarted")
			}
			if !completedEvent {
				t.Error("did not receive EventIndexCompleted")
			}
			return
		}
	}
}

func TestRunner_Rebuild_EmptyRoot(t *testing.T) {
	// Don't provide a root, should default to current working directory
	runner, _, _, cleanup := newTestRunner(t, "")
	defer cleanup()

	ctx := context.Background()
	// Should not fail even with empty root
	if err := runner.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild failed: %v", err)
	}
}

func TestRunner_Rebuild_ConcurrentCalls(t *testing.T) {
	tempDir := t.TempDir()

	// Create multiple files to make the rebuild take longer
	for i := 0; i < 20; i++ {
		fileName := "file" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + ".go"
		testFile := filepath.Join(tempDir, fileName)
		content := fmt.Sprintf("package test\n\nfunc Foo%d() {}\n", i)
		if err := os.WriteFile(testFile, []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}
	}

	runner, _, _, cleanup := newTestRunner(t, tempDir)
	defer cleanup()

	ctx := context.Background()

	// Start first rebuild
	errChan1 := make(chan error, 1)
	go func() {
		errChan1 <- runner.Rebuild(ctx)
	}()

	// Give first rebuild time to start
	time.Sleep(50 * time.Millisecond)

	// Try to start second rebuild while first is running
	err := runner.Rebuild(ctx)
	if err == nil {
		// First rebuild may have completed quickly, skip the test
		t.Skip("first rebuild completed before second started")
	}
	if err.Error() != "index rebuild already in progress" {
		t.Errorf("unexpected error message: %v", err)
	}

	// Wait for first rebuild to complete
	if err := <-errChan1; err != nil {
		t.Fatalf("first rebuild failed: %v", err)
	}

	// Should be able to rebuild again after first completes
	if err := runner.Rebuild(ctx); err != nil {
		t.Fatalf("second rebuild failed: %v", err)
	}
}

func TestRunner_Rebuild_ContextCanceled(t *testing.T) {
	tempDir := t.TempDir()

	// Create multiple files to increase chance of cancellation
	for i := 0; i < 10; i++ {
		testFile := filepath.Join(tempDir, "test"+string(rune('0'+i))+".go")
		if err := os.WriteFile(testFile, []byte("package test\n\nfunc Foo() {}"), 0o644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}
	}

	runner, _, hub, cleanup := newTestRunner(t, tempDir)
	defer cleanup()

	// Subscribe to telemetry events
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	ctx, cancel := context.WithCancel(context.Background())

	// Start rebuild in background
	errChan := make(chan error, 1)
	go func() {
		errChan <- runner.Rebuild(ctx)
	}()

	// Cancel context quickly
	time.Sleep(1 * time.Millisecond)
	cancel()

	err := <-errChan
	if err == nil {
		// Sometimes the rebuild completes before cancellation
		t.Skip("rebuild completed before cancellation")
	}

	// Should receive a failed event
	timeout := time.After(1 * time.Second)
	select {
	case event := <-events:
		if event.Type == telemetry.EventIndexFailed {
			if event.Data["error"] == nil {
				t.Error("EventIndexFailed missing error")
			}
		}
	case <-timeout:
		// May not receive event if cancellation was very fast
	}
}

func TestRunner_StartBackground(t *testing.T) {
	tempDir := t.TempDir()

	// Create a test file
	testFile := filepath.Join(tempDir, "test.go")
	if err := os.WriteFile(testFile, []byte("package test"), 0o644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	runner, store, _, cleanup := newTestRunner(t, tempDir)
	defer cleanup()

	ctx := context.Background()
	runner.StartBackground(ctx)

	// Wait for background rebuild to complete
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		files, err := store.SearchFiles(ctx, "", "test.go", 10)
		if err != nil {
			t.Fatalf("SearchFiles failed: %v", err)
		}
		if len(files) == 1 {
			// File was indexed successfully
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("background rebuild did not complete in time")
}

func TestRunner_PublishEvent_NilTelemetry(t *testing.T) {
	// Create runner with nil telemetry
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "store.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	runner := NewRunner(store, tempDir, nil)

	// Should not panic when telemetry is nil
	runner.publishEvent(telemetry.EventIndexStarted, map[string]any{"test": "data"})
}

func TestRunner_PublishEvent_NilRunner(t *testing.T) {
	var runner *Runner
	// Should not panic with nil runner
	runner.publishEvent(telemetry.EventIndexStarted, map[string]any{"test": "data"})
}

func TestRunner_Rebuild_TelemetryFailure(t *testing.T) {
	tempDir := t.TempDir()

	// Create an invalid directory structure that will cause indexing to fail
	invalidDir := filepath.Join(tempDir, "invalid")
	if err := os.MkdirAll(invalidDir, 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	// Create a file with invalid Go syntax
	invalidFile := filepath.Join(tempDir, "invalid.go")
	if err := os.WriteFile(invalidFile, []byte("package invalid\n\nfunc {"), 0o644); err != nil {
		t.Fatalf("failed to write invalid file: %v", err)
	}

	runner, _, hub, cleanup := newTestRunner(t, tempDir)
	defer cleanup()

	// Subscribe to telemetry events
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	ctx := context.Background()
	err := runner.Rebuild(ctx)
	if err == nil {
		t.Fatal("expected error with invalid Go file")
	}

	// Verify failure event was published
	timeout := time.After(1 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Type == telemetry.EventIndexFailed {
				if event.Data["error"] == nil {
					t.Error("EventIndexFailed missing error")
				}
				if event.Data["root"] == nil {
					t.Error("EventIndexFailed missing root")
				}
				if event.Data["duration_ms"] == nil {
					t.Error("EventIndexFailed missing duration_ms")
				}
				return
			}
		case <-timeout:
			t.Fatal("did not receive EventIndexFailed")
		}
	}
}

func TestRunner_Rebuild_NonExistentRoot(t *testing.T) {
	nonExistent := filepath.Join(t.TempDir(), "does-not-exist")
	runner, _, _, cleanup := newTestRunner(t, nonExistent)
	defer cleanup()

	ctx := context.Background()
	err := runner.Rebuild(ctx)
	if err == nil {
		t.Fatal("expected error with non-existent root")
	}
}
