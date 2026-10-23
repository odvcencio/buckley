package main

import (
	"context"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/storage"
)

func pressureFixture(t *testing.T, limit int64) (*evidence.SQLiteStore, *runledger.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.New(filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ev, err := evidence.NewWithDB(store.DB(), filepath.Join(dir, "evidence"))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.ConfigureStorageBudget(context.Background(), ev.DB(), limit); err != nil {
		t.Fatal(err)
	}
	return ev, ledger
}

func addPressureRun(t *testing.T, ev *evidence.SQLiteStore, ledger *runledger.SQLiteStore, id string, at time.Time, size int) {
	t.Helper()
	ctx := context.Background()
	noise := make([]byte, size) // incompressible, so stored size equals size
	_, _ = rand.New(rand.NewSource(int64(len(id)) + int64(size))).Read(noise)
	if _, err := ledger.StartRun(ctx, runledger.AgentRun{RunID: id, SessionID: "s-" + id, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindToolResult, MediaType: "text/plain", CreatedAt: at,
		InlineBody: noise, Metadata: map[string]any{evidence.MetaRunID: id}}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.EndRun(ctx, id, "completed", at, nil); err != nil {
		t.Fatal(err)
	}
}

// Past 80% of the budget, completed runs older than the pressure age and the
// evidence only they referenced are pruned; newer history stays.
func TestPrunePastBudgetThreshold_PrunesOldHistoryOnlyUnderPressure(t *testing.T) {
	now := time.Now().UTC()
	cfg := config.DefaultConfig()
	cfg.Retention.HistoryTTL = 90 * 24 * time.Hour // the regular rule would keep 20-day-old runs
	cfg.Retention.EphemeralEvidenceTTL = 90 * 24 * time.Hour

	// Plenty of room: nothing is pruned.
	ev, ledger := pressureFixture(t, 512<<20)
	addPressureRun(t, ev, ledger, "old", now.Add(-20*24*time.Hour), 200<<10)
	report, pruned, err := prunePastBudgetThreshold(context.Background(), cfg, ev, now, false)
	if err != nil || pruned {
		t.Fatalf("pruned without pressure: %+v pruned=%v err=%v", report, pruned, err)
	}

	// Near the limit: the old run goes, the recent one stays.
	ev, ledger = pressureFixture(t, 16<<20)
	addPressureRun(t, ev, ledger, "old", now.Add(-20*24*time.Hour), 3<<20)
	addPressureRun(t, ev, ledger, "recent", now.Add(-2*24*time.Hour), 3<<20)
	addPressureRun(t, ev, ledger, "fill", now.Add(-1*time.Hour), 8<<20)
	budget, _ := storage.ReadStorageBudget(context.Background(), ev.DB())
	if budget.UsedBytes*10 < budget.LimitBytes*8 {
		t.Fatalf("fixture is not past 80%%: %+v", budget)
	}
	report, pruned, err = prunePastBudgetThreshold(context.Background(), cfg, ev, now, false)
	if err != nil || !pruned || report.Runs != 1 || report.Evidence != 1 {
		t.Fatalf("report=%+v pruned=%v err=%v", report, pruned, err)
	}
	var runs int
	if err := ev.DB().QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE run_id IN ('recent','fill')`).Scan(&runs); err != nil || runs != 2 {
		t.Fatalf("recent history lost: %d err=%v", runs, err)
	}
	after, _ := storage.ReadStorageBudget(context.Background(), ev.DB())
	if after.UsedBytes >= budget.UsedBytes {
		t.Fatalf("usage did not fall: before %d after %d", budget.UsedBytes, after.UsedBytes)
	}

	// pressure_age 0 turns it off even when forced.
	cfg.Retention.PressureAge = 0
	if _, pruned, err := prunePastBudgetThreshold(context.Background(), cfg, ev, now, true); err != nil || pruned {
		t.Fatalf("pressure pruning ran with pressure_age=0: pruned=%v err=%v", pruned, err)
	}
}

func TestPrunePastBudgetThreshold_NoRunTablesIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	ev, err := evidence.New(filepath.Join(dir, "plain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	if err := storage.ConfigureStorageBudget(context.Background(), ev.DB(), 16<<20); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prunePastBudgetThreshold(context.Background(), config.DefaultConfig(), ev, time.Now(), true); err != nil {
		t.Fatalf("evidence-only store: %v", err)
	}
}
