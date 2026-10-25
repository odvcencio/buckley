package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
)

func closeRuntimeTestSessions(t *testing.T, sessions []*SessionState) {
	t.Helper()
	t.Cleanup(func() {
		for _, sess := range sessions {
			_ = sess.ToolRegistry.Close()
			if sess.HookCloser != nil {
				_ = sess.HookCloser.Close()
			}
		}
	})
}

func TestLoadOrCreateControllerSessions_DefersInactiveRuntimes(t *testing.T) {
	cfg, store, workDir := newControllerSessionTestConfig(t)
	cfg.SessionID = "selected"
	createControllerTestSession(t, store, "selected", workDir, storage.SessionStatusActive, time.Now())
	for i := range 100 {
		createControllerTestSession(t, store, fmt.Sprintf("idle-%d", i), workDir, storage.SessionStatusActive, time.Now())
	}
	var created []string
	cfg.CodeModeToolFactory = func(id string) (tool.Tool, error) {
		created = append(created, id)
		return repairNameTool{name: "exec_program"}, nil
	}
	sessions, selected, err := loadOrCreateControllerSessions(cfg, workDir)
	if err != nil {
		t.Fatal(err)
	}
	closeRuntimeTestSessions(t, sessions)
	if len(created) != 1 || created[0] != "selected" || len(sessions) != 101 {
		t.Fatalf("created runtimes=%v, sessions=%d", created, len(sessions))
	}
	for i, sess := range sessions {
		if i == selected {
			if sess.runtimeLoader != nil || sess.ToolRegistry == nil || sess.SkillRegistry == nil {
				t.Fatal("selected runtime was not initialized")
			}
		} else if sess.runtimeLoader == nil || sess.ToolRegistry != nil || sess.SkillRegistry != nil || sess.HookCloser != nil {
			t.Fatalf("unused session %s eagerly initialized a runtime", sess.ID)
		}
	}
}

func TestSwitchToSessionLocked_RetriesRuntimeWithoutLosingState(t *testing.T) {
	cfg, store, workDir := newControllerSessionTestConfig(t)
	cfg.SessionID = "selected"
	for _, id := range []string{"selected", "deferred"} {
		createControllerTestSession(t, store, id, workDir, storage.SessionStatusActive, time.Now())
	}
	if err := store.SaveSessionSkill("deferred", "recoverable", "model", "saved scope"); err != nil {
		t.Fatal(err)
	}
	var created []string
	cfg.CodeModeToolFactory = func(id string) (tool.Tool, error) {
		created = append(created, id)
		return repairNameTool{name: "exec_program"}, nil
	}
	sessions, selected, err := loadOrCreateControllerSessions(cfg, workDir)
	if err != nil {
		t.Fatal(err)
	}
	closeRuntimeTestSessions(t, sessions)
	deferredIndex := 1 - selected
	deferred := sessions[deferredIndex]
	deferred.MessageQueue = []QueuedMessage{{Content: "queued work"}}
	deferred.DisableToolsNextTurn = true
	ctrl, _ := newSessionNavTestController(t, nil)
	ctrl.cfg, ctrl.store, ctrl.workDir = cfg.Config, store, workDir
	ctrl.sessions, ctrl.currentSession = sessions, selected
	ctrl.conversation, ctrl.registry = sessions[selected].Conversation, sessions[selected].ToolRegistry
	ctrl.switchToSessionLocked(deferredIndex)
	if ctrl.currentSession != selected || deferred.runtimeLoader == nil || deferred.ToolRegistry != nil || len(created) != 1 {
		t.Fatal("missing playbook changed selection or partially published a runtime")
	}
	dir := filepath.Join(workDir, ".buckley", "skills")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "recoverable.md"), []byte("---\nname: recoverable\ndescription: Recover saved skill\nallowed_tools: [activate_skill, list_directory]\n---\nretained guidance\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctrl.switchToSessionLocked(deferredIndex)
	if ctrl.currentSession != deferredIndex || deferred.runtimeLoader != nil || !deferred.permissionsReady || !deferred.SkillRegistry.IsActive("recoverable") || len(created) != 2 {
		t.Fatal("retry did not restore the runtime, permissions, and skill")
	}
	if len(deferred.MessageQueue) != 1 || deferred.MessageQueue[0].Content != "queued work" || !deferred.DisableToolsNextTurn {
		t.Fatal("runtime initialization discarded queued session state")
	}
	ctrl.switchToSessionLocked(selected)
	ctrl.switchToSessionLocked(deferredIndex)
	if len(created) != 2 {
		t.Fatal("revisiting a tab rebuilt its existing runtime")
	}
}

func TestStreamResponseTurn_InitializesDeferredRuntimeBeforeAppendingPrompt(t *testing.T) {
	cfg, store, workDir := newControllerSessionTestConfig(t)
	cfg.SessionID = "selected"
	for _, id := range []string{"selected", "deferred"} {
		createControllerTestSession(t, store, id, workDir, storage.SessionStatusActive, time.Now())
	}
	if err := store.SaveMessage(&storage.Message{SessionID: "deferred", Role: "user", Content: "stored previous turn", Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var request model.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSEToolLoopTest(t, w, `{"id":"deferred-response","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant","content":"Both turns are present."},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	cfg.Config = newStreamIntegrationConfig(server.URL)
	mgr, err := model.NewManager(cfg.Config)
	if err != nil {
		t.Fatal(err)
	}
	sessions, selected, err := loadOrCreateControllerSessions(cfg, workDir)
	if err != nil {
		t.Fatal(err)
	}
	closeRuntimeTestSessions(t, sessions)
	deferred := sessions[1-selected]
	ctrl, _ := newSessionNavTestController(t, nil)
	ctrl.cfg, ctrl.store, ctrl.workDir = cfg.Config, store, workDir
	ctrl.modelMgr, ctrl.modelOverride = mgr, "gpt-4o"
	ctrl.sessions, ctrl.currentSession = sessions, selected
	ctrl.conversation, ctrl.registry = sessions[selected].Conversation, sessions[selected].ToolRegistry
	if ok := ctrl.streamResponseTurn(context.Background(), "queued user turn survives", deferred, agentloop.ReadOnlyIntent); !ok {
		t.Fatal("deferred turn failed")
	}
	body, _ := json.Marshal(request.Messages)
	if !strings.Contains(string(body), "stored previous turn") || !strings.Contains(string(body), "queued user turn survives") {
		t.Fatalf("history or new prompt was overwritten: %s", body)
	}
	if deferred.runtimeLoader != nil || !deferred.permissionsReady || ctrl.registry != sessions[selected].ToolRegistry || ctrl.currentSession != selected {
		t.Fatal("background turn changed the selected session or omitted permissions")
	}
}
