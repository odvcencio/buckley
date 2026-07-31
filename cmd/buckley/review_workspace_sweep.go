package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// reviewWorkspaceTempPrefix matches the directories model.PrepareReviewWorkspace
// creates under the OS temp directory (os.MkdirTemp("", "buckley-codex-review-*"))
// for one review's captured, read-only snapshot.
const reviewWorkspaceTempPrefix = "buckley-codex-review-"

// staleReviewWorkspaceMaxAge is how long a captured review workspace may sit
// in the OS temp directory before a review command sweeps it as abandoned.
// A workspace this old almost always means its owning process was killed,
// crashed, or timed out before its deferred cleanup ran.
const staleReviewWorkspaceMaxAge = 24 * time.Hour

// sweepStaleReviewWorkspaces removes buckley-codex-review-* directories left
// behind in the OS temp directory by a review whose deferred cleanup never
// ran. It is best-effort and bounded to that one prefix: a failure to list
// or remove an entry is logged and never blocks the review command that
// calls it at startup.
func sweepStaleReviewWorkspaces() {
	root := os.TempDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		slog.Warn("Could not scan temp directory for stale review workspaces", "dir", root, "error", err)
		return
	}

	cutoff := time.Now().Add(-staleReviewWorkspaceMaxAge)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), reviewWorkspaceTempPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			// The entry may have been removed concurrently; nothing to sweep.
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}

		path := filepath.Join(root, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("Could not remove stale review workspace", "path", path, "error", err)
			continue
		}
		slog.Info("Removed stale review workspace", "path", path, "age", time.Since(info.ModTime()).Round(time.Minute))
	}
}
