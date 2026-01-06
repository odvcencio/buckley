package index

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

// Runner coordinates background indexing.
type Runner struct {
	store *storage.Store
	root  string

	mu      sync.Mutex
	running bool

	telemetry *telemetry.Hub
}

// NewRunner constructs a Runner rooted at repository root.
func NewRunner(store *storage.Store, root string, telemetryHub *telemetry.Hub) *Runner {
	return &Runner{
		store:     store,
		root:      root,
		telemetry: telemetryHub,
	}
}

// StartBackground kicks off a background incremental rebuild once.
func (r *Runner) StartBackground(ctx context.Context) {
	go func() {
		if _, err := r.IncrementalRebuild(ctx); err != nil && ctx.Err() == nil {
			log.Printf("[index] background rebuild failed: %v", err)
		}
	}()
}

// Rebuild performs a full scan. Returns error if already running.
func (r *Runner) Rebuild(ctx context.Context) error {
	_, err := r.doRebuild(ctx, false)
	return err
}

// IncrementalRebuild performs an incremental scan, only re-indexing changed files.
// Returns stats about what was indexed. Returns error if already running.
func (r *Runner) IncrementalRebuild(ctx context.Context) (*IndexStats, error) {
	return r.doRebuild(ctx, true)
}

// doRebuild performs either a full or incremental scan.
func (r *Runner) doRebuild(ctx context.Context, incremental bool) (*IndexStats, error) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return nil, fmt.Errorf("index rebuild already in progress")
	}
	r.running = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	root := r.root
	if root == "" {
		if cwd, err := os.Getwd(); err == nil {
			root = cwd
		}
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	r.publishEvent(telemetry.EventIndexStarted, map[string]any{
		"root":        absRoot,
		"incremental": incremental,
	})

	idx := New(r.store, absRoot)
	var stats *IndexStats
	if incremental {
		stats, err = idx.IncrementalScan(ctx)
	} else {
		stats, err = idx.ScanWithStats(ctx)
	}

	if err != nil {
		r.publishEvent(telemetry.EventIndexFailed, map[string]any{
			"root":        absRoot,
			"incremental": incremental,
			"error":       err.Error(),
			"duration_ms": time.Since(start).Milliseconds(),
		})
		return stats, err
	}

	eventData := map[string]any{
		"root":        absRoot,
		"incremental": incremental,
		"duration_ms": time.Since(start).Milliseconds(),
	}
	if stats != nil {
		eventData["files_scanned"] = stats.FilesScanned
		eventData["files_indexed"] = stats.FilesIndexed
		eventData["files_skipped"] = stats.FilesSkipped
		eventData["files_deleted"] = stats.FilesDeleted
	}
	r.publishEvent(telemetry.EventIndexCompleted, eventData)

	return stats, nil
}

func (r *Runner) publishEvent(eventType telemetry.EventType, data map[string]any) {
	if r == nil || r.telemetry == nil {
		return
	}
	payload := map[string]any{}
	for k, v := range data {
		payload[k] = v
	}
	r.telemetry.Publish(telemetry.Event{
		Type: eventType,
		Data: payload,
	})
}
