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

// pressureThreshold is the share of resources.storage_bytes past which a store
// is pruned early.
const pressureThresholdPercent = 80

// prunePastBudgetThreshold prunes completed runs and unreferenced evidence
// older than retention.pressure_age when the store has used at least 80% of its
// storage budget (or always when force is set). It reports whether it ran. A
// database without run history tables prunes evidence only.
func prunePastBudgetThreshold(ctx context.Context, cfg *config.Config, store *evidence.SQLiteStore, now time.Time, force bool) (retention.Report, bool, error) {
	age := cfg.Retention.PressureAge
	if age <= 0 {
		return retention.Report{}, false, nil
	}
	budget, err := storage.ReadStorageBudget(ctx, store.DB())
	if err != nil || budget.LimitBytes == 0 {
		return retention.Report{}, false, err
	}
	if !force && budget.UsedBytes*100 < budget.LimitBytes*pressureThresholdPercent {
		return retention.Report{}, false, nil
	}
	policy := historyRetentionPolicy(cfg.Retention)
	policy.HistoryTTL = age
	policy.Evidence.EphemeralTTL, policy.Evidence.OtherTTL = age, age
	var hasRuns bool
	if err := store.DB().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'agent_runs')`).Scan(&hasRuns); err != nil {
		return retention.Report{}, false, err
	}
	if !hasRuns {
		n, err := store.SweepCount(ctx, policy.Evidence, now)
		return retention.Report{Evidence: n}, n > 0, err
	}
	cleaner, err := retention.New(store.DB(), store)
	if err != nil {
		return retention.Report{}, false, err
	}
	report, err := cleaner.Clean(ctx, policy, now, false)
	return report, err == nil, err
}
