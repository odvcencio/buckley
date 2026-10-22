package execmode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRunner_RuntimeCannotAccessBuildCache(t *testing.T) {
	if DetectIsolation() != IsolationBwrap {
		t.Skip("nested bubblewrap not available")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cache := sharedGoCache()
	poison := filepath.Join(cache, "00", "0000000000000000000000000000000000000000000000000000000000000000-d")
	sink := &recordingSink{}
	runner, err := NewRunner(newWorkspace(t), sink, DefaultTimeout)
	if err != nil {
		t.Fatal(err)
	}
	source := fmt.Sprintf(`package main
import("fmt";"os";"path/filepath";"strings";"execprogram/caps")
func main() {
	_, readErr := os.ReadFile(%q)
	writeErr := os.WriteFile(%q, []byte("cache-poison"), 0600)
	_, procErr := os.ReadFile(%q)
	scratchErr := os.WriteFile(filepath.Join(os.Getenv("HOME"), "scratch.txt"), []byte("ok"), 0600)
	executable, err := os.Executable(); if err != nil { panic(err) }
	linkErr := os.Link(executable, filepath.Join(os.Getenv("HOME"), "binary-link"))
	file, selfErr := os.OpenFile(executable, os.O_WRONLY, 0); if selfErr == nil { file.Close() }
	input, _, err := caps.ReadFile("greeting.txt"); if err != nil { panic(err) }
	fmt.Printf("cache_read_denied=%%v cache_write_denied=%%v proc_cache_denied=%%v scratch_write=%%v binary_link_denied=%%v binary_write_denied=%%v input=%%s\n", readErr != nil, writeErr != nil, procErr != nil, scratchErr == nil, linkErr != nil, selfErr != nil, strings.TrimSpace(input))
}`, filepath.Join(cache, "README"), poison, filepath.Join("/proc/1/root", cache, "README"))
	for _, name := range []string{"normal", "quoted_tmp"} {
		t.Run(name, func(t *testing.T) {
			if name == "quoted_tmp" {
				dir, err := os.MkdirTemp("", "q '$() ; ")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(dir) })
				t.Setenv("TMPDIR", dir)
			}
			result, err := runner.Run(context.Background(), source)
			want := "cache_read_denied=true cache_write_denied=true proc_cache_denied=true scratch_write=true binary_link_denied=true binary_write_denied=true input=hello from the workspace\n"
			if err != nil || result.ExitCode != 0 || result.Stdout != want {
				t.Fatalf("Run: %+v err=%v", result, err)
			}
			if _, err := os.Stat(poison); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("runtime mutated host cache: %v", err)
			}
		})
	}
	if calls := sink.byMethod(CapFilesRead); len(calls) != 2 {
		t.Fatalf("audited %d reads, want 2", len(calls))
	}
}
