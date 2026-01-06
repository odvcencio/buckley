package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/orchestrator"
)

type batchCoordinator interface {
	CleanupWorkspaces(ctx context.Context, olderThan time.Duration) (int, error)
}

var batchLoadConfigFn = config.Load
var batchNewCoordinatorFn = func(cfg config.BatchConfig) (batchCoordinator, error) {
	return orchestrator.NewBatchCoordinator(cfg, nil)
}

func runBatchCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley batch <subcommand>")
	}
	switch args[0] {
	case "prune-workspaces":
		return runBatchPruneWorkspaces(args[1:])
	default:
		return fmt.Errorf("unknown batch subcommand %s", args[0])
	}
}

func runBatchPruneWorkspaces(args []string) error {
	fs := flag.NewFlagSet("batch prune-workspaces", flag.ContinueOnError)
	olderThan := fs.Duration("older-than", 4*time.Hour, "Delete task PVCs older than this duration")
	force := fs.Bool("force", false, "Run even when batch.enabled is false (dangerous)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := batchLoadConfigFn()
	if err != nil {
		return withExitCode(err, 2)
	}
	if !cfg.Batch.Enabled {
		if !*force {
			return withExitCode(fmt.Errorf("batch is disabled (set batch.enabled=true or BUCKLEY_BATCH_ENABLED=1; use --force to override)"), 2)
		}
		cfg.Batch.Enabled = true
	}
	coordinator, err := batchNewCoordinatorFn(cfg.Batch)
	if err != nil {
		return err
	}
	if coordinator == nil {
		return fmt.Errorf("batch coordinator unavailable; check configuration")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	deleted, err := coordinator.CleanupWorkspaces(ctx, *olderThan)
	if err != nil {
		return err
	}
	fmt.Printf("Removed %d task workspace PVCs older than %s\n", deleted, olderThan.String())
	return nil
}
