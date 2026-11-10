package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestStartHistoryRetention_CleansOnStartupAndStopsBeforeStoreClose(t *testing.T) {
	ev, err := evidence.New(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	old := time.Now().Add(-40 * 24 * time.Hour)
	if _, err := ledger.StartRun(ctx, runledger.AgentRun{RunID: "expired", SessionID: "worker", StartedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.EndRun(ctx, "expired", "completed", old, nil); err != nil {
		t.Fatal(err)
	}
	stop, err := startHistoryRetention(ctx, config.DefaultConfig().Retention, ev)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := ledger.GetRun(ctx, "expired")
		if errors.Is(err, runledger.ErrNotFound) {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("startup cleanup did not complete: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	// Stop is idempotent, and no cleanup worker remains attached to the DB.
	stop()
}
