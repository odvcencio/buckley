package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/retention"
	"m31labs.dev/buckley/pkg/storage"
)

func historyRetentionPolicy(cfg config.RetentionConfig) retention.Policy {
	return retention.Policy{
		HistoryTTL: cfg.HistoryTTL, MaxCompletedSessions: cfg.MaxCompletedSessions, MaxCompletedRuns: cfg.MaxCompletedRuns,
		Evidence: evidence.RetentionPolicy{EphemeralTTL: cfg.EphemeralEvidenceTTL, DurableTTL: cfg.DurableEvidenceTTL, OtherTTL: cfg.EphemeralEvidenceTTL},
	}
}

func startSessionHistoryMaintenance(ctx context.Context, cfg *config.Config, store *storage.Store) (func(), error) {
	if !cfg.Retention.Enabled && cfg.Resources.StorageBytes == 0 {
		return func() {}, storage.ConfigureStorageBudget(ctx, store.DB(), 0)
	}
	_, evidenceStore, err := openServeDurableStores(store)
	if err != nil {
		return nil, err
	}
	return startHistoryMaintenance(ctx, cfg, evidenceStore)
}

func startHistoryMaintenance(ctx context.Context, cfg *config.Config, store *evidence.SQLiteStore) (func(), error) {
	if err := storage.ConfigureStorageBudget(ctx, store.DB(), cfg.Resources.StorageBytes); err != nil {
		return nil, fmt.Errorf("configure storage budget: %w", err)
	}
	r := cfg.Retention
	if !r.Enabled {
		return func() {}, nil
	}
	if r.Interval <= 0 {
		return nil, fmt.Errorf("retention.interval must be positive")
	}
	cleaner, err := retention.New(store.DB(), store)
	if err != nil {
		return nil, err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(r.Interval)
		defer ticker.Stop()
		for {
			passCtx, stop := context.WithTimeout(workerCtx, 30*time.Second)
			_, err := cleaner.Clean(passCtx, historyRetentionPolicy(r), time.Now(), false)
			stop()
			if err != nil && workerCtx.Err() == nil {
				fmt.Fprintf(os.Stderr, "History cleanup: %v\n", err)
			}
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}
