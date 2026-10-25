package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

func newHistoryResidencyTestController(t *testing.T) (*Controller, map[string]*SessionState) {
	t.Helper()
	cfg, store, workDir := newControllerSessionTestConfig(t)
	cfg.Config.Resources.SessionMemoryBytes = 64 << 10
	cfg.SessionID = "history-0"
	for i := range 8 {
		id := fmt.Sprintf("history-%d", i)
		createControllerTestSession(t, store, id, workDir, storage.SessionStatusActive, time.Now())
		for _, role := range []string{"user", "assistant"} {
			if err := store.SaveMessage(&storage.Message{SessionID: id, Role: role, Content: id + " " + strings.Repeat("evidence ", 400), Timestamp: time.Now(), Tokens: 900}); err != nil {
				t.Fatal(err)
			}
		}
	}
	sessions, selected, err := loadOrCreateControllerSessions(cfg, workDir)
	if err != nil {
		t.Fatal(err)
	}
	ctrl, _ := newSessionNavTestController(t, nil)
	ctrl.cfg, ctrl.store, ctrl.workDir = cfg.Config, store, workDir
	ctrl.sessions, ctrl.currentSession = sessions, selected
	ctrl.conversation, ctrl.registry = sessions[selected].Conversation, sessions[selected].ToolRegistry
	ctrl.historyClock, sessions[selected].historyUse = 1, 1
	byID := make(map[string]*SessionState)
	for _, sess := range sessions {
		byID[sess.ID] = sess
	}
	t.Cleanup(func() {
		for _, sess := range ctrl.sessions {
			_ = sess.ToolRegistry.Close()
			if sess.HookCloser != nil {
				_ = sess.HookCloser.Close()
			}
		}
	})
	return ctrl, byID
}

func TestIdleHistory_LimitsBodiesAndPreservesSessionState(t *testing.T) {
	ctrl, sessions := newHistoryResidencyTestController(t)
	old := sessions["history-0"]
	conv, registry, skills := old.Conversation, old.ToolRegistry, old.SkillRegistry
	wire := conv.ToModelMessages()
	old.continuation = new(model.ContinuationCoordinator)
	cursor := old.continuation
	old.undoStack = []turnUndoRecord{{messages: append([]conversation.Message(nil), conv.Messages[1:]...)}}
	old.redoStack = []turnUndoRecord{{}}
	old.DisableToolsNextTurn = true
	for i := 1; i < 8; i++ {
		ctrl.resumeSession(fmt.Sprintf("history-%d", i))
		resident := 0
		for j, sess := range ctrl.sessions {
			if j != ctrl.currentSession {
				resident += sess.Conversation.WorkingSetBytes()
			}
		}
		if resident > sessionWorkingMemoryBytes(ctrl.cfg) {
			t.Fatalf("idle bodies retained %d bytes", resident)
		}
	}
	if old.historyReference == nil || len(conv.Messages) != 0 || sessions["history-6"].historyReference != nil {
		t.Fatal("idle cache did not evict the oldest view and keep a recent one")
	}
	ctrl.resumeSession(old.ID)
	if old.historyReference != nil || !reflect.DeepEqual(wire, conv.ToModelMessages()) || old.Conversation != conv || old.ToolRegistry != registry || old.SkillRegistry != skills {
		t.Fatal("restoring history replaced session runtime or changed context")
	}
	if old.continuation != cursor || len(old.undoStack) != 1 || len(old.redoStack) != 1 || !old.DisableToolsNextTurn {
		t.Fatal("history residency discarded continuation, undo or pending intent")
	}
}

func TestIdleHistory_PinsBusyQueuedAndFailedPersistence(t *testing.T) {
	for _, state := range []string{"streaming", "compacting", "queued", "persistence"} {
		t.Run(state, func(t *testing.T) {
			ctrl, sessions := newHistoryResidencyTestController(t)
			pinned := sessions["history-0"]
			switch state {
			case "streaming":
				pinned.Streaming = true
			case "compacting":
				pinned.Compacting = true
			case "queued":
				pinned.MessageQueue = []QueuedMessage{{Content: "pending work"}}
			case "persistence":
				pinned.persistenceErr = errors.New("write failed")
			}
			for i := 1; i < 8; i++ {
				ctrl.resumeSession(fmt.Sprintf("history-%d", i))
			}
			if pinned.historyReference != nil || len(pinned.Conversation.Messages) != 2 {
				t.Fatal("protected session bodies were released")
			}
		})
	}
}

