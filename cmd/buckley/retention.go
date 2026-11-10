package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/retention"
)

func historyRetentionPolicy(cfg config.RetentionConfig) retention.Policy {
	return retention.Policy{
		HistoryTTL: cfg.HistoryTTL, MaxCompletedSessions: cfg.MaxCompletedSessions, MaxCompletedRuns: cfg.MaxCompletedRuns,
		Evidence: evidence.RetentionPolicy{EphemeralTTL: cfg.EphemeralEvidenceTTL, DurableTTL: cfg.DurableEvidenceTTL, OtherTTL: cfg.EphemeralEvidenceTTL},
	}
}

func startHistoryRetention(ctx context.Context, cfg config.RetentionConfig, store *evidence.SQLiteStore) (func(), error) {
	if !cfg.Enabled {
		return func() {}, nil
	}
	if cfg.Interval <= 0 {
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
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()
		for {
			passCtx, stop := context.WithTimeout(workerCtx, 30*time.Second)
			_, err := cleaner.Clean(passCtx, historyRetentionPolicy(cfg), time.Now(), false)
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
