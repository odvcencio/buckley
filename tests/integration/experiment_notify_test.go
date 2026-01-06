//go:build integration
// +build integration

package integration

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/experiment"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/notify"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/worktree"
)

// mockNotifyAdapter captures notifications for testing
type mockNotifyAdapter struct {
	mu       sync.Mutex
	events   []capturedEvent
	name     string
}

type capturedEvent struct {
	Type    string
	TaskID  string
	Title   string
	Message string
}

func newMockNotifyAdapter(name string) *mockNotifyAdapter {
	return &mockNotifyAdapter{
		name:   name,
		events: make([]capturedEvent, 0),
	}
}

func (m *mockNotifyAdapter) Name() string {
	return m.name
}

func (m *mockNotifyAdapter) Send(ctx context.Context, event *notify.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, capturedEvent{
		Type:    string(event.Type),
		TaskID:  event.TaskID,
		Title:   event.Title,
		Message: event.Message,
	})
	return nil
}

func (m *mockNotifyAdapter) ReceiveResponses(ctx context.Context) (<-chan *notify.Response, error) {
	ch := make(chan *notify.Response)
	close(ch)
	return ch, nil
}

func (m *mockNotifyAdapter) Close() error {
	return nil
}

func (m *mockNotifyAdapter) Events() []capturedEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]capturedEvent, len(m.events))
	copy(result, m.events)
	return result
}

// mockWorktreeManager for testing
type mockWorktreeManager struct {
	tempDir string
}

func (m *mockWorktreeManager) Create(branch string) (*worktree.Worktree, error) {
	return &worktree.Worktree{
		Branch: branch,
		Path:   filepath.Join(m.tempDir, branch),
	}, nil
}

func (m *mockWorktreeManager) Remove(branch string, force bool) error {
	return nil
}

// TestExperimentNotifyIntegration verifies that the notification system
// is properly wired into the experiment runner
func TestExperimentNotifyIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temp directory
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	// Setup storage
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	// Setup mock notify adapter
	mockAdapter := newMockNotifyAdapter("test-adapter")
	notifyMgr := notify.NewManager(nil, mockAdapter)

	// Setup minimal config
	cfg := &config.Config{
		Experiment: config.ExperimentConfig{
			Enabled:        true,
			MaxConcurrent:  2,
			DefaultTimeout: 30 * time.Second,
		},
	}

	// Setup model manager (minimal)
	mgr := &model.Manager{}

	// Setup mock worktree manager
	wtMgr := &mockWorktreeManager{tempDir: tempDir}

	// Create experiment store
	expStore := experiment.NewStoreFromStorage(store)

	// Verify runner can be created with notify manager
	runner, err := experiment.NewRunner(
		experiment.RunnerConfig{
			MaxConcurrent:  2,
			DefaultTimeout: 30 * time.Second,
		},
		experiment.Dependencies{
			Config:       cfg,
			ModelManager: mgr,
			Notify:       notifyMgr,
			Worktree:     wtMgr,
			Store:        expStore,
		},
	)
	if err != nil {
		t.Fatalf("Failed to create runner: %v", err)
	}

	if runner == nil {
		t.Fatal("Runner is nil")
	}

	t.Log("Runner created successfully with notify manager")
}

// TestNotifyManagerNilSafety verifies that nil notify manager doesn't cause panics
func TestNotifyManagerNilSafety(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	cfg := &config.Config{
		Experiment: config.ExperimentConfig{
			Enabled:        true,
			MaxConcurrent:  1,
			DefaultTimeout: 5 * time.Second,
		},
	}

	mgr := &model.Manager{}
	wtMgr := &mockWorktreeManager{tempDir: tempDir}
	expStore := experiment.NewStoreFromStorage(store)

	// Create runner WITHOUT notify manager (nil)
	runner, err := experiment.NewRunner(
		experiment.RunnerConfig{
			MaxConcurrent:  1,
			DefaultTimeout: 5 * time.Second,
		},
		experiment.Dependencies{
			Config:       cfg,
			ModelManager: mgr,
			Notify:       nil, // Explicitly nil
			Worktree:     wtMgr,
			Store:        expStore,
		},
	)
	if err != nil {
		t.Fatalf("Failed to create runner: %v", err)
	}

	if runner == nil {
		t.Fatal("Runner is nil")
	}

	t.Log("Runner created successfully with nil notify manager")
}