func TestIdleHistory_NewSessionBoundsPreviousForeground(t *testing.T) {
	ctrl, _ := newHistoryResidencyTestController(t)
	for i := 1; i <= 4; i++ {
		ctrl.resumeSession(fmt.Sprintf("history-%d", i))
	}
	previous := ctrl.sessions[ctrl.currentSession].ID
	ctrl.newSession()
	if ctrl.sessions[ctrl.currentSession].ID == previous {
		t.Fatal("new session was not created")
	}
	resident := 0
	for i, sess := range ctrl.sessions {
		if i != ctrl.currentSession {
			resident += sess.Conversation.WorkingSetBytes()
		}
	}
	if resident > sessionWorkingMemoryBytes(ctrl.cfg) {
		t.Fatal("starting a new session bypassed the inactive history budget")
	}
}

func TestIdleHistory_BackgroundTurnRestoresBeforeAppendingInput(t *testing.T) {
	ctrl, sessions := newHistoryResidencyTestController(t)
	for i := 1; i < 8; i++ {
		ctrl.resumeSession(fmt.Sprintf("history-%d", i))
	}
	old := sessions["history-0"]
	if old.historyReference == nil {
		t.Fatal("fixture did not release the background session")
	}
	var request model.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEToolLoopTest(t, w, `{"id":"restored","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Both saved context and new input are present."},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	cfg := newStreamIntegrationConfig(server.URL)
	cfg.Resources = ctrl.cfg.Resources
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctrl.cfg, ctrl.modelMgr, ctrl.modelOverride = cfg, mgr, "gpt-4o"
	selected, registry := ctrl.currentSession, ctrl.registry
	old.Streaming = true
	if !ctrl.streamResponseTurn(context.Background(), "new background input", old, agentloop.ReadOnlyIntent) {
		t.Fatal("background turn failed")
	}
	body, _ := json.Marshal(request.Messages)
	if !strings.Contains(string(body), "history-0 evidence") || !strings.Contains(string(body), "new background input") || old.historyReference != nil {
		t.Fatal("background turn replaced saved context with only the new input")
	}
	if ctrl.currentSession != selected || ctrl.registry != registry {
		t.Fatal("background rehydration changed the selected session")
	}
}

func TestIdleHistory_UndoRedoAfterEviction(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	ctrl, _, sess := newUndoTestController(t, dir)
	ctrl.cfg = config.DefaultConfig()
	ctrl.cfg.Resources.SessionMemoryBytes = 64 << 10
	store, err := storage.New(filepath.Join(t.TempDir(), "undo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctrl.store = store
	createControllerTestSession(t, store, sess.ID, dir, storage.SessionStatusActive, time.Now())
	boundary := ctrl.beginTurnUndo(sess)
	sess.Conversation.AddUserMessage("edit the file")
	if err := ctrl.saveLatestConversationMessage(sess); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("edited"), 0644); err != nil {
		t.Fatal(err)
	}
	sess.Conversation.AddAssistantMessage("edit completed")
	if err := ctrl.saveLatestConversationMessage(sess); err != nil {
		t.Fatal(err)
	}
	ctrl.finishTurnUndo(sess, boundary)
	for i := range 5 {
		id := fmt.Sprintf("idle-%d", i)
		createControllerTestSession(t, store, id, dir, storage.SessionStatusActive, time.Now())
		conv := conversation.New(id)
		conv.AddUserMessage(strings.Repeat("a", 4000))
		conv.AddAssistantMessage(strings.Repeat("b", 4000))
		if err := conv.SaveAllMessages(store); err != nil {
			t.Fatal(err)
		}
		ctrl.sessions = append(ctrl.sessions, &SessionState{ID: id, Conversation: conv})
		ctrl.resumeSession(id)
	}
	if sess.historyReference == nil || len(sess.undoStack) != 1 {
		t.Fatal("fixture failed to evict history while retaining the undo record")
	}
	ctrl.resumeSession(sess.ID)
	ctrl.undoLastTurn()
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "original" || len(sess.Conversation.Messages) != 0 {
		t.Fatal("undo after restore lost file or conversation semantics")
	}
	ctrl.redoLastTurn()
	data, err = os.ReadFile(file)
	if err != nil || string(data) != "edited" || len(sess.Conversation.Messages) != 2 {
		t.Fatal("redo after restore lost file or conversation semantics")
	}
}
