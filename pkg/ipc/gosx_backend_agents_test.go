package ipc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/ipc/gosxui"
	"m31labs.dev/buckley/pkg/mission"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/ui/viewmodel"
)

func TestGoSXBackendLoadsAgentsFromSharedRuntimeProjection(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
	if err := store.CreateSession(&storage.Session{
		ID:          "session-1",
		Principal:   "alice",
		ProjectPath: dir,
		Status:      storage.SessionStatusActive,
		CreatedAt:   now,
		LastActive:  now,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	hub := telemetry.NewHub()
	server := NewServer(Config{ProjectRoot: dir}, store, hub, nil, nil, config.DefaultConfig(), nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	server.runtimeTracker.Start(ctx)
	t.Cleanup(func() {
		cancel()
		server.runtimeTracker.Stop()
		hub.Close()
	})

	// Seed the retired projection to prove GoSX no longer reads it.
	if err := server.missionStore.RecordAgentActivity(&mission.AgentActivity{
		AgentID:   "legacy-agent",
		SessionID: "session-1",
		AgentType: "legacy",
		Action:    "stale activity",
		Status:    "active",
		Timestamp: now,
	}); err != nil {
		t.Fatalf("RecordAgentActivity: %v", err)
	}

	hub.Publish(telemetry.Event{
		Type:      telemetry.EventSubagentSpawned,
		SessionID: "session-1",
		TaskID:    "agent-parent",
		Data: map[string]any{
			"parent_session_id": "session-1",
			"agent":             "reviewer",
			"persona":           "review",
			"model":             "example/frontier",
			"state":             "running",
			"task":              "review the repository",
		},
	})
	hub.Publish(telemetry.Event{
		Type:      telemetry.EventSubagentSpawned,
		SessionID: "session-1",
		TaskID:    "agent-child",
		Data: map[string]any{
			"parent_session_id": "session-1",
			"parent_run_id":     "agent-parent",
			"agent":             "researcher",
			"state":             "running",
			"task":              "trace call sites",
		},
	})
	waitForAgentRuns(t, server, "session-1", 2)

	req := httptest.NewRequest(http.MethodGet, "http://localhost/?session=session-1", nil)
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  "alice",
		Scope: storage.TokenScopeMember,
	}))
	data, err := (gosxBackend{server: server}).Load(req.Context(), req)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(data.AgentRuns) != 1 {
		t.Fatalf("GoSX agent roots = %+v, want one", data.AgentRuns)
	}
	parent := data.AgentRuns[0]
	if parent.ID != "agent-parent" || parent.Persona != "review" || parent.Model != "example/frontier" || parent.Task != "review the repository" {
		t.Fatalf("GoSX parent agent = %+v", parent)
	}
	if len(parent.Children) != 1 || parent.Children[0].ID != "agent-child" || parent.Children[0].ParentID != "agent-parent" {
		t.Fatalf("GoSX child agents = %+v", parent.Children)
	}
	if containsAgentView(data.AgentRuns, "legacy-agent") {
		t.Fatalf("GoSX loaded retired Mission agent projection: %+v", data.AgentRuns)
	}
}

func waitForAgentRuns(t *testing.T, server *Server, sessionID string, count int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countAgentRuns(server.runtimeTracker.GetAgentRuns(sessionID)) == count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d shared agent runs", count)
}

func countAgentRuns(runs []viewmodel.AgentRun) int {
	count := 0
	for _, run := range runs {
		count += 1 + countAgentRuns(run.Children)
	}
	return count
}

func containsAgentView(agents []gosxui.AgentView, id string) bool {
	for _, agent := range agents {
		if agent.ID == id || containsAgentView(agent.Children, id) {
			return true
		}
	}
	return false
}
