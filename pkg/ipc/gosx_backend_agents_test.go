package ipc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/ipc/command"
	"m31labs.dev/buckley/pkg/ipc/gosxui"
	"m31labs.dev/buckley/pkg/mission"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/ui/viewmodel"
)

func TestGoSXBackendDispatchPropagatesAuthenticatedActor(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{
		ID: "session-actor", Principal: "alice", ProjectPath: dir,
		Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var captured command.SessionCommand
	gateway := command.NewGateway()
	gateway.Register(command.HandlerFunc(func(value command.SessionCommand) error {
		captured = value
		return nil
	}))
	server := NewServer(Config{ProjectRoot: dir}, store, nil, gateway, nil, config.DefaultConfig(), nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/app", nil)
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name: "alice", Scope: storage.TokenScopeMember,
	}))
	if err := (gosxBackend{server: server}).Dispatch(context.Background(), req, gosxui.CommandRequest{
		SessionID: "session-actor", Type: "input", Content: "hello",
	}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if captured.ID == "" || captured.AcceptedBy != "alice" || captured.SessionID != "session-actor" || captured.Content != "hello" {
		t.Fatalf("captured command = %+v", captured)
	}
}

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
	if parent.ID != "agent-parent" || parent.Persona != "review" || parent.Model != "example/frontier" || !strings.HasPrefix(parent.Task, "task-") || parent.Task == "review the repository" {
		t.Fatalf("GoSX parent agent = %+v", parent)
	}
	if len(parent.Children) != 1 || parent.Children[0].ID != "agent-child" || parent.Children[0].ParentID != "agent-parent" {
		t.Fatalf("GoSX child agents = %+v", parent.Children)
	}
	if containsAgentView(data.AgentRuns, "legacy-agent") {
		t.Fatalf("GoSX loaded retired Mission agent projection: %+v", data.AgentRuns)
	}
}

