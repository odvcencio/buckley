// pkg/ralph/control_watcher_test.go
package ralph

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewControlWatcher(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	watcher := NewControlWatcher(path, 100*time.Millisecond)
	if watcher == nil {
		t.Fatal("expected non-nil watcher")
	}
}

func TestControlWatcher_Start_NoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.yaml")

	watcher := NewControlWatcher(path, 100*time.Millisecond)
	err := watcher.Start()
	if err == nil {
		t.Error("expected error when starting watcher with nonexistent file")
	}
}

func TestControlWatcher_Start_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	content := `
backends:
  test:
    command: "test"
    enabled: true
mode: sequential
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	watcher := NewControlWatcher(path, 100*time.Millisecond)
	if err := watcher.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer watcher.Stop()

	cfg := watcher.Config()
	if cfg == nil {
		t.Fatal("expected non-nil config after start")
	}
	if cfg.Mode != "sequential" {
		t.Errorf("expected mode 'sequential', got %q", cfg.Mode)
	}
}

func TestControlWatcher_Stop_BeforeStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	watcher := NewControlWatcher(path, 100*time.Millisecond)
	// Should not panic
	watcher.Stop()
}

func TestControlWatcher_Stop_AfterStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	content := `
backends:
  test:
    command: "test"
    enabled: true
mode: sequential
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	watcher := NewControlWatcher(path, 100*time.Millisecond)
	if err := watcher.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Stop should not panic and should be idempotent
	watcher.Stop()
	watcher.Stop()
}

func TestControlWatcher_Config_BeforeStart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	watcher := NewControlWatcher(path, 100*time.Millisecond)
	cfg := watcher.Config()
	if cfg != nil {
		t.Error("expected nil config before start")
	}
}

func TestControlWatcher_Subscribe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	content := `
backends:
  test:
    command: "test"
    enabled: true
mode: sequential
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	watcher := NewControlWatcher(path, 50*time.Millisecond)
	if err := watcher.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer watcher.Stop()

	ch := watcher.Subscribe()
	if ch == nil {
		t.Fatal("expected non-nil channel from Subscribe")
	}
}

func TestControlWatcher_DetectsFileChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	content := `
backends:
  test:
    command: "test"
    enabled: true
mode: sequential
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	watcher := NewControlWatcher(path, 50*time.Millisecond)
	if err := watcher.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer watcher.Stop()

	ch := watcher.Subscribe()

	// Give the watcher time to complete at least one poll cycle
	time.Sleep(100 * time.Millisecond)

	// Update the file with atomic write pattern
	newContent := `
backends:
  test:
    command: "test"
    enabled: true
mode: parallel
`
	// Write to temp file first, then rename for atomicity
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(newContent), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		t.Fatalf("failed to rename temp file: %v", err)
	}

	// Wait for notification with timeout
	select {
	case cfg := <-ch:
		if cfg == nil {
			t.Error("expected non-nil config from channel")
		} else if cfg.Mode != "parallel" {
			t.Errorf("expected mode 'parallel', got %q", cfg.Mode)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("timeout waiting for file change notification")
	}
}

func TestControlWatcher_MultipleSubscribers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	content := `
backends:
  test:
    command: "test"
    enabled: true
mode: sequential
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	watcher := NewControlWatcher(path, 50*time.Millisecond)
	if err := watcher.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer watcher.Stop()

	ch1 := watcher.Subscribe()
	ch2 := watcher.Subscribe()
	ch3 := watcher.Subscribe()

	// Give the watcher time to start
	time.Sleep(100 * time.Millisecond)

	// Update the file with atomic write pattern
	newContent := `
backends:
  test:
    command: "test"
    enabled: true
mode: round_robin
`
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(newContent), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		t.Fatalf("failed to rename temp file: %v", err)
	}

	// All subscribers should receive notification
	var wg sync.WaitGroup
	var receivedCount int
	var mu sync.Mutex

	checkChannel := func(ch <-chan *ControlConfig) {
		defer wg.Done()
		select {
		case cfg := <-ch:
			if cfg != nil && cfg.Mode == "round_robin" {
				mu.Lock()
				receivedCount++
				mu.Unlock()
			}
		case <-time.After(500 * time.Millisecond):
		}
	}

	wg.Add(3)
	go checkChannel(ch1)
	go checkChannel(ch2)
	go checkChannel(ch3)
	wg.Wait()

	if receivedCount != 3 {
		t.Errorf("expected 3 subscribers to receive notification, got %d", receivedCount)
	}
}

func TestControlWatcher_IgnoresInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	content := `
backends:
  test:
    command: "test"
    enabled: true
