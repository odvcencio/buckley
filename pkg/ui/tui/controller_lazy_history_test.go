package tui

import (
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/storage"
)

func TestSwitchToSessionLocked_DeferredHistory(t *testing.T) {
	for _, failRead := range []bool{false, true} {
		name := "loads_once"
		if failRead {
			name = "read_failure_preserves_selection"
		}
		t.Run(name, func(t *testing.T) {
			cfg, store, workDir := newControllerSessionTestConfig(t)
			cfg.SessionID = "selected"
			for _, id := range []string{"selected", "deferred"} {
				createControllerTestSession(t, store, id, workDir, storage.SessionStatusActive, time.Now())
			}
			for _, msg := range []storage.Message{
				{Role: "user", Content: "inspect", Tokens: 3},
				{Role: "assistant", Reasoning: "check the file", ToolCalls: `[{"id":"call-1","type":"function","function":{"name":"read_file","arguments":"{}"}}]`, Tokens: 5},
				{Role: "tool", Content: "evidence", ToolCallID: "call-1", Name: "read_file", Tokens: 7},
			} {
				msg.SessionID = "deferred"
				msg.Timestamp = time.Now()
				if err := store.SaveMessage(&msg); err != nil {
					t.Fatal(err)
				}
			}
			sessions, current, err := loadOrCreateControllerSessions(cfg, workDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, sess := range sessions {
					if sess.HookCloser != nil {
						_ = sess.HookCloser.Close()
					}
					_ = sess.ToolRegistry.Close()
				}
			})
			deferredIndex := 1 - current
			deferred := sessions[deferredIndex]
			if deferred.ID != "deferred" || !deferred.deferredHistory || len(deferred.Conversation.Messages) != 0 {
				t.Fatal("inactive history was loaded at startup")
			}
			ctrl, _ := newSessionNavTestController(t, nil)
			ctrl.sessions = sessions
			ctrl.currentSession = current
			ctrl.conversation = sessions[current].Conversation
			ctrl.registry = sessions[current].ToolRegistry
			ctrl.store = store
			if failRead {
				closedStore, err := storage.New(filepath.Join(t.TempDir(), "closed.db"))
				if err != nil {
					t.Fatal(err)
				}
				if err := closedStore.Close(); err != nil {
					t.Fatal(err)
				}
				ctrl.store = closedStore
			}
			ctrl.switchToSessionLocked(deferredIndex)
			if failRead {
				if ctrl.currentSession != current || ctrl.conversation != sessions[current].Conversation || !deferred.deferredHistory {
					t.Fatal("failed history load changed the active session")
				}
				return
			}
			messages := deferred.Conversation.Messages
			if ctrl.currentSession != deferredIndex || deferred.deferredHistory || len(messages) != 3 || deferred.Conversation.TokenCount != 15 {
				t.Fatal("deferred session did not restore its full history")
			}
			if len(messages[1].ToolCalls) != 1 || messages[1].ToolCalls[0].ID != "call-1" || messages[1].Reasoning != "check the file" || messages[2].ToolCallID != "call-1" || messages[2].Content != "evidence" {
				t.Fatal("history lost tool correlation or reasoning")
			}
			deferred.Conversation.Messages[0].Content = "in-memory update"
			ctrl.switchToSessionLocked(current)
			ctrl.switchToSessionLocked(deferredIndex)
			if deferred.Conversation.Messages[0].Content != "in-memory update" {
				t.Fatal("revisiting a session reloaded its history")
			}
		})
	}
}
