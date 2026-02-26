package tool

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func TestApprovalMiddlewareWriteFile(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "note.txt")
	if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
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

	registry := NewEmptyRegistry()
	registry.Register(&builtin.WriteFileTool{})
	registry.EnableMissionControl(missionStore, "agent-1", true, 2*time.Second)
	registry.UpdateMissionSession("session-1")
	if !registry.shouldGateChanges() {
		t.Fatal("expected approval gate to be enabled")
	}

	done := make(chan struct{})
	var result *builtin.Result
	var execErr error
	go func() {
		result, execErr = registry.Execute("write_file", map[string]any{
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

	changeID := waitForPendingChange(t, store.DB())
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
	if result == nil || !result.Success {
		t.Fatalf("expected success result, got %#v", result)
	}
	contents, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if string(contents) != "new" {
		t.Errorf("unexpected content: %s", string(contents))
	}
}

func waitForPendingChange(t *testing.T, db *sql.DB) string {
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
