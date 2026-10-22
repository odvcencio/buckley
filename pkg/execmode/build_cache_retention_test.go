package execmode

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cacheFixtureEntry(t *testing.T, dir string, id int, size int, executable bool) string {
	t.Helper()
	name := fmt.Sprintf("%064x-d", id)
	path := filepath.Join(dir, name[:2], name)
	if executable {
		path = filepath.Join(path, "execprogram")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0600); err != nil {
		t.Fatal(err)
	}
	if executable {
		path = filepath.Dir(path)
	}
	stamp := time.Unix(int64(id), 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTrimBuildCache_Budgets(t *testing.T) {
	for _, tc := range []struct {
		name        string
		files, size int
		bytes       int64
		entries     int
	}{
		{"bytes", 4, 10, 25, 8},
		{"entries", 6, 0, 100, 4},
		{"bounded_inventory", 30, 10, 100, 3},
		{"below_budget", 2, 5, 100, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for i := 0; i < tc.files; i++ {
				cacheFixtureEntry(t, dir, i+1, tc.size, false)
			}
			if err := trimBuildCache(dir, tc.bytes, tc.entries); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			entries, more, err := scanBuildCache(root, tc.entries+1)
			if err != nil {
				t.Fatal(err)
			}
			var size int64
			for _, entry := range entries {
				size += entry.bytes
			}
			if more || len(entries) > tc.entries || size > tc.bytes {
				t.Fatalf("entries=%d more=%v bytes=%d", len(entries), more, size)
			}
			if tc.name == "bytes" {
				for _, id := range []int{3, 4} {
					if _, err := os.Stat(filepath.Join(dir, "00", fmt.Sprintf("%064x-d", id))); err != nil {
						t.Fatal("newest artifacts evicted", err)
					}
				}
			}
		})
	}
}

func TestTrimBuildCache_PrefersExecutablesAndRejectsInvalidEntries(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	keep := cacheFixtureEntry(t, dir, 1, 15, false)
	drop := cacheFixtureEntry(t, dir, 2, 20, true)
	outsideFile := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(outsideFile, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "ab")); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(dir, "00", fmt.Sprintf("%064x-d", 3))); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "unknown", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := trimBuildCache(dir, 20, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("hot archive removed", err)
	}
	if _, err := os.Stat(drop); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("executable not evicted: %v", err)
	}
	if b, err := os.ReadFile(outsideFile); err != nil || string(b) != "preserve" {
		t.Fatalf("outside file changed: %q %v", b, err)
	}
	for _, path := range []string{"ab", "unknown", filepath.Join("00", fmt.Sprintf("%064x-d", 3))} {
		if _, err := os.Lstat(filepath.Join(dir, path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid entry remains %s: %v", path, err)
		}
	}
}

func TestBuildCacheLease_SubprocessHelper(t *testing.T) {
	dir := os.Getenv("EXECMODE_CACHE_LEASE_TEST_DIR")
	if dir == "" {
		t.Skip("subprocess only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lease, err := acquireBuildCache(ctx, dir, 5, 8)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "ready")
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCacheLease_RejectsSymlinkRoots(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(fmt.Sprint(parent), func(t *testing.T) {
			base := t.TempDir()
			outside := t.TempDir()
			path := filepath.Join(base, "cache")
			dir := path
			if parent {
				dir = filepath.Join(path, "gocache")
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Skip(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if lease, err := acquireBuildCache(ctx, dir, 5, 8); err == nil {
				lease.Close()
				t.Fatal("accepted symlink cache root")
			}
		})
	}
}

func TestBuildCacheLease_ConcurrentProcessAndCancellation(t *testing.T) {
	if DetectIsolation() != IsolationBwrap {
		t.Skip("real sandbox and file locks required")
	}
	dir := filepath.Join(t.TempDir(), "gocache")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := acquireBuildCache(ctx, dir, 5, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.users.Close() })
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestBuildCacheLease_SubprocessHelper$")
	cmd.Env = append(os.Environ(), "EXECMODE_CACHE_LEASE_TEST_DIR="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("child: %q %v", ready, err)
	}
	path := cacheFixtureEntry(t, dir, 1, 10, false)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("active process lost input", err)
	}
	waitCtx, stop := context.WithTimeout(ctx, 40*time.Millisecond)
	_, err = acquireBuildCache(waitCtx, dir, 5, 8)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission did not cancel: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("waiting admission evicted active input", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("last lease did not trim: %v", err)
	}
	lease, err := acquireBuildCache(ctx, dir, 5, 8)
	if err != nil {
		t.Fatal("lease leaked after cancellation", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}
