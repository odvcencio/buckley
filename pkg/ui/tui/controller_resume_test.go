package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/storage"
)

func newResumeLookupTestController(t *testing.T) (*Controller, *storage.Store) {
	t.Helper()
	cfg, store, workDir := newControllerSessionTestConfig(t)
	cfg.SessionID = "current"
	now := time.Now()
	for i, id := range []string{"saved", "paused", "1", "0", "700", "-1", "999999999999999999999999", "current", "open"} {
		status := storage.SessionStatusCompleted
		if id == "current" || id == "open" {
			status = storage.SessionStatusActive
		} else if id == "paused" {
			status = storage.SessionStatusPaused
		}
		createControllerTestSession(t, store, id, workDir, status, now.Add(-time.Duration(i+1)*time.Minute))
	}
	if err := store.CreateSession(&storage.Session{ID: "other-project", ProjectPath: filepath.Join(workDir, "other"), GitRepo: workDir, CreatedAt: now, LastActive: now, Status: storage.SessionStatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(&storage.Message{SessionID: "saved", Role: "assistant", Content: "Completed work survives resume.", Timestamp: now}); err != nil {
		t.Fatal(err)
	}
	sessions, selected, err := loadOrCreateControllerSessions(cfg, workDir)
	if err != nil {
		t.Fatal(err)
	}
	ctrl, _ := newSessionNavTestController(t, nil)
	ctrl.cfg, ctrl.store, ctrl.workDir = cfg.Config, store, workDir
	ctrl.sessions, ctrl.currentSession = sessions, selected
	ctrl.conversation, ctrl.registry = sessions[selected].Conversation, sessions[selected].ToolRegistry
	t.Cleanup(func() {
		ctrl.Stop()
		for _, sess := range ctrl.sessions {
			if sess.HookCloser != nil {
				_ = sess.HookCloser.Close()
			}
		}
	})
	return ctrl, store
}

func TestResumeSession_ResolvesIDsAndNumbersWithinProject(t *testing.T) {
	for _, tt := range []struct {
		reference string
		want      string
	}{
		{"saved", "saved"},
		{"  saved \n", "saved"},
		{"paused", "paused"},
		{"open", "open"},
		{"1", "saved"},
		{"2", "paused"},
		{"+2", "paused"},
		{"0001", "saved"},
		{"3", "1"},
		{"0", "0"},
		{"700", "700"},
		{"-1", "-1"},
		{"999999999999999999999999", "999999999999999999999999"},
		{"other-project", ""},
		{"99998", ""},
		{"save", ""},
		{"missing", ""},
		{"", ""},
	} {
		t.Run(tt.reference, func(t *testing.T) {
			ctrl, store := newResumeLookupTestController(t)
			before := ctrl.sessions[ctrl.currentSession]
			var existing *SessionState
			for _, sess := range ctrl.sessions {
				if sess.ID == tt.want {
					existing = sess
				}
			}
			ctrl.resumeSession(tt.reference)
			selected := ctrl.sessions[ctrl.currentSession]
			if tt.want == "" {
				if selected != before || len(ctrl.sessions) != 2 {
					t.Fatal("missing or out-of-project reference changed the selected tabs")
				}
			} else {
				if selected.ID != tt.want || selected.ToolRegistry == nil || selected.runtimeLoader != nil {
					t.Fatalf("selected session=%s, want initialized %s", selected.ID, tt.want)
				}
				if existing != nil && selected != existing {
					t.Fatal("reopening a loaded tab replaced its session state")
				}
				saved, err := store.GetSession(tt.want)
				if err != nil || saved == nil || saved.Status != storage.SessionStatusActive {
					t.Fatalf("resumed session was not activated: %v, %v", saved, err)
				}
				if tt.want == "saved" && (len(selected.Conversation.Messages) != 1 || selected.Conversation.Messages[0].Content != "Completed work survives resume.") {
					t.Fatal("resume lost completed conversation work")
				}
			}
			other, err := store.GetSession("other-project")
			if err != nil || other.Status != storage.SessionStatusCompleted {
				t.Fatal("resuming another reference changed the out-of-project session")
			}
		})
	}
}

func TestResumeSession_FullIDIgnoresUnrelatedMalformedMetadata(t *testing.T) {
	ctrl, store := newResumeLookupTestController(t)
	if _, err := store.DB().Exec("UPDATE sessions SET created_at = 'not-a-timestamp' WHERE session_id = '700'"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListSessionsByRepo(ctrl.workDir); err == nil {
		t.Fatal("fixture did not make full session enumeration fail")
	}
	ctrl.resumeSession("saved")
	if ctrl.sessions[ctrl.currentSession].ID != "saved" {
		t.Fatal("valid full-ID resume scanned unrelated malformed session metadata")
	}
}

func TestResumeSession_ReportsLookupFailureWithoutChangingTabs(t *testing.T) {
	ctrl, store := newResumeLookupTestController(t)
	before := ctrl.sessions[ctrl.currentSession]
	if err := store.DB().Close(); err != nil {
		t.Fatal(err)
	}
	ctrl.resumeSession("saved")
	if ctrl.sessions[ctrl.currentSession] != before || len(ctrl.sessions) != 2 {
		t.Fatal("failed lookup changed session selection")
	}
	for _, msg := range drainAllMessages(ctrl.app) {
		if added, ok := msg.(AddMessageMsg); ok && strings.HasPrefix(added.Content, "Could not load session:") {
			return
		}
	}
	t.Fatal("lookup failure was not reported")
}
