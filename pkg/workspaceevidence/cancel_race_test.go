package workspaceevidence

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Cancelling the context while a directory is being read must end the read
// with an error. It must not close the directory out from under ReadDir, which
// panicked in CI with a nil directory-state dereference.
func TestReadDirectoryManifest_CancelWhileReading(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 4000; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for i := 0; i < 300; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		go func(delay time.Duration) {
			time.Sleep(delay)
			cancel()
		}(time.Duration(i%50) * 20 * time.Microsecond)
		_, _ = readDirectoryManifest(ctx, root, 1_000_000, 1<<30)
		cancel()
	}
}
