package commands

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestCollectCanopyReviewBoundsInheritedPipeWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell script")
	}
	helper := filepath.Join(t.TempDir(), "canopy")
	script := "#!/bin/sh\n(sleep 5) &\nwait\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	t.Setenv("CANOPY_BIN", helper)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, status := collectCanopyReview(ctx, t.TempDir(), "HEAD")
	if status != "timed out" {
		t.Fatalf("status = %q, want timed out", status)
	}
	if elapsed := time.Since(started); elapsed > canopyPipeWaitDelay+time.Second {
		t.Fatalf("collectCanopyReview returned after %s", elapsed)
	}
}
