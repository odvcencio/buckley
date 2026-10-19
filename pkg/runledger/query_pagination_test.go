package runledger

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestListRuns_CursorUsesNanosecondsAndSurvivesPruning(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for _, item := range []struct{ id, at, backend, session string }{
		{"a", "2026-08-20T08:00:00.1Z", "worker", "session"},
		{"b", "2026-08-20T10:00:00.12+02:00", "worker", "session"},
		{"c", "2026-08-20T08:00:00.120000001Z", "worker", "session"},
		{"d", "2026-08-20T08:00:00.999999999Z", "worker", "session"},
		{"e", "2026-08-20T08:00:01Z", "worker", "session"},
		{"f", "2026-08-20T08:00:01.000000001Z", "worker", "session"},
		{"g", "2026-08-20T08:00:01.000000001Z", "worker", "session"},
		{"other-backend", "2026-08-20T08:00:02Z", "goal", "session"},
		{"other-session", "2026-08-20T08:00:02Z", "worker", "other"},
	} {
		at, err := time.Parse(time.RFC3339Nano, item.at)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.StartRun(ctx, AgentRun{RunID: item.id, SessionID: item.session, Backend: item.backend, StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		// Exercise legacy timestamp spellings alongside the canonical writer.
		if _, err := store.DB().Exec(`UPDATE agent_runs SET started_at=? WHERE run_id=?`, item.at, item.id); err != nil {
			t.Fatal(err)
		}
	}
	query := RunQuery{SessionID: "session", Backend: "worker", Limit: 2, Order: RunOrderNewestFirst}
	var ids []string
	for page := 0; page < 5; page++ {
		runs, err := store.ListRuns(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) == 0 {
			break
		}
		ids = append(ids, runIDs(runs)...)
		last := runs[len(runs)-1]
		query.BeforeStartedAt, query.BeforeRunID = last.StartedAt, last.RunID
		if page == 0 {
			if _, err := store.DB().Exec(`DELETE FROM agent_runs WHERE run_id=?`, last.RunID); err != nil {
				t.Fatal(err)
			}
			if _, err := store.StartRun(ctx, AgentRun{RunID: "inserted-later", SessionID: "session", Backend: "worker", StartedAt: last.StartedAt.Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if want := []string{"g", "f", "e", "d", "c", "b", "a"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("paged IDs=%v want=%v", ids, want)
	}
}

func TestListRuns_RejectsIncompleteCursor(t *testing.T) {
	store := newTestStore(t)
	for _, query := range []RunQuery{{BeforeRunID: "id"}, {BeforeStartedAt: time.Now()}} {
		if _, err := store.ListRuns(context.Background(), query); err == nil {
			t.Fatal("accepted incomplete run cursor")
		}
	}
}
