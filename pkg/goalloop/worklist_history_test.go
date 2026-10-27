package goalloop

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

func worklistHistory(t testing.TB, count int) (*Loop, *runledger.SQLiteStore, string) {
	t.Helper()
	dir := t.TempDir()
	ev, err := evidence.New(filepath.Join(dir, "shared.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatal(err)
	}
	checkpoints, err := taskstate.NewManager(ledger, ev)
	if err != nil {
		t.Fatal(err)
	}
	loop, err := New(Config{Ledger: ledger, Checkpoints: checkpoints, SessionID: "worklist-history",
		Planner: staticPlanner{specs: []TaskSpec{{Title: "second", Priority: 2}, {Title: "first café 日本語", Priority: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	intake, err := loop.Start(context.Background(), Goal{Statement: "Keep completed work and audit evidence", BudgetUSD: 1})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := ledger.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var last int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(sequence), 0) FROM run_events WHERE run_id = ?`, intake.RunID).Scan(&last); err != nil {
		t.Fatal(err)
	}
	insert, err := tx.Prepare(`INSERT INTO run_events
		(event_id, run_id, sequence, event_type, timestamp, task_id, payload_json, redaction_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer insert.Close()
	for i := 0; i < count; i++ {
		payload, err := json.Marshal(map[string]any{"text": "completed work café 日本語 " + strings.Repeat("x", 512), "ordinal": i})
		if err != nil {
			t.Fatal(err)
		}
		_, err = insert.Exec(runledger.StableEventID(intake.RunID, fmt.Sprint(i)), intake.RunID, last+int64(i)+1,
			"future.audit", "2026-10-27T00:00:00Z", intake.Tasks[0].TaskID, string(payload), "fixture")
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return loop, ledger, intake.RunID
}

func TestLoop_WorklistHistoryBudget(t *testing.T) {
	loop, ledger, runID := worklistHistory(t, 10000)
	ctx := context.Background()
	for _, kind := range []string{"queue", "report"} {
		t.Run(kind, func(t *testing.T) {
			read := func() {
				if kind == "queue" {
					queue, err := loop.BuildQueue(ctx, runID)
					if err != nil || len(queue) != 2 || queue[0].Title != "first café 日本語" {
						t.Fatalf("queue changed: %+v, %v", queue, err)
					}
				} else {
					report, err := loop.Report(ctx, runID)
					if err != nil || len(report.Spend) != 2 || report.Statement != "Keep completed work and audit evidence" {
						t.Fatalf("report changed: %+v, %v", report, err)
					}
				}
			}
			read()
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			read()
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)
			allocated := after.TotalAlloc - before.TotalAlloc
			t.Logf(`{"operation":%q,"audit_events":10000,"allocated_bytes":%d,"elapsed_ns":%d}`, kind, allocated, elapsed.Nanoseconds())
			if allocated > 8<<20 {
				t.Errorf("%s allocated %d bytes; live worklists must not load unrelated audit bodies", kind, allocated)
			}
		})
	}
	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID, Types: []string{"future.audit"}})
	if err != nil || len(events) != 10000 || events[len(events)-1].Payload["text"] != "completed work café 日本語 "+strings.Repeat("x", 512) {
		t.Fatalf("complete audit evidence changed: count=%d, error=%v", len(events), err)
	}
}

func BenchmarkLoop_WorklistHistory(b *testing.B) {
	for _, kind := range []string{"queue", "report"} {
		b.Run(kind, func(b *testing.B) {
			for _, count := range []int{1000, 10000} {
				b.Run(fmt.Sprint(count), func(b *testing.B) {
					loop, _, runID := worklistHistory(b, count)
					ctx := context.Background()
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if kind == "queue" {
							if _, err := loop.BuildQueue(ctx, runID); err != nil {
								b.Fatal(err)
							}
						} else if _, err := loop.Report(ctx, runID); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
