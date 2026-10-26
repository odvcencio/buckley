package runledger

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSQLiteStore_SessionRunOverview_MatchesReplay(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, run := range []AgentRun{
		{RunID: "old-root", SessionID: "session", StartedAt: base},
		{RunID: "new-child-of-old-root", SessionID: "session", ParentRunID: "old-root", StartedAt: base.Add(time.Hour)},
		{RunID: "root-z", SessionID: "session", StartedAt: base.Add(time.Minute)},
		{RunID: "root-a", SessionID: "session", StartedAt: base.Add(time.Minute), AgentID: "primary", ModelID: "fixture",
			Budget: map[string]any{"body": strings.Repeat("b", 65536)}, Outcome: map[string]any{"body": strings.Repeat("o", 65536)}},
		{RunID: "child-z", SessionID: "session", ParentRunID: "root-a", StartedAt: base.Add(2 * time.Minute)},
		{RunID: "child-a", SessionID: "another-session", ParentRunID: "root-a", StartedAt: base.Add(2 * time.Minute)},
		{RunID: "grandchild", SessionID: "another-session", ParentRunID: "child-a", StartedAt: base.Add(3 * time.Minute)},
	} {
		if _, err := store.StartRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []Event{
		{RunID: "root-a", Type: EventRunStarted},
		{RunID: "root-a", Type: EventRunPaused},
		{RunID: "root-a", Type: "future.event", TaskID: "task", Payload: map[string]any{"text": "completed work café 日本語"}},
		{RunID: "child-a", Type: EventRunStarted},
		{RunID: "child-a", Type: EventRunCompleted},
		{RunID: "child-a", Type: "future.after_completion", TaskID: "task"},
		{RunID: "child-z", Type: EventRunStarted},
		{RunID: "child-z", Type: EventRunFailed},
	} {
		if _, err := store.Append(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.EndRun(ctx, "child-a", "completed", base.Add(4*time.Minute), map[string]any{"result": "kept"}); err != nil {
		t.Fatal(err)
	}
	// Gaps are replay evidence, not a substitute for counting actual rows.
	if _, err := store.DB().Exec(`UPDATE run_events SET sequence = 10 WHERE run_id = 'root-a' AND sequence = 3`); err != nil {
		t.Fatal(err)
	}
	before, err := store.ListEvents(ctx, EventQuery{RunID: "root-a"})
	if err != nil {
		t.Fatal(err)
	}
	full, err := LoadGoalTree(ctx, store, "root-a")
	if err != nil {
		t.Fatal(err)
	}
	var project func(*RunNode) *RunOverview
	project = func(node *RunNode) *RunOverview {
		run := node.Run
		run.Budget, run.Outcome = nil, nil
		result := &RunOverview{Run: run, Status: node.State.Status, EventCount: node.State.EventCount}
		for _, child := range node.Children {
			result.Children = append(result.Children, project(child))
		}
		return result
	}
	got, err := store.SessionRunOverview(ctx, "session")
	if err != nil {
		t.Fatal(err)
	}
	if want := project(full); !reflect.DeepEqual(got, want) {
		t.Fatalf("overview differs from full replay metadata:\n got: %#v\nwant: %#v", got, want)
	}
	if got.Run.RunID != "root-a" || got.EventCount != 3 || got.Status != "paused" ||
		len(got.Children) != 2 || got.Children[0].Run.RunID != "child-a" || len(got.Children[0].Children) != 1 {
		t.Fatalf("root selection, counts, or cross-session hierarchy changed: %+v", got)
	}
	after, err := store.ListEvents(ctx, EventQuery{RunID: "root-a"})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("overview changed canonical replay evidence: %v", err)
	}
}

func TestSQLiteStore_SessionRunOverview_Lifecycle(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		types []string
		want  string
	}{
		{name: "no_events"},
		{name: "unknown_only", types: []string{"future.event"}},
		{name: "started", types: []string{EventRunStarted}, want: "running"},
		{name: "paused", types: []string{EventRunStarted, EventRunPaused}, want: "paused"},
		{name: "resumed", types: []string{EventRunPaused, EventRunResumed}, want: "running"},
		{name: "completed", types: []string{EventRunStarted, EventRunCompleted, "future.event"}, want: "completed"},
		{name: "failed", types: []string{EventRunFailed, "future.event"}, want: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, err := store.StartRun(ctx, AgentRun{SessionID: tc.name})
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range tc.types {
				if _, err := store.Append(ctx, Event{RunID: run.RunID, Type: kind}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := store.SessionRunOverview(ctx, tc.name)
			if err != nil || got == nil || got.Status != tc.want || got.EventCount != len(tc.types) {
				t.Fatalf("overview = %+v, %v; want status %q and %d events", got, err, tc.want, len(tc.types))
			}
		})
	}
	if got, err := store.SessionRunOverview(ctx, "missing"); got != nil || err != nil {
		t.Fatalf("missing session = %+v, %v; want nil, nil", got, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.SessionRunOverview(cancelled, "started"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query = %v", err)
	}
}
