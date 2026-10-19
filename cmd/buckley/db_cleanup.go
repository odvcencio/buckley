package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/retention"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/storage"
)

func runDBCleanup(args []string) error {
	cfg, err := loadConfiguredConfig()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("db cleanup", flag.ContinueOnError)
	dbFlag := fs.String("db", "", "Database path (default: session database and goal ledger)")
	dryRun := fs.Bool("dry-run", false, "Show history eligible for cleanup without deleting it")
	vacuum := fs.Bool("vacuum", false, "Reclaim legacy SQLite free space and enable incremental vacuum")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: buckley db cleanup [--db path] [--dry-run] [--vacuum]")
	}
	if *dryRun && *vacuum {
		return fmt.Errorf("--vacuum cannot be combined with --dry-run")
	}
	paths := []string{strings.TrimSpace(*dbFlag)}
	if paths[0] == "" {
		primary, err := resolveDBPath()
		if err != nil {
			return err
		}
		ledger, err := resolveLedgerDBPath()
		if err != nil {
			return err
		}
		paths = []string{primary}
		if ledger != primary {
			paths = append(paths, ledger)
		}
	}
	for _, path := range paths {
		path, err = expandHomePath(path)
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if *dbFlag != "" {
				return err
			}
			continue
		} else if err != nil {
			return err
		}
		if err := cleanupDatabase(path, historyRetentionPolicy(cfg.Retention), *dryRun, *vacuum); err != nil {
			return err
		}
	}
	return nil
}

func cleanupDatabase(path string, policy retention.Policy, dryRun, vacuum bool) error {
	// Opening evidence alone preserves a standalone goal ledger's schema.
	store, err := evidence.New(path, evidence.WithBlobRoot(filepath.Join(filepath.Dir(path), "evidence")))
	if err != nil {
		return err
	}
	defer store.Close()
	if _, err := runledger.NewWithDB(store.DB()); err != nil {
		return err
	}
	cleaner, err := retention.New(store.DB(), store)
	if err != nil {
		return err
	}
	report, err := cleaner.Clean(context.Background(), policy, time.Now(), dryRun)
	if err != nil {
		return err
	}
	if vacuum {
		if err := storage.ReclaimSQLite(context.Background(), store.DB(), true); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Database string `json:"database"`
		retention.Report
	}{path, report})
}
