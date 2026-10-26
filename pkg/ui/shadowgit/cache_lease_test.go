package shadowgit

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func leasedTestStore(t *testing.T, repo, name string) *Store {
	t.Helper()
	store, err := New(repo, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.lease == nil {
		t.Skip("platform has no cache file locks")
	}
	return store
}

func abandonTestStore(t *testing.T, store *Store) {
	t.Helper()
	// A hard exit closes the parent's descriptor without explicitly unlocking
	// the open-file description still inherited by a Git child.
	if err := store.lease.file.Close(); err != nil {
		t.Fatal(err)
	}
	store.lease.file = nil
}

func cacheFiles(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	result := make(map[string][32]byte)
	if err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		var data []byte
		if entry.Type()&os.ModeSymlink != 0 {
			link, err := os.Readlink(name)
			if err != nil {
				return err
			}
			data = []byte(link)
		} else {
			data, err = os.ReadFile(name)
			if err != nil {
				return err
			}
		}
		result[name] = sha256.Sum256(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCacheLease_PreservesOldPausedOwnerAndItsUndo(t *testing.T) {
	repo := newTestRepo(t)
	store := leasedTestStore(t, repo, "paused")
	writeFile(t, repo, "file", "before")
	before := restoreTree(t, store)
	writeFile(t, repo, "file", "after")
	after := restoreTree(t, store)
	if err := store.RetainTrees(context.Background(), []string{before, after}); err != nil {
		t.Fatal(err)
	}
	original := cacheFiles(t, store.StorageDir())
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(store.StorageDir(), cacheOwnerFile), old, old); err != nil {
		t.Fatal(err)
	}
	leasedTestStore(t, repo, "other owner")
	if !reflect.DeepEqual(original, cacheFiles(t, store.StorageDir())) {
		t.Fatal("opening another store changed a paused owner's cache")
	}
	if err := store.Apply(context.Background(), after, before); err != nil || readFile(t, repo, "file") != "before" {
		t.Fatalf("paused undo did not survive cleanup: %v", err)
	}
}

func TestCacheLease_ReapsAbandonedObjectsAndInterruptedIndexes(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo, "user", "user work")
	original := userGitFiles(t, repo)
	store := leasedTestStore(t, repo, "abandoned")
	tree := restoreTree(t, store)
	if err := store.RetainTrees(context.Background(), []string{tree}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index-123", "index-123.lock", "config.lock", "HEAD.lock", store.RefName() + ".lock"} {
		if err := os.WriteFile(filepath.Join(store.StorageDir(), name), []byte("interrupted staging"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	dir := store.StorageDir()
	abandonTestStore(t, store)
	leasedTestStore(t, repo, "reaper")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("abandoned cache remains: %v", err)
	}
	if !reflect.DeepEqual(original, userGitFiles(t, repo)) || readFile(t, repo, "user") != "user work" {
		t.Fatal("orphan cleanup changed user work or Git metadata")
	}
}

func TestCacheLease_ReapsPartialInitializationAndLargeObjectDirectory(t *testing.T) {
	repo := newTestRepo(t)
	parent := filepath.Join(repo, ".git", "buckley", "undo")
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(parent, "handle-")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := newCacheLease(dir, "refs/worktree/buckley/undo-live/partial/initialized")
	if err != nil {
		t.Fatal(err)
	}
	if lease == nil {
		t.Skip("platform has no cache file locks")
	}
	t.Cleanup(lease.close)
	if err := os.MkdirAll(filepath.Join(dir, "objects", "aa"), 0700); err != nil {
		t.Fatal(err)
	}
	for n := range 500 {
		name := filepath.Join(dir, "objects", "aa", fmt.Sprintf("%038x", n))
		if err := os.WriteFile(name, []byte("partial object bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"config.lock", "index-789.lock"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("interrupted init"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	lease.close()
	leasedTestStore(t, repo, "recover-partial")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("partial initialization was not reclaimed: %v", err)
	}
}

func TestCacheLease_ReapsPackedCaptureWithoutReadingRepositoryConfig(t *testing.T) {
	repo := newTestRepo(t)
	retentionGit(t, repo, "config", "core.bigFileThreshold", "1")
	store := leasedTestStore(t, repo, "packed")
	data := budgetPayload(7, 256<<10)
	if err := os.WriteFile(filepath.Join(repo, "artifact"), data, 0600); err != nil {
		t.Fatal(err)
	}
	restoreTree(t, store)
	packs, err := filepath.Glob(filepath.Join(store.StorageDir(), "objects", "pack", "*.pack"))
	if err != nil || len(packs) == 0 {
		t.Fatalf("streamed capture did not create a pack: %v", err)
	}
	// Cleanup must not execute configuration from an abandoned bare repository.
	if err := os.WriteFile(filepath.Join(store.StorageDir(), "config"), []byte("invalid interrupted repository configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := store.StorageDir()
	abandonTestStore(t, store)
	leasedTestStore(t, repo, "reap-packed")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("packed cache was not reclaimed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(repo, "artifact"))
	if err != nil || sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatal("packed cleanup changed completed file")
	}
}

func TestCacheLease_PreservesUnknownCachesAndContinuesRecovery(t *testing.T) {
	for _, scenario := range []string{"legacy", "malformed", "foreign platform", "unexpected file", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			repo := newTestRepo(t)
			store := leasedTestStore(t, repo, "preserve")
			writeFile(t, repo, "file", "completed")
			restoreTree(t, store)
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("keep external data"), 0600); err != nil {
				t.Fatal(err)
			}
			abandonTestStore(t, store)
			marker := filepath.Join(store.StorageDir(), cacheOwnerFile)
			switch scenario {
			case "legacy":
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(marker, []byte("not an ownership marker"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign platform":
				if err := os.WriteFile(marker, []byte("buckley-undo-v1\nforeign\n"+store.RefName()+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unexpected file":
				if err := os.WriteFile(filepath.Join(store.StorageDir(), "user-note"), []byte("preserve unexpected data"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(filepath.Join(store.StorageDir(), "objects"), filepath.Join(outside, "objects")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(store.StorageDir(), "objects")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			before, external := cacheFiles(t, store.StorageDir()), cacheFiles(t, outside)
			// Create a second eligible orphan without triggering constructor cleanup.
			other := leasedTestStore(t, repo, "eligible orphan")
			eligible := other.StorageDir()
			restoreTree(t, other)
			abandonTestStore(t, other)
			leasedTestStore(t, repo, "sweep")
			if !reflect.DeepEqual(before, cacheFiles(t, store.StorageDir())) || !reflect.DeepEqual(external, cacheFiles(t, outside)) {
				t.Fatal("cleanup changed an unknown cache or linked external data")
			}
			if _, err := os.Stat(eligible); !os.IsNotExist(err) {
				t.Fatalf("unknown cache blocked eligible orphan cleanup: %v", err)
			}
		})
	}
}

func TestCacheLease_GitChildProtectsCacheAfterOwnerExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX clean filter")
	}
	repo := newTestRepo(t)
	store := leasedTestStore(t, repo, "git-child")
	control := t.TempDir()
	ready, release := filepath.Join(control, "ready"), filepath.Join(control, "release")
	script := filepath.Join(control, "filter")
	body := "#!/bin/sh\n: > \"$BUCKLEY_LEASE_READY\"\nwhile [ ! -e \"$BUCKLEY_LEASE_RELEASE\" ]; do sleep .02; done\ncat\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUCKLEY_LEASE_READY", ready)
	t.Setenv("BUCKLEY_LEASE_RELEASE", release)
	retentionGit(t, repo, "config", "filter.lease.clean", "'"+strings.ReplaceAll(script, "'", "'\\''")+"'")
	writeFile(t, repo, ".gitattributes", "*.hold filter=lease\n")
	writeFile(t, repo, "payload.hold", "completed source")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := store.command(ctx, []string{"GIT_INDEX_FILE=" + filepath.Join(store.StorageDir(), "index-456")}, "add", "--all", "--", ".")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Git clean filter did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	dir := store.StorageDir()
	abandonTestStore(t, store)
	other := leasedTestStore(t, repo, "other")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("active Git child's cache was reaped: %v", err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	leasedTestStore(t, repo, "after-child")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("finished Git child's cache was not reclaimed: %v", err)
	}
	if _, err := os.Stat(other.StorageDir()); err != nil {
		t.Fatal("cleanup removed a live unrelated owner")
	}
	if readFile(t, repo, "payload.hold") != "completed source" {
		t.Fatal("cleanup changed the source file")
	}
}
