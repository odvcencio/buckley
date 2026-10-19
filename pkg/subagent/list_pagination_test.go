package subagent

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestCoordinator_ListCursorStableAcrossCacheEvictionAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runs.db")
	ev, err := evidence.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) { return "finding", nil }), 1)
	t.Cleanup(func() { _ = manager.Close() })
	c := NewCoordinator(manager, WithRunLedger(ledger), WithEvidence(ev), withCompletedRunCacheLimits(2, 1<<20))
	started := time.Date(2026, 8, 20, 8, 0, 0, 120000001, time.UTC)
	c.now = func() time.Time { return started }
	for _, id := range []string{"c", "a", "e", "b", "d"} {
		if _, err := c.Spawn(ctx, agentcoord.TaskSpec{RunID: id, ParentSessionID: "session", Task: "full task"}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Wait(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ledger.StartRun(ctx, runledger.AgentRun{RunID: "unrelated", SessionID: "session", Backend: "goal", StartedAt: started.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	filter := agentcoord.RunFilter{ParentSessionID: "session", Limit: 2, NewestFirst: true}
	first, err := c.List(ctx, filter)
	if err != nil || len(first) != 2 || first[0].ID != "e" || first[1].ID != "d" || !first[1].StartedAt.Equal(started) {
		t.Fatalf("warm page=%+v err=%v", first, err)
	}
	filter.BeforeStartedAt, filter.BeforeRunID = first[1].StartedAt, first[1].ID
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ev.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := evidence.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	ledger, err = runledger.NewWithDB(reopened.DB())
	if err != nil {
		t.Fatal(err)
	}
	cold := NewCoordinator(nil, WithRunLedger(ledger), WithEvidence(reopened))
	ids := []string{first[0].ID, first[1].ID}
	for page := 0; page < 3; page++ {
		runs, err := cold.List(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 0 {
			break
		}
		for _, run := range runs {
			ids = append(ids, run.ID)
			if run.Task.Task != "full task" || run.Result.Summary != "finding" {
				t.Fatalf("paged projection lost full contract/result: %+v", run)
			}
		}
		last := runs[len(runs)-1]
		filter.BeforeStartedAt, filter.BeforeRunID = last.StartedAt, last.ID
	}
	if want := []string{"e", "d", "c", "b", "a"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("IDs=%v want=%v", ids, want)
	}
}

func TestCoordinator_ListLocalCursor(t *testing.T) {
	manager := NewManager(runnerFunc(func(context.Context, Request, func(int)) (string, error) { return "done", nil }), 1)
	t.Cleanup(func() { _ = manager.Close() })
	c := NewCoordinator(manager)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if _, err := c.Spawn(ctx, agentcoord.TaskSpec{RunID: id, ParentSessionID: "session", Task: "inspect"}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Wait(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	page, err := c.List(ctx, agentcoord.RunFilter{ParentSessionID: "session", NewestFirst: true, Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != "c" {
		t.Fatalf("local page=%+v err=%v", page, err)
	}
	next, err := c.List(ctx, agentcoord.RunFilter{ParentSessionID: "session", NewestFirst: true, Limit: 1, BeforeStartedAt: page[0].StartedAt, BeforeRunID: page[0].ID})
	if err != nil || len(next) != 1 || next[0].ID != "b" {
		t.Fatalf("local next page=%+v err=%v", next, err)
	}
}
