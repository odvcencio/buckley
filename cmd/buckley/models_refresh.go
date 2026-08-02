package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/model"
)

const defaultModelsDevRefreshTimeout = 15 * time.Second

func runModelsCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley models <refresh> [flags]")
	}
	switch strings.TrimSpace(args[0]) {
	case "refresh":
		return runModelsRefreshCommand(args[1:])
	default:
		return fmt.Errorf("usage: buckley models <refresh> [flags]")
	}
}

// runModelsRefreshCommand merges models.dev capability and pricing
// metadata into the local model catalog cache. It is offline-safe: a
// network failure returns a clear error and leaves the existing cache
// file untouched, rather than crashing or writing partial data.
func runModelsRefreshCommand(args []string) error {
	fs := flag.NewFlagSet("models refresh", flag.ContinueOnError)
	url := fs.String("url", model.ModelsDevAPIURL, "models.dev catalog endpoint")
	cachePath := fs.String("cache", "", "Catalog cache path (defaults to BUCKLEY_DATA_DIR or ~/.buckley/model_catalog.json)")
	timeout := fs.Duration("timeout", defaultModelsDevRefreshTimeout, "Network timeout for the models.dev fetch")
	dryRun := fs.Bool("dry-run", false, "Fetch and merge, but do not write the cache file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := strings.TrimSpace(*cachePath)
	if path == "" {
		resolved, err := resolveModelCatalogCachePath()
		if err != nil {
			return err
		}
		path = resolved
	} else {
		expanded, err := expandHomePath(path)
		if err != nil {
			return err
		}
		path = expanded
	}

	existing, err := model.LoadCatalogCache(path)
	if err != nil {
		return fmt.Errorf("load existing catalog cache: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	catalog, err := model.FetchModelsDevCatalog(ctx, http.DefaultClient, *url)
	if err != nil {
		return fmt.Errorf("fetch models.dev catalog: %w", err)
	}

	before := len(existing)
	merged := model.MergeModelsDevCatalog(existing, catalog)
	added, updated := countCatalogChanges(existing, merged)

	if *dryRun {
		fmt.Printf("Dry run: would merge %d models.dev entries into %s (%d new, %d updated, %d unchanged)\n",
			countModelsDevModels(catalog), path, added, updated, before-updated)
		return nil
	}

	if err := model.SaveCatalogCache(path, merged); err != nil {
		return fmt.Errorf("save catalog cache: %w", err)
	}

	fmt.Printf("Merged models.dev catalog into %s: %d new, %d updated, %d total\n", path, added, updated, len(merged))
	return nil
}

func countModelsDevModels(catalog model.ModelsDevCatalog) int {
	n := 0
	for _, provider := range catalog {
		n += len(provider.Models)
	}
	return n
}

// countCatalogChanges compares before and after (the merged result) and
// reports how many IDs are new versus how many existing IDs changed.
func countCatalogChanges(before, after map[string]model.ModelInfo) (added, updated int) {
	for id, info := range after {
		prior, existed := before[id]
		if !existed {
			added++
			continue
		}
		if !reflect.DeepEqual(prior, info) {
			updated++
		}
	}
	return added, updated
}

func resolveModelCatalogCachePath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv(envBuckleyDataDir)); dir != "" {
		dir, err := expandHomePath(dir)
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "model_catalog.json"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".buckley", "model_catalog.json"), nil
}
