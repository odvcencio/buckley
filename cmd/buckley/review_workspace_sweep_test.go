package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSweepStaleReviewWorkspacesRemovesOnlyOldReviewWorkspaceDirs(t *testing.T) {
	tempRoot := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)

	stale := filepath.Join(tempRoot, reviewWorkspaceTempPrefix+"stale123")
	fresh := filepath.Join(tempRoot, reviewWorkspaceTempPrefix+"fresh456")
	unrelatedDir := filepath.Join(tempRoot, "buckley-other-789")
	staleFile := filepath.Join(tempRoot, reviewWorkspaceTempPrefix+"notadir")

	for _, dir := range []string{stale, fresh, unrelatedDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(staleFile, []byte("not a workspace"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-48 * time.Hour)
	for _, path := range []string{stale, unrelatedDir, staleFile} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	sweepStaleReviewWorkspaces()

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale review workspace was not removed: err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh review workspace was incorrectly removed: %v", err)
	}
	if _, err := os.Stat(unrelatedDir); err != nil {
		t.Fatalf("unrelated old directory was incorrectly removed: %v", err)
	}
	if _, err := os.Stat(staleFile); err != nil {
		t.Fatalf("non-directory entry was incorrectly removed: %v", err)
	}
}

func TestSweepStaleReviewWorkspacesToleratesUnreadableTempDir(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))

	// Best-effort: must not panic or block the caller when the temp
	// directory cannot be scanned.
	sweepStaleReviewWorkspaces()
}