mode: sequential
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	watcher := NewControlWatcher(path, 50*time.Millisecond)
	if err := watcher.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer watcher.Stop()

	// Verify initial config
	initialCfg := watcher.Config()
	if initialCfg.Mode != "sequential" {
		t.Errorf("expected initial mode 'sequential', got %q", initialCfg.Mode)
	}

	// Write invalid YAML
	if err := os.WriteFile(path, []byte("invalid: yaml: [broken"), 0644); err != nil {
		t.Fatalf("failed to write invalid YAML: %v", err)
	}

	// Wait for a poll cycle
	time.Sleep(100 * time.Millisecond)

	// Config should remain unchanged (invalid YAML is ignored)
	currentCfg := watcher.Config()
	if currentCfg.Mode != "sequential" {
		t.Errorf("expected mode to remain 'sequential' after invalid YAML, got %q", currentCfg.Mode)
	}
}

func TestControlWatcher_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	content := `
backends:
  test:
    command: "test"
    enabled: true
mode: sequential
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	watcher := NewControlWatcher(path, 50*time.Millisecond)
	if err := watcher.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer watcher.Stop()

	// Concurrent reads and subscribes
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				watcher.Config()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				watcher.Subscribe()
			}
		}()
	}
	wg.Wait()
}

func TestControlWatcher_NilGuards(t *testing.T) {
	var watcher *ControlWatcher

	// These should not panic
	watcher.Stop()

	cfg := watcher.Config()
	if cfg != nil {
		t.Error("expected nil config from nil watcher")
	}

	ch := watcher.Subscribe()
	if ch != nil {
		t.Error("expected nil channel from nil watcher")
	}
}

func TestControlWatcher_Unsubscribe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	content := `
backends:
  test:
    command: "test"
    enabled: true
mode: sequential
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	watcher := NewControlWatcher(path, 50*time.Millisecond)
	if err := watcher.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer watcher.Stop()

	ch := watcher.Subscribe()

	// Unsubscribe
	watcher.Unsubscribe(ch)

	// Give time for unsubscribe to process
	time.Sleep(100 * time.Millisecond)

	// Update the file with atomic write pattern
	newContent := `
backends:
  test:
    command: "test"
    enabled: true
mode: parallel
`
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(newContent), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		t.Fatalf("failed to rename temp file: %v", err)
	}

	// Unsubscribed channel should not receive notification
	select {
	case <-ch:
		t.Error("unsubscribed channel should not receive notification")
	case <-time.After(200 * time.Millisecond):
		// Expected: no notification
	}
}

func TestControlWatcher_FileDeleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	content := `
backends:
  test:
    command: "test"
    enabled: true
mode: sequential
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	watcher := NewControlWatcher(path, 50*time.Millisecond)
	if err := watcher.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer watcher.Stop()

	// Verify initial config
	initialCfg := watcher.Config()
	if initialCfg.Mode != "sequential" {
		t.Errorf("expected initial mode 'sequential', got %q", initialCfg.Mode)
	}

	// Delete the file
	if err := os.Remove(path); err != nil {
		t.Fatalf("failed to delete file: %v", err)
	}

	// Wait for a poll cycle
	time.Sleep(100 * time.Millisecond)

	// Config should remain unchanged (deletion is treated like invalid file)
	currentCfg := watcher.Config()
	if currentCfg.Mode != "sequential" {
		t.Errorf("expected mode to remain 'sequential' after deletion, got %q", currentCfg.Mode)
	}
}

func TestControlWatcher_FileRecreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ralph-control.yaml")

	content := `
backends:
  test:
    command: "test"
    enabled: true
mode: sequential
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	watcher := NewControlWatcher(path, 50*time.Millisecond)
	if err := watcher.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer watcher.Stop()

	ch := watcher.Subscribe()

	// Give the watcher time to start
	time.Sleep(100 * time.Millisecond)

	// Delete the file
	if err := os.Remove(path); err != nil {
		t.Fatalf("failed to delete file: %v", err)
	}

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	// Recreate with new content using atomic write
	newContent := `
backends:
  test:
    command: "test"
    enabled: true
mode: parallel
`
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(newContent), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		t.Fatalf("failed to rename temp file: %v", err)
	}

	// Should receive notification for new file
	select {
	case cfg := <-ch:
		if cfg == nil {
			t.Error("expected non-nil config from channel")
		} else if cfg.Mode != "parallel" {
			t.Errorf("expected mode 'parallel', got %q", cfg.Mode)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("timeout waiting for file recreation notification")
	}
}
