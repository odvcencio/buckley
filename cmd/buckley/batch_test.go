package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
)

type fakeBatchPruneCoordinator struct {
	called  bool
	deleted int
}

func (f *fakeBatchPruneCoordinator) CleanupWorkspaces(_ context.Context, _ time.Duration) (int, error) {
	f.called = true
	return f.deleted, nil
}

func TestRunBatchPruneWorkspacesRequiresEnabledBatchConfig(t *testing.T) {
	oldLoad := batchLoadConfigFn
	oldNewCoordinator := batchNewCoordinatorFn
	t.Cleanup(func() {
		batchLoadConfigFn = oldLoad
		batchNewCoordinatorFn = oldNewCoordinator
	})

	batchLoadConfigFn = func() (*config.Config, error) {
		cfg := config.DefaultConfig()
		cfg.Batch.Enabled = false
		return cfg, nil
	}
	batchNewCoordinatorFn = func(_ config.BatchConfig) (batchCoordinator, error) {
		t.Fatalf("expected batch coordinator not to be created when batch.enabled=false")
		return nil, nil
	}

	err := runBatchPruneWorkspaces(nil)
	if err == nil {
		t.Fatal("expected error when batch.enabled=false")
	}
	if got := exitCodeForError(err); got != 2 {
		t.Fatalf("exitCode=%d want 2", got)
	}
	if !strings.Contains(err.Error(), "batch is disabled") {
		t.Fatalf("expected disabled message, got %q", err.Error())
	}
}

func TestRunBatchPruneWorkspacesForceOverridesDisabledConfig(t *testing.T) {
	oldLoad := batchLoadConfigFn
	oldNewCoordinator := batchNewCoordinatorFn
	t.Cleanup(func() {
		batchLoadConfigFn = oldLoad
		batchNewCoordinatorFn = oldNewCoordinator
	})

	fake := &fakeBatchPruneCoordinator{deleted: 3}
	var coordinatorConfig config.BatchConfig

	batchLoadConfigFn = func() (*config.Config, error) {
		cfg := config.DefaultConfig()
		cfg.Batch.Enabled = false
		return cfg, nil
	}
	batchNewCoordinatorFn = func(cfg config.BatchConfig) (batchCoordinator, error) {
		coordinatorConfig = cfg
		return fake, nil
	}

	out := captureStdout(t, func() {
		if err := runBatchPruneWorkspaces([]string{"--force", "--older-than", "1h"}); err != nil {
			t.Fatalf("runBatchPruneWorkspaces: %v", err)
		}
	})

	if !coordinatorConfig.Enabled {
		t.Fatalf("expected coordinator config to be enabled when --force is set")
	}
	if !fake.called {
		t.Fatalf("expected coordinator CleanupWorkspaces to be called")
	}
	if !strings.Contains(out, "Removed 3 task workspace PVCs") {
		t.Fatalf("expected output to include removal count, got %q", out)
	}
}
