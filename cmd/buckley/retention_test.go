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
	"m31labs.dev/buckley/pkg/storage"
)

func TestStartHistoryMaintenance_CleansOnStartupAndStopsBeforeStoreClose(t *testing.T) {
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
	stop, err := startHistoryMaintenance(ctx, config.DefaultConfig(), ev)
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

func TestStartSessionHistoryMaintenance_ProtectsResumeAndHonorsOptOut(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	old := time.Now().Add(-40 * 24 * time.Hour)
	for _, id := range []string{"resume", "expired"} {
		if err := store.CreateSession(&storage.Session{ID: id, Status: storage.SessionStatusCompleted, CreatedAt: old, LastActive: old, CompletedAt: &old}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetSessionStatus("resume", storage.SessionStatusActive); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Resources.StorageBytes = 16 << 20
	stop, err := startSessionHistoryMaintenance(context.Background(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(5 * time.Second)
	for {
		session, err := store.GetSession("expired")
		if session == nil && err == nil {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("startup cleanup did not expire completed history: session=%+v err=%v", session, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if session, err := store.GetSession("resume"); err != nil || session == nil || session.Status != storage.SessionStatusActive {
		t.Fatalf("selected resume was pruned: session=%+v err=%v", session, err)
	}
	budget, err := storage.ReadStorageBudget(context.Background(), store.DB())
	if err != nil || budget.LimitBytes != cfg.Resources.StorageBytes {
		t.Fatalf("composed budget=%+v err=%v", budget, err)
	}
	cfg.Retention.Enabled = false
	cfg.Resources.StorageBytes = 0
	stop, err = startSessionHistoryMaintenance(context.Background(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	budget, err = storage.ReadStorageBudget(context.Background(), store.DB())
	if err != nil || budget.LimitBytes != 0 {
		t.Fatalf("explicit budget opt-out ignored: %+v err=%v", budget, err)
	}
}
