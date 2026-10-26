package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/runledger"
)

func sessionNavHistory(t testing.TB, count int) (*runledger.SQLiteStore, string) {
	t.Helper()
	store, err := runledger.New(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	run, err := store.StartRun(context.Background(), runledger.AgentRun{
		SessionID: "session-history", AgentID: "primary", ModelID: "fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	insert, err := tx.Prepare(`INSERT INTO run_events
		(event_id, run_id, sequence, event_type, timestamp, task_id, payload_json, redaction_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer insert.Close()
	for i := 0; i < count+2; i++ {
		kind := "future.tool.observed"
		if i == 0 {
			kind = runledger.EventRunStarted
		} else if i == count/2+1 {
			kind = runledger.EventRunPaused
		}
		payload, err := json.Marshal(map[string]any{
			"text":    "completed work café 日本語 " + strings.Repeat("x", 512),
			"ordinal": i,
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = insert.Exec(runledger.StableEventID(run.RunID, fmt.Sprint(i)), run.RunID, i+1, kind,
			run.StartedAt.Format(time.RFC3339Nano), "task-history", string(payload), "fixture")
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return store, run.RunID
}

func TestBuildRunTreeNav_HistoryBudget(t *testing.T) {
	store, runID := sessionNavHistory(t, 10000)
	ctx := context.Background()
	// Warm SQLite's fixed-size page cache before measuring refresh allocations.
	if _, _, ok := buildRunTreeNav(ctx, store, "session-history"); !ok {
		t.Fatal("warm-up could not load navigation")
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	nodes, byID, ok := buildRunTreeNav(ctx, store, "session-history")
	elapsed := time.Since(start)
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(nodes)
	runtime.KeepAlive(byID)
	if !ok || len(nodes) != 1 || nodes[0].ID != runID || nodes[0].Status != "paused" {
		t.Fatalf("navigation lost its event-derived status: %+v", nodes)
	}
	record := runNodeToActivityRecord(byID[runID])
	if !strings.Contains(record.Detail, "Events: 10002\n") || record.Status != "paused" {
		t.Fatalf("inspector lost the full event count or status: %+v", record)
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf(`{"events":10002,"allocated_bytes":%d,"retained_bytes":%d,"elapsed_ns":%d}`,
		allocated, int64(after.HeapAlloc)-int64(before.HeapAlloc), elapsed.Nanoseconds())
	if allocated > 8<<20 {
		t.Fatalf("navigator allocated %d bytes for one run; event payload history must stay out of navigation", allocated)
	}
	events, err := store.ListEvents(ctx, runledger.EventQuery{RunID: runID})
	if err != nil || len(events) != 10002 {
		t.Fatalf("full replay history changed: %d events, %v", len(events), err)
	}
	if events[len(events)-1].Payload["text"] != "completed work café 日本語 "+strings.Repeat("x", 512) {
		t.Fatal("navigation changed retained replay evidence")
	}
}

func BenchmarkBuildRunTreeNav_History(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			store, _ := sessionNavHistory(b, count)
			ctx := context.Background()
			if _, _, ok := buildRunTreeNav(ctx, store, "session-history"); !ok {
				b.Fatal("warm-up could not load navigation")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				nodes, byID, ok := buildRunTreeNav(ctx, store, "session-history")
				if !ok || len(nodes) != 1 || len(byID) != 1 {
					b.Fatal("navigation was not loaded")
				}
				runtime.KeepAlive(nodes)
				runtime.KeepAlive(byID)
			}
		})
	}
}
