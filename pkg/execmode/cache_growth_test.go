package execmode

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestRunner_RepeatedProgramCacheGrowth(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a cold Go cache")
	}
	cacheRoot := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheRoot)
	t.Setenv("HOME", cacheRoot)
	t.Setenv("LocalAppData", cacheRoot)
	runner, err := NewRunner(t.TempDir(), &recordingSink{}, DefaultTimeout, WithoutIsolation())
	if err != nil {
		t.Fatal(err)
	}
	var previous int64
	for run := 0; run < 3; run++ {
		result, err := runner.Run(context.Background(), `package main
import ("fmt"; "execprogram/caps")
func main() { entries, err := caps.ListDir("."); fmt.Println(len(entries), err) }
`)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("run %d: %+v, %v", run, result, err)
		}
		var bytes int64
		if err := filepath.WalkDir(sharedGoCache(), func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			info, err := os.Stat(path)
			if err == nil {
				bytes += info.Size()
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		t.Logf("run %d: cache bytes=%d growth=%d duration=%s", run, bytes, bytes-previous, result.Duration)
		if run > 0 && bytes-previous > 64*1024 {
			t.Fatalf("identical program grew the cache by %d bytes", bytes-previous)
		}
		previous = bytes
	}
}