func TestGoSXBackendMergesDurableRunsWithLiveOverlay(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	for _, session := range []storage.Session{
		{ID: "session-1", Principal: "alice", ProjectPath: dir, Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now},
		{ID: "session-2", Principal: "alice", ProjectPath: dir, Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now},
	} {
		if err := store.CreateSession(&session); err != nil {
			t.Fatalf("CreateSession(%s): %v", session.ID, err)
		}
	}

	hub := telemetry.NewHub()
	server := NewServer(Config{ProjectRoot: dir}, store, hub, nil, nil, config.DefaultConfig(), nil, nil)
	evidenceStore, err := evidence.NewWithDB(store.DB(), filepath.Join(dir, "evidence"))
	if err != nil {
		t.Fatalf("evidence.NewWithDB: %v", err)
	}
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	if err := server.SetDurableStores(ledger, evidenceStore); err != nil {
		t.Fatalf("SetDurableStores: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server.runtimeTracker.Start(ctx)
	t.Cleanup(func() {
		cancel()
		server.runtimeTracker.Stop()
		hub.Close()
	})

	started := now.Add(-2 * time.Minute)
	ended := now.Add(-time.Minute)
	for _, run := range []runledger.AgentRun{
		{RunID: "durable-parent", SessionID: "session-1", AgentID: "planner", ModelID: "model-a", Backend: "local-process", Status: "running", StartedAt: started},
		{RunID: "durable-child", SessionID: "session-1", ParentRunID: "durable-parent", AgentID: "reviewer", ModelID: "model-b", Backend: "local-process", Status: "completed", StartedAt: started.Add(time.Second), EndedAt: &ended},
		{RunID: "blocked-child", SessionID: "session-1", ParentRunID: "durable-parent", AgentID: "blocked", ModelID: "model-c", Backend: "local-process", Status: "blocked", StartedAt: started.Add(2 * time.Second), EndedAt: &ended},
		{RunID: "other-session", SessionID: "session-2", AgentID: "hidden", Backend: "local-process", Status: "running", StartedAt: started},
	} {
		if _, err := ledger.StartRun(context.Background(), run); err != nil {
			t.Fatalf("StartRun(%s): %v", run.RunID, err)
		}
	}

	// The fresh tracker has no live events yet. Durable-only nonterminal state
	// must be explicit and terminal state must survive the restart boundary.
	data := loadGoSXSession(t, server, "alice", "session-1")
	if len(data.AgentRuns) != 1 || data.AgentRuns[0].ID != "durable-parent" {
		t.Fatalf("durable roots = %+v, want durable-parent only", data.AgentRuns)
	}
	parent := data.AgentRuns[0]
	if parent.Status != "resumable" {
		t.Fatalf("detached parent status = %q, want resumable", parent.Status)
	}
	if len(parent.Children) != 2 || parent.Children[0].ID != "durable-child" || parent.Children[1].ID != "blocked-child" {
		t.Fatalf("durable child tree = %+v", parent.Children)
	}
	if parent.Children[0].Status != "completed" {
		t.Fatalf("terminal durable child status = %q, want completed", parent.Children[0].Status)
	}
	if parent.Children[1].Status != "blocked" {
		t.Fatalf("blocked durable child status = %q, want blocked", parent.Children[1].Status)
	}
	if containsAgentView(data.AgentRuns, "other-session") {
		t.Fatalf("durable run from another session leaked: %+v", data.AgentRuns)
	}
	viewerData := loadGoSXSessionWithScope(t, server, "alice", storage.TokenScopeViewer, "session-1")
	if viewerData.CanWrite || countGoSXAgentViews(viewerData.AgentRuns) != 3 {
		t.Fatalf("viewer durable projection = CanWrite:%v runs:%+v", viewerData.CanWrite, viewerData.AgentRuns)
	}
	foreignData := loadGoSXSessionWithScope(t, server, "mallory", storage.TokenScopeViewer, "session-1")
	if foreignData.Current != nil || len(foreignData.AgentRuns) != 0 {
		t.Fatalf("foreign viewer accessed session projection: current=%+v runs=%+v", foreignData.Current, foreignData.AgentRuns)
	}

	// Overlay the same stable IDs with telemetry. The live tree must enrich the
	// durable nodes without duplicating them, while a stale nonterminal event
	// cannot overwrite a durable terminal state.
	hub.Publish(telemetry.Event{
		Type:      telemetry.EventSubagentSpawned,
		SessionID: "session-1",
		TaskID:    "durable-parent",
		Timestamp: now,
		Data: map[string]any{
			"parent_session_id": "session-1",
			"agent":             "planner-live",
			"state":             "running",
		},
	})
	hub.Publish(telemetry.Event{
		Type:      telemetry.EventSubagentFailed,
		SessionID: "session-1",
		TaskID:    "durable-child",
		Timestamp: now,
		Data: map[string]any{
			"parent_session_id": "session-1",
			"parent_run_id":     "durable-parent",
			"state":             "failed",
		},
	})
	hub.Publish(telemetry.Event{
		Type:      telemetry.EventSubagentCompleted,
		SessionID: "session-1",
		TaskID:    "blocked-child",
		Timestamp: now,
		Data: map[string]any{
			"parent_session_id": "session-1",
			"parent_run_id":     "durable-parent",
			"state":             "completed",
		},
	})
	waitForAgentRuns(t, server, "session-1", 3)
	data = loadGoSXSession(t, server, "alice", "session-1")
	if countGoSXAgentViews(data.AgentRuns) != 3 {
		t.Fatalf("merged agent count = %d, want 3: %+v", countGoSXAgentViews(data.AgentRuns), data.AgentRuns)
	}
	if data.AgentRuns[0].Agent != "planner-live" {
		t.Fatalf("live overlay agent = %q, want planner-live", data.AgentRuns[0].Agent)
	}
	if data.AgentRuns[0].Children[0].Status != "completed" {
		t.Fatalf("live terminal conflict status = %q, want completed", data.AgentRuns[0].Children[0].Status)
	}
	if data.AgentRuns[0].Children[1].Status != "blocked" {
		t.Fatalf("live blocked conflict status = %q, want blocked", data.AgentRuns[0].Children[1].Status)
	}
}

func TestGoSXBackendLoadsNewestBoundedDurableRunsInChronologicalOrder(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	started := time.Date(2026, time.August, 20, 10, 0, 0, 0, time.UTC)
	if err := store.CreateSession(&storage.Session{
		ID: "session-history", Principal: "alice", ProjectPath: dir,
		Status: storage.SessionStatusActive, CreatedAt: started, LastActive: started,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	evidenceStore, err := evidence.NewWithDB(store.DB(), filepath.Join(dir, "evidence"))
	if err != nil {
		t.Fatalf("evidence.NewWithDB: %v", err)
	}
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	for i := 0; i < 270; i++ {
		id := fmt.Sprintf("history-%03d", i)
		if _, err := ledger.StartRun(context.Background(), runledger.AgentRun{
			RunID: id, SessionID: "session-history", Status: "running", StartedAt: started.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("StartRun(%s): %v", id, err)
		}
	}
	hub := telemetry.NewHub()
	server := NewServer(Config{ProjectRoot: dir}, store, hub, nil, nil, config.DefaultConfig(), nil, nil)
	if err := server.SetDurableStores(ledger, evidenceStore); err != nil {
		t.Fatalf("SetDurableStores: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server.runtimeTracker.Start(ctx)
	t.Cleanup(func() {
		cancel()
		server.runtimeTracker.Stop()
		hub.Close()
	})
	for i := 0; i < 20; i++ {
		hub.Publish(telemetry.Event{
			Type:      telemetry.EventSubagentSpawned,
			SessionID: "session-history",
			TaskID:    fmt.Sprintf("live-%03d", i),
			Timestamp: started.Add(time.Duration(300+i) * time.Second),
			Data: map[string]any{
				"parent_session_id": "session-history",
				"state":             "running",
				"task":              "production_secret",
			},
		})
	}
	waitForAgentRuns(t, server, "session-history", 20)

	data := loadGoSXSessionWithScope(t, server, "alice", storage.TokenScopeViewer, "session-history")
	if len(data.AgentRuns) != gosxAgentRunLimit {
		t.Fatalf("agent run window = %d, want %d", len(data.AgentRuns), gosxAgentRunLimit)
	}
	if data.AgentRuns[0].ID != "history-034" || data.AgentRuns[len(data.AgentRuns)-1].ID != "live-019" {
		t.Fatalf("bounded chronological window = first:%q last:%q", data.AgentRuns[0].ID, data.AgentRuns[len(data.AgentRuns)-1].ID)
	}
	if containsAgentView(data.AgentRuns, "history-000") || containsAgentView(data.AgentRuns, "history-033") || !containsAgentView(data.AgentRuns, "history-269") {
		t.Fatalf("newest window selection is incorrect")
	}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("live-%03d", i)
		run := findAgentView(data.AgentRuns, id)
		if run == nil || run.Task == "production_secret" || !strings.HasPrefix(run.Task, "task-") {
			t.Fatalf("live run %q was not retained with confidential task marker: %+v", id, run)
		}
	}
	markers := make(map[string]string, 20)
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("live-%03d", i)
		marker := findAgentView(data.AgentRuns, id).Task
		if prior, duplicate := markers[marker]; duplicate {
			t.Fatalf("equal task bodies correlated across runs %q and %q via %q", prior, id, marker)
		}
		markers[marker] = id
	}
}

func TestGoSXBackendSanitizesDurableAgentMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{
		ID: "session-safe", Principal: "alice", ProjectPath: dir,
		Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	evidenceStore, err := evidence.NewWithDB(store.DB(), filepath.Join(dir, "evidence"))
	if err != nil {
		t.Fatalf("evidence.NewWithDB: %v", err)
	}
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	rawID := strings.Repeat("secret run body ", 200)
	rawTask := strings.Repeat("sensitive task text ", 500)
	rawAgent := strings.Repeat("agent", 60) + "\x00hidden"
	rawModel := strings.Repeat("model", 60) + "\nleak"
	if _, err := ledger.StartRun(context.Background(), runledger.AgentRun{
		RunID: rawID, SessionID: "session-safe", ParentRunID: strings.Repeat("missing parent ", 100),
		TaskID: rawTask, AgentID: rawAgent, ModelID: rawModel, Status: "failed", StartedAt: now,
	}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	server := NewServer(Config{ProjectRoot: dir}, store, nil, nil, nil, config.DefaultConfig(), nil, nil)
	if err := server.SetDurableStores(ledger, evidenceStore); err != nil {
		t.Fatalf("SetDurableStores: %v", err)
	}

	data := loadGoSXSessionWithScope(t, server, "alice", storage.TokenScopeViewer, "session-safe")
	if len(data.AgentRuns) != 1 {
		t.Fatalf("sanitized runs = %+v", data.AgentRuns)
	}
	run := data.AgentRuns[0]
	if !strings.HasPrefix(run.ID, "run-") || !strings.HasPrefix(run.ParentID, "run-") || !strings.HasPrefix(run.Task, "task-") {
		t.Fatalf("identifier sanitization = %+v", run)
	}
	if strings.Contains(run.ID, "secret") || strings.Contains(run.Task, "sensitive") || strings.ContainsRune(run.Agent, '\x00') || strings.ContainsRune(run.Model, '\n') {
		t.Fatalf("unsafe durable metadata rendered: %+v", run)
	}
	if len([]rune(run.Agent)) > 128 || len([]rune(run.Model)) > 128 || run.Status != "failed" {
		t.Fatalf("bounded durable metadata = %+v", run)
	}
}

func TestAgentViewsFingerprintsLiveTaskBodies(t *testing.T) {
	key := [32]byte{1, 2, 3, 4}
	runs := []viewmodel.AgentRun{
		{ID: "live-production-a", ParentSessionID: "session-1", Status: "running", Task: "production_secret"},
		{ID: "live-production-b", ParentSessionID: "session-1", Status: "running", Task: "production_secret"},
		{ID: "live-password", ParentSessionID: "session-1", Status: "running", Task: "password"},
		{ID: "durable-id", ParentSessionID: "session-1", Status: "completed", Task: "production_secret", TaskIsID: true},
	}
	views := agentViews(runs, key)
	if len(views) != 4 {
		t.Fatalf("agent views = %+v", views)
	}
	for _, view := range views {
		if !strings.HasPrefix(view.Task, "task-") || strings.Contains(view.Task, "password") || strings.Contains(view.Task, "production_secret") {
			t.Fatalf("task value was not rendered as an opaque marker: %+v", view)
		}
	}
	if views[0].Task == views[1].Task {
		t.Fatalf("equal bodies correlated across distinct runs: %q", views[0].Task)
	}
	if again := agentViews(runs, key); again[0].Task != views[0].Task {
		t.Fatalf("marker was not stable within one server: first=%q again=%q", views[0].Task, again[0].Task)
	}
	otherKey := [32]byte{4, 3, 2, 1}
	if other := agentViews(runs, otherKey); other[0].Task == views[0].Task {
		t.Fatalf("marker correlated across server keys: %q", views[0].Task)
	}
	if withoutKey := agentViews(runs, [32]byte{}); withoutKey[0].Task != "" {
		t.Fatalf("missing server key did not fail closed: %q", withoutKey[0].Task)
	}
}

func TestGoSXBackendReconcilesOldTerminalBeforeBoundingLiveOverlay(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	started := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	for _, session := range []storage.Session{
		{ID: "session-window", Principal: "alice", ProjectPath: dir, Status: storage.SessionStatusActive, CreatedAt: started, LastActive: started},
		{ID: "session-foreign", Principal: "alice", ProjectPath: dir, Status: storage.SessionStatusActive, CreatedAt: started, LastActive: started},
	} {
		if err := store.CreateSession(&session); err != nil {
			t.Fatalf("CreateSession(%s): %v", session.ID, err)
		}
	}
	evidenceStore, err := evidence.NewWithDB(store.DB(), filepath.Join(dir, "evidence"))
	if err != nil {
		t.Fatalf("evidence.NewWithDB: %v", err)
	}
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	ended := started.Add(500 * time.Millisecond)
	for i := 0; i <= gosxAgentRunLimit; i++ {
		run := runledger.AgentRun{
			RunID: fmt.Sprintf("window-%03d", i), SessionID: "session-window", Status: "running", StartedAt: started.Add(time.Duration(i) * time.Second),
		}
		if i == 0 {
			run.Status = "completed"
			run.EndedAt = &ended
		}
		if _, err := ledger.StartRun(context.Background(), run); err != nil {
			t.Fatalf("StartRun(%s): %v", run.RunID, err)
		}
	}
	if _, err := ledger.StartRun(context.Background(), runledger.AgentRun{
		RunID: "foreign-collision", SessionID: "session-foreign", Status: "completed", StartedAt: started,
	}); err != nil {
		t.Fatalf("StartRun(foreign): %v", err)
	}

	hub := telemetry.NewHub()
	server := NewServer(Config{ProjectRoot: dir}, store, hub, nil, nil, config.DefaultConfig(), nil, nil)
	if err := server.SetDurableStores(ledger, evidenceStore); err != nil {
		t.Fatalf("SetDurableStores: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server.runtimeTracker.Start(ctx)
	t.Cleanup(func() {
		cancel()
		server.runtimeTracker.Stop()
		hub.Close()
	})
	for _, id := range []string{"window-000", "foreign-collision"} {
		hub.Publish(telemetry.Event{
			Type: telemetry.EventSubagentSpawned, SessionID: "session-window", TaskID: id,
			Timestamp: started.Add(2 * time.Hour),
			Data:      map[string]any{"parent_session_id": "session-window", "state": "running"},
		})
	}
	waitForAgentRuns(t, server, "session-window", 2)

	backend := gosxBackend{server: server}
	durableRuns, err := backend.durableAgentRuns(context.Background(), "session-window")
	if err != nil {
		t.Fatalf("durableAgentRuns: %v", err)
	}
	canonical, live, err := backend.reconcileLiveAgentRuns(context.Background(), "session-window", durableRuns, server.runtimeTracker.GetAgentRuns("session-window"))
	if err != nil {
		t.Fatalf("reconcileLiveAgentRuns: %v", err)
	}
	reconciled := viewmodel.MergeAgentRuns(viewmodel.MergeAgentRuns(durableRuns, canonical), live)
	oldTerminal := findViewmodelAgentRun(reconciled, "window-000")
	if oldTerminal == nil || oldTerminal.Status != "completed" {
		t.Fatalf("canonical terminal did not win before final bound: %+v", oldTerminal)
	}
	if findViewmodelAgentRun(reconciled, "foreign-collision") != nil {
		t.Fatalf("foreign canonical run survived session reconciliation: %+v", reconciled)
	}

	data := loadGoSXSessionWithScope(t, server, "alice", storage.TokenScopeViewer, "session-window")
	if countGoSXAgentViews(data.AgentRuns) != gosxAgentRunLimit {
		t.Fatalf("bounded agent count = %d, want %d", countGoSXAgentViews(data.AgentRuns), gosxAgentRunLimit)
	}
	if containsAgentView(data.AgentRuns, "window-000") || !containsAgentView(data.AgentRuns, "window-256") || containsAgentView(data.AgentRuns, "foreign-collision") {
		t.Fatalf("stale live run displaced canonical newest window: %+v", data.AgentRuns)
	}
}

func TestBoundAgentRunsPromotesCurrentChildWhenParentFallsOutsideWindow(t *testing.T) {
	started := time.Date(2026, time.August, 20, 11, 0, 0, 0, time.UTC)
	runs := []viewmodel.AgentRun{{
		ID: "old-parent", Status: "completed", StartedAt: started,
		Children: []viewmodel.AgentRun{{
			ID: "current-child", ParentID: "old-parent", Status: "running", StartedAt: started.Add(time.Hour), UpdatedAt: started.Add(time.Hour),
		}},
	}}
	bounded := boundAgentRuns(runs, 1)
	if len(bounded) != 1 || bounded[0].ID != "current-child" || bounded[0].ParentID != "old-parent" || len(bounded[0].Children) != 0 {
		t.Fatalf("promoted child projection = %+v", bounded)
	}
}

func TestGoSXBackendProjectsDurableRunAfterStorageReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New(first): %v", err)
	}
	now := time.Now().UTC()
	if err := store.CreateSession(&storage.Session{
		ID: "session-reopen", Principal: "alice", ProjectPath: dir,
		Status: storage.SessionStatusActive, CreatedAt: now, LastActive: now,
	}); err != nil {
		_ = store.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	firstLedger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		_ = store.Close()
		t.Fatalf("runledger.NewWithDB(first): %v", err)
	}
	ended := now.Add(time.Second)
	if _, err := firstLedger.StartRun(context.Background(), runledger.AgentRun{
		RunID: "reopened-run", SessionID: "session-reopen", Status: "completed", StartedAt: now, EndedAt: &ended,
	}); err != nil {
		_ = store.Close()
		t.Fatalf("StartRun: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(first): %v", err)
	}

	reopened, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New(reopened): %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	evidenceStore, err := evidence.NewWithDB(reopened.DB(), filepath.Join(dir, "evidence"))
	if err != nil {
		t.Fatalf("evidence.NewWithDB: %v", err)
	}
	ledger, err := runledger.NewWithDB(reopened.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB(reopened): %v", err)
	}
	server := NewServer(Config{ProjectRoot: dir}, reopened, nil, nil, nil, config.DefaultConfig(), nil, nil)
	if err := server.SetDurableStores(ledger, evidenceStore); err != nil {
		t.Fatalf("SetDurableStores: %v", err)
	}

	data := loadGoSXSessionWithScope(t, server, "alice", storage.TokenScopeViewer, "session-reopen")
	if len(data.AgentRuns) != 1 || data.AgentRuns[0].ID != "reopened-run" || data.AgentRuns[0].Status != "completed" {
		t.Fatalf("reopened durable projection = %+v", data.AgentRuns)
	}
}

func countGoSXAgentViews(runs []gosxui.AgentView) int {
	count := 0
	for _, run := range runs {
		count += 1 + countGoSXAgentViews(run.Children)
	}
	return count
}

func loadGoSXSession(t *testing.T, server *Server, principal, sessionID string) gosxui.PageData {
	return loadGoSXSessionWithScope(t, server, principal, storage.TokenScopeMember, sessionID)
}

func loadGoSXSessionWithScope(t *testing.T, server *Server, principal, scope, sessionID string) gosxui.PageData {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://localhost/?session="+sessionID, nil)
	req = req.WithContext(context.WithValue(req.Context(), principalContextKey, &requestPrincipal{
		Name:  principal,
		Scope: scope,
	}))
	data, err := (gosxBackend{server: server}).Load(req.Context(), req)
	if err != nil {
		t.Fatalf("GoSX Load: %v", err)
	}
	return data
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

func findViewmodelAgentRun(runs []viewmodel.AgentRun, id string) *viewmodel.AgentRun {
	for i := range runs {
		if runs[i].ID == id {
			return &runs[i]
		}
		if found := findViewmodelAgentRun(runs[i].Children, id); found != nil {
			return found
		}
	}
	return nil
}

func containsAgentView(agents []gosxui.AgentView, id string) bool {
	for _, agent := range agents {
		if agent.ID == id || containsAgentView(agent.Children, id) {
			return true
		}
	}
	return false
}

func findAgentView(agents []gosxui.AgentView, id string) *gosxui.AgentView {
	for i := range agents {
		if agents[i].ID == id {
			return &agents[i]
		}
		if found := findAgentView(agents[i].Children, id); found != nil {
			return found
		}
	}
	return nil
}
