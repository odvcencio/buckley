package tool

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/ui/progress"
	"github.com/odvcencio/buckley/pkg/ui/toast"
)

type failingTool struct{}

func (failingTool) Name() string { return "run_shell" }

func (failingTool) Description() string { return "fails" }

func (failingTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (failingTool) Execute(params map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: false, Error: "boom"}, os.ErrInvalid
}

func TestDefaultMiddlewareStack_ProgressAndToast(t *testing.T) {
	progressSeen := false
	progressMgr := progress.NewProgressManager()
	progressMgr.SetOnChange(func(items []progress.Progress) {
		if len(items) > 0 {
			progressSeen = true
		}
	})

	toastSeen := false
	toastMgr := toast.NewToastManager()
	toastMgr.SetOnChange(func(items []*toast.Toast) {
		if len(items) > 0 {
			toastSeen = true
		}
	})

	registry := NewEmptyRegistry()
	registry.Register(failingTool{})

	cfg := DefaultRegistryConfig()
	cfg.MaxOutputBytes = 0
	cfg.Middleware.DefaultTimeout = 0
	cfg.Middleware.MaxResultBytes = 0
	cfg.Middleware.ProgressManager = progressMgr
	cfg.Middleware.ToastManager = toastMgr
	ApplyRegistryConfig(registry, cfg)

	if _, err := registry.Execute("run_shell", map[string]any{"command": "noop"}); err == nil {
		t.Fatal("expected error")
	}
	if !progressSeen {
		t.Fatal("expected progress events")
	}
	if !toastSeen {
		t.Fatal("expected toast notification")
	}
}

func TestDefaultMiddlewareStack_ApprovalAndTelemetry(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "note.txt")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "mission.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	missionStore := mission.NewStore(store.DB())
	session := &storage.Session{
		ID:         "session-1",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		Status:     storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	hub := telemetry.NewHub()
	eventCh, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)

	registry := NewEmptyRegistry()
	registry.Register(&builtin.WriteFileTool{})

	cfg := DefaultRegistryConfig()
	cfg.MaxOutputBytes = 0
	cfg.TelemetryHub = hub
	cfg.TelemetrySessionID = session.ID
	cfg.MissionStore = missionStore
	cfg.MissionAgentID = "agent-1"
	cfg.MissionSessionID = session.ID
	cfg.MissionTimeout = 2 * time.Second
	cfg.RequireMissionApproval = true
	cfg.Middleware.DefaultTimeout = 0
	cfg.Middleware.MaxResultBytes = 0
	ApplyRegistryConfig(registry, cfg)

	done := make(chan struct{})
	var execErr error
	go func() {
		_, execErr = registry.Execute("write_file", map[string]any{
			"path":    target,
			"content": "new",
		})
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("expected execution to wait for approval")
	case <-time.After(25 * time.Millisecond):
	}

	changeID := waitForPendingChangeID(t, store.DB())
	if err := missionStore.UpdatePendingChangeStatus(changeID, "approved", "tester"); err != nil {
		t.Fatalf("approve change: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("execution timed out waiting for approval")
	}

	if execErr != nil {
		t.Fatalf("unexpected execute error: %v", execErr)
	}

	expectTelemetryEvents(t, eventCh, telemetry.EventToolStarted, telemetry.EventToolCompleted)
}

func waitForPendingChangeID(t *testing.T, db *sql.DB) string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	t.Cleanup(ticker.Stop)

	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for pending change")
		case <-ticker.C:
			var id string
			err := db.QueryRow(`SELECT id FROM pending_changes ORDER BY created_at DESC LIMIT 1`).Scan(&id)
			if err == nil && id != "" {
				return id
			}
		}
	}
}

func expectTelemetryEvents(t *testing.T, ch <-chan telemetry.Event, want ...telemetry.EventType) {
	t.Helper()
	needed := make(map[telemetry.EventType]struct{}, len(want))
	for _, evt := range want {
		needed[evt] = struct{}{}
	}

	deadline := time.After(2 * time.Second)
	for len(needed) > 0 {
		select {
		case event := <-ch:
			delete(needed, event.Type)
		case <-deadline:
			t.Fatalf("timed out waiting for telemetry events: %#v", needed)
		}
	}
}
