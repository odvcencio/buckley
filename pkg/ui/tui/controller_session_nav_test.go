package tui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/ui/widgets"
	"m31labs.dev/fluffyui/backend/sim"
)

func newSessionNavTestController(t *testing.T, runLedger runledger.Store) (*Controller, *WidgetApp) {
	t.Helper()
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.backend.Fini)
	ctrl := &Controller{
		app: app,
		cfg: &config.Config{},
		sessions: []*SessionState{
			{ID: "session-1", Conversation: conversation.New("session-1")},
			{ID: "session-2", Conversation: conversation.New("session-2")},
		},
		currentSession: 1,
		runLedger:      runLedger,
	}
	return ctrl, app
}

func lastSessionNavMsg(t *testing.T, app *WidgetApp) SessionNavMsg {
	t.Helper()
	msgs := drainAllMessages(app)
	for i := len(msgs) - 1; i >= 0; i-- {
		if nav, ok := msgs[i].(SessionNavMsg); ok {
			return nav
		}
	}
	t.Fatal("expected a SessionNavMsg in the queue")
	return SessionNavMsg{}
}

func TestRefreshSessionNav_FlatFallbackWithoutRunLedger(t *testing.T) {
	ctrl, app := newSessionNavTestController(t, nil)

	ctrl.refreshSessionNav()

	nav := lastSessionNavMsg(t, app)
	if len(nav.Nodes) != 2 {
		t.Fatalf("nodes len = %d, want 2", len(nav.Nodes))
	}
	if nav.Nodes[0].ID != "session-1" || nav.Nodes[1].ID != "session-2" {
		t.Fatalf("unexpected node IDs: %+v", nav.Nodes)
	}
	if !nav.Nodes[1].Active {
		t.Fatal("expected the current session (index 1) to be marked active")
	}
	if nav.Nodes[0].Active {
		t.Fatal("expected the non-current session to be inactive")
	}
}

func newSessionNavRunLedger(t *testing.T) runledger.Store {
	t.Helper()
	store, err := runledger.New(filepath.Join(t.TempDir(), "runledger.db"))
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRefreshSessionNav_FlatFallbackWhenNoRunForSession(t *testing.T) {
	store := newSessionNavRunLedger(t)
	ctrl, app := newSessionNavTestController(t, store)

	ctrl.refreshSessionNav()

	nav := lastSessionNavMsg(t, app)
	if len(nav.Nodes) != 2 {
		t.Fatalf("nodes len = %d, want 2 (flat fallback)", len(nav.Nodes))
	}
	if ctrl.sessionRunNodes != nil {
		t.Fatal("expected sessionRunNodes to be nil in flat-fallback mode")
	}
}

func TestRefreshSessionNav_UsesRunTreeWhenRunExists(t *testing.T) {
	store := newSessionNavRunLedger(t)
	ctx := context.Background()

	root, err := store.StartRun(ctx, runledger.AgentRun{SessionID: "session-2", AgentID: "primary"})
	if err != nil {
		t.Fatalf("StartRun(root): %v", err)
	}
	time.Sleep(2 * time.Millisecond) // ensure a distinct StartedAt ordering
	child, err := store.StartRun(ctx, runledger.AgentRun{SessionID: "session-2", ParentRunID: root.RunID, AgentID: "investigator"})
	if err != nil {
		t.Fatalf("StartRun(child): %v", err)
	}

	ctrl, app := newSessionNavTestController(t, store)
	ctrl.refreshSessionNav()

	nav := lastSessionNavMsg(t, app)
	if len(nav.Nodes) != 2 {
		t.Fatalf("nodes len = %d, want 2 (root + child)", len(nav.Nodes))
	}
	if nav.Nodes[0].ID != root.RunID || nav.Nodes[0].Depth != 0 {
		t.Fatalf("root node = %+v, want ID=%s Depth=0", nav.Nodes[0], root.RunID)
	}
	if nav.Nodes[1].ID != child.RunID || nav.Nodes[1].Depth != 1 {
		t.Fatalf("child node = %+v, want ID=%s Depth=1", nav.Nodes[1], child.RunID)
	}
	if len(ctrl.sessionRunNodes) != 2 {
		t.Fatalf("sessionRunNodes len = %d, want 2", len(ctrl.sessionRunNodes))
	}
}

func TestHandleSessionNodeSelected_ShowsRunInInspector(t *testing.T) {
	store := newSessionNavRunLedger(t)
	ctx := context.Background()
	root, err := store.StartRun(ctx, runledger.AgentRun{SessionID: "session-2", AgentID: "primary"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	ctrl, app := newSessionNavTestController(t, store)
	ctrl.refreshSessionNav()
	drainAllMessages(app)

	ctrl.handleSessionNodeSelected(widgets.SessionNavNode{ID: root.RunID})

	msgs := drainAllMessages(app)
	var found *SetActivitiesMsg
	for _, msg := range msgs {
		if m, ok := msg.(SetActivitiesMsg); ok {
			found = &m
		}
	}
	if found == nil {
		t.Fatal("expected a SetActivitiesMsg after selecting a run node")
	}
	if len(found.Records) != 1 || found.Records[0].ID != root.RunID {
		t.Fatalf("activity records = %+v, want one record for %s", found.Records, root.RunID)
	}
}

func TestHandleSessionNodeSelected_FlatNodeIsNoOp(t *testing.T) {
	ctrl, app := newSessionNavTestController(t, nil)
	ctrl.refreshSessionNav()
	drainAllMessages(app)

	ctrl.handleSessionNodeSelected(widgets.SessionNavNode{ID: "session-1"})

	for _, msg := range drainAllMessages(app) {
		if _, ok := msg.(SetActivitiesMsg); ok {
			t.Fatal("expected no SetActivitiesMsg for a flat session-list node")
		}
	}
}

func TestLatestRootRun_PicksMostRecentRoot(t *testing.T) {
	older := runledger.AgentRun{RunID: "old", StartedAt: time.Now().Add(-time.Hour)}
	newer := runledger.AgentRun{RunID: "new", StartedAt: time.Now()}
	child := runledger.AgentRun{RunID: "child", ParentRunID: "old", StartedAt: time.Now()}

	got, ok := latestRootRun([]runledger.AgentRun{older, newer, child})
	if !ok {
		t.Fatal("expected a root run to be found")
	}
	if got.RunID != "new" {
		t.Fatalf("latestRootRun = %q, want new", got.RunID)
	}
}

func TestLatestRootRun_NoRootReturnsFalse(t *testing.T) {
	_, ok := latestRootRun([]runledger.AgentRun{{RunID: "child", ParentRunID: "parent"}})
	if ok {
		t.Fatal("expected no root run to be found")
	}
}
