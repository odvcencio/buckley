package execmode

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunner_ReusesBuildCacheAcrossScratchDirectories(t *testing.T) {
	if DetectIsolation() != IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	workspace := t.TempDir()
	sink := &recordingSink{}
	runner, err := NewRunner(workspace, sink, DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	const source = `package main
import ("fmt"; "runtime"; "execprogram/caps")
func main() {
	text, _, err := caps.ReadFile("input.txt")
	if err != nil { panic(err) }
	_, file, _, _ := runtime.Caller(0)
	fmt.Print(file, ":", text)
}`
	cacheEntries := func() map[string]int64 {
		t.Helper()
		entries := make(map[string]int64)
		err := filepath.WalkDir(sharedGoCache(), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), "-d") {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			entries[path] = info.Size()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return entries
	}
	run := func(program, content, suffix string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(workspace, "input.txt"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		result, err := runner.Run(ctx, program)
		if err != nil || result.ExitCode != 0 || result.Stdout != "execprogram/main.go:"+content+suffix {
			t.Fatalf("Run: %+v, err=%v", result, err)
		}
	}
	run(source, "first", "")
	warm := cacheEntries()
	if len(warm) == 0 {
		t.Fatal("no cached build artifacts")
	}
	run(source, "fresh", "")
	if got := cacheEntries(); !reflect.DeepEqual(got, warm) {
		t.Fatalf("identical source added build artifacts: before=%d after=%d", len(warm), len(got))
	}
	changed := strings.Replace(source, `text)`, `text + "!")`, 1)
	run(changed, "changed", "!")
	changedCache := cacheEntries()
	if reflect.DeepEqual(changedCache, warm) {
		t.Fatal("changed source did not produce new build artifacts")
	}
	run(changed, "fresh again", "!")
	if got := cacheEntries(); !reflect.DeepEqual(got, changedCache) {
		t.Fatalf("changed source did not reuse its build artifacts: before=%d after=%d", len(changedCache), len(got))
	}
	if calls := sink.byMethod(CapFilesRead); len(calls) != 4 {
		t.Fatalf("audited %d reads, want 4", len(calls))
	}
}
