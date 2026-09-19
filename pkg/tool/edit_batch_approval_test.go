package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/mission"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestApprovalMiddlewareEditBatch(t *testing.T) {
	for _, decision := range []string{"approved", "rejected"} {
		t.Run(decision, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "note.txt")
			if err := os.WriteFile(path, []byte("alpha beta"), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := storage.New(filepath.Join(dir, "mission.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if err := store.CreateSession(&storage.Session{ID: "batch", CreatedAt: time.Now(), LastActive: time.Now(), Status: storage.SessionStatusActive}); err != nil {
				t.Fatal(err)
			}
			missionStore := mission.NewStore(store.DB())
			registry := NewEmptyRegistry()
			registry.Register(&builtin.EditFileTool{})
			registry.EnableMissionControl(missionStore, "agent", true, 2*time.Second)
			registry.UpdateMissionSession("batch")
			done := make(chan struct{})
			var result *builtin.Result
			var execErr error
			go func() {
				result, execErr = registry.Execute("edit_file", map[string]any{"path": path, "edits": []any{
					map[string]any{"old_string": "alpha", "new_string": "FIRST"},
					map[string]any{"old_string": "beta", "new_string": "SECOND"},
				}})
				close(done)
			}()
			id := waitForPendingChange(t, store.DB())
			var diff string
			if err := store.DB().QueryRow(`SELECT diff FROM pending_changes WHERE id = ?`, id).Scan(&diff); err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{"alpha", "beta", "FIRST", "SECOND"} {
				if !strings.Contains(diff, value) {
					t.Fatalf("approval omits %q: %s", value, diff)
				}
			}
			content, err := os.ReadFile(path)
			if err != nil || string(content) != "alpha beta" {
				t.Fatalf("changed before approval: %q %v", content, err)
			}
			if err := missionStore.UpdatePendingChangeStatus(id, decision, "tester"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("approval did not settle")
			}
			if execErr != nil || result == nil || result.Success != (decision == "approved") {
				t.Fatalf("result=%+v err=%v", result, execErr)
			}
			want := "alpha beta"
			if decision == "approved" {
				want = "FIRST SECOND"
			}
			content, err = os.ReadFile(path)
			if err != nil || string(content) != want {
				t.Fatalf("disk=%q err=%v want=%q", content, err, want)
			}
		})
	}
}
