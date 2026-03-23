package gts

import (
	"context"
	"fmt"
	"os"
	"time"
)

// IndexCache tracks the gts index freshness and triggers rebuilds.
type IndexCache struct {
	indexPath string
	maxAge    time.Duration
}

// NewIndexCache creates a cache that monitors the index at the given path.
// If maxAge elapses since the index was last modified, EnsureFresh rebuilds it.
func NewIndexCache(indexPath string, maxAge time.Duration) *IndexCache {
	return &IndexCache{
		indexPath: indexPath,
		maxAge:    maxAge,
	}
}

// EnsureFresh checks the index age and rebuilds via gts index if stale or missing.
func (c *IndexCache) EnsureFresh(ctx context.Context, runner *Runner) error {
	if c == nil {
		return nil
	}

	info, err := os.Stat(c.indexPath)
	if err == nil && time.Since(info.ModTime()) < c.maxAge {
		return nil // index exists and is fresh
	}

	// Index missing or stale -- rebuild
	if _, err := runner.Run(ctx, "index"); err != nil {
		return fmt.Errorf("rebuilding gts index: %w", err)
	}
	return nil
}

// IsFresh returns true if the index exists and is within maxAge.
func (c *IndexCache) IsFresh() bool {
	if c == nil {
		return false
	}
	info, err := os.Stat(c.indexPath)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < c.maxAge
}
