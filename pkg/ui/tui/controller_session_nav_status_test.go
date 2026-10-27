package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/ui/widgets"
)

func TestRunStatus_NavigatorAndInspectorAgree(t *testing.T) {
	ended := time.Now().UTC()
	for _, tc := range []struct {
		name   string
		event  string
		stored string
		ended  *time.Time
		want   string
	}{
		{name: "legacy_completed", stored: "completed", ended: &ended, want: "completed"},
		{name: "legacy_failed", stored: "failed", ended: &ended, want: "failed"},
		{name: "closed_after_running_event", event: "running", stored: "completed", ended: &ended, want: "completed"},
		{name: "closed_after_pause_event", event: "paused", stored: "cancelled", ended: &ended, want: "cancelled"},
		{name: "open_pause_event", event: "paused", stored: "running", want: "paused"},
		{name: "legacy_paused", stored: "paused", want: "paused"},
		{name: "legacy_running", stored: "running", want: "running"},
		{name: "unknown_legacy", want: "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := &runledger.RunOverview{Run: runledger.AgentRun{RunID: "run", Status: tc.stored, EndedAt: tc.ended}, Status: tc.event, EventCount: 7}
			byID := map[string]*runledger.RunOverview{}
			var rows []widgets.SessionNavNode
			flattenRunTree(node, 0, byID, &rows)
			record := runNodeToActivityRecord(byID["run"])
			if len(rows) != 1 || rows[0].Status != tc.want || string(record.Status) != tc.want || !strings.Contains(record.Detail, "Status: "+tc.want+"\n") {
				t.Fatalf("status disagrees: navigator=%+v, inspector=%+v, want %q", rows, record, tc.want)
			}
			if node.Status != tc.event || node.Run.Status != tc.stored {
				t.Fatal("presentation changed canonical status fields")
			}
		})
	}
}

func TestRefreshSessionNav_StoredTerminalStatus(t *testing.T) {
	store := newSessionNavRunLedger(t)
	ctx := context.Background()
	run, err := store.StartRun(ctx, runledger.AgentRun{SessionID: "session-2", AgentID: "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EndRun(ctx, run.RunID, "completed", time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	before, err := store.ListEvents(ctx, runledger.EventQuery{RunID: run.RunID})
	if err != nil || len(before) != 0 {
		t.Fatalf("fixture must represent a saved run without lifecycle events: %v", err)
	}
	ctrl, app := newSessionNavTestController(t, store)
	ctrl.refreshSessionNav()
	nav := lastSessionNavMsg(t, app)
	if len(nav.Nodes) != 1 || nav.Nodes[0].Status != "completed" {
		t.Fatalf("saved completed run appears active: %+v", nav.Nodes)
	}
	ctrl.handleSessionNodeSelected(nav.Nodes[0])
	var selected *SetActivitiesMsg
	for _, msg := range drainAllMessages(app) {
		if activity, ok := msg.(SetActivitiesMsg); ok {
			selected = &activity
		}
	}
	if selected == nil || len(selected.Records) != 1 || selected.Records[0].Status != widgets.ActivityCompleted {
		t.Fatalf("inspector did not preserve saved completion: %+v", selected)
	}
	after, err := store.ListEvents(ctx, runledger.EventQuery{RunID: run.RunID})
	if err != nil || len(after) != 0 {
		t.Fatalf("navigation synthesized lifecycle events: %v", err)
	}
}
