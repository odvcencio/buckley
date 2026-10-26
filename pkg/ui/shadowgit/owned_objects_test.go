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
	"strings"
	"testing"
)

func userGitFiles(t *testing.T, repo string) map[string][32]byte {
	t.Helper()
	root := filepath.Join(repo, ".git")
	files := make(map[string][32]byte)
	if err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == filepath.Join(root, "buckley") {
			return filepath.SkipDir
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			files[name] = sha256.Sum256(data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func privateObjectBytes(t *testing.T, store *Store) int64 {
	t.Helper()
	var total int64
	if err := filepath.WalkDir(filepath.Join(store.StorageDir(), "objects"), func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return total
}

func TestPrivateStorage_CollectsExpiredObjectsAndPreservesUserGit(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo, "user.txt", "committed")
	retentionGit(t, repo, "add", ".")
	retentionGit(t, repo, "commit", "-qm", "user baseline")
	writeFile(t, repo, "user.txt", "staged")
	retentionGit(t, repo, "add", ".")
	writeFile(t, repo, "user.txt", "unstaged")
	head := retentionGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, ".git/shallow"), []byte(head+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	original := userGitFiles(t, repo)
	store, err := New(repo, "bounded")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	var trees []string
	var steady int64
	for turn := range 35 {
		data := make([]byte, 0, 4096)
		for block := range 128 {
			digest := sha256.Sum256(fmt.Appendf(nil, "%d/%d", turn, block))
			data = append(data, digest[:]...)
		}
		if err := os.WriteFile(filepath.Join(repo, "artifact"), data, 0600); err != nil {
			t.Fatal(err)
		}
		tree, err := store.WriteTree(ctx)
		if err != nil {
			t.Fatal(err)
		}
		trees = append(trees, tree)
		if len(trees) > 4 {
			trees = trees[1:]
		}
		if err := store.RetainTrees(ctx, trees); err != nil {
			t.Fatal(err)
		}
		bytes := privateObjectBytes(t, store)
		if turn == 10 {
			steady = bytes
		}
		if turn > 10 && bytes > steady+2048 {
			t.Fatalf("private object growth continued: turn=%d bytes=%d steady=%d", turn, bytes, steady)
		}
	}
	if !reflect.DeepEqual(userGitFiles(t, repo), original) {
		t.Fatal("private snapshots or collection changed user Git data")
	}
	if _, err := os.Stat(filepath.Join(store.StorageDir(), "objects/info/alternates")); !os.IsNotExist(err) {
		t.Fatal("private store borrowed user objects")
	}
	for _, tree := range trees {
		retentionPrivateGit(t, store, "cat-file", "-e", tree)
	}
	dir := store.StorageDir()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("closed store left private data: %v", err)
	}
	if !reflect.DeepEqual(userGitFiles(t, repo), original) {
		t.Fatal("closing private storage changed user Git data")
	}
}

func TestPrivateStorage_IgnoresInheritedRepositorySelectors(t *testing.T) {
	repo, foreign := newTestRepo(t), newTestRepo(t)
	writeFile(t, repo, "file", "actual workspace")
	writeFile(t, foreign, "sentinel", "foreign remains unchanged")
	original := userGitFiles(t, foreign)
	t.Setenv("GIT_DIR", filepath.Join(foreign, ".git"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(foreign, ".git"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(foreign, ".git/objects"))
	t.Setenv("GIT_SHALLOW_FILE", filepath.Join(foreign, ".git/shallow"))
	t.Setenv("GIT_NAMESPACE", "foreign")
	store, err := New(repo, "selectors")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tree, err := store.WriteTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.run(ctx, nil, "show", tree+":file"); err != nil || got != "actual workspace" {
		t.Fatalf("wrong repository captured: %q %v", got, err)
	}
	if err := store.RetainTrees(ctx, []string{tree}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(userGitFiles(t, foreign), original) {
		t.Fatal("private collection followed inherited repository selectors")
	}
}

func TestPrivateStorage_PreservesRepositoryAttributesAndFilters(t *testing.T) {
	if _, err := exec.LookPath("tr"); err != nil {
		t.Skip("fixture clean/smudge filter requires tr")
	}
	repo := newTestRepo(t)
	writeFile(t, repo, "base.txt", "base\r\n")
	writeFile(t, repo, "test.filter", "lowercase\n")
	writeFile(t, repo, "ignored.cache", "must stay outside snapshots")
	writeFile(t, repo, ".git/info/exclude", "*.cache\n")
	writeFile(t, repo, ".git/info/attributes", "*.txt text eol=lf\n*.filter filter=upper\n")
	retentionGit(t, repo, "config", "filter.upper.clean", "tr a-z A-Z")
	retentionGit(t, repo, "config", "filter.upper.smudge", "tr A-Z a-z")
	store, err := New(repo, "attributes")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	before := restoreTree(t, store)
	if got := retentionPrivateGit(t, store, "show", before+":base.txt"); got != "base" {
		t.Fatalf("line ending attribute lost: %q", got)
	}
	if got := retentionPrivateGit(t, store, "show", before+":test.filter"); got != "LOWERCASE" {
		t.Fatalf("clean filter lost: %q", got)
	}
	if got := retentionPrivateGit(t, store, "ls-tree", "--name-only", before); strings.Contains(got, "ignored.cache") {
		t.Fatal("user info/exclude was ignored")
	}
	writeFile(t, repo, "test.filter", "modified\n")
	after := restoreTree(t, store)
	if err := store.RetainTrees(ctx, []string{before, after}); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(ctx, after, before); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, repo, "test.filter"); got != "lowercase\n" {
		t.Fatalf("smudge filter lost: %q", got)
	}
}

func TestRetainTrees_EmptyHistoryReleasesPhysicalObjects(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo, "completed", "completed work stays intact")
	store, err := New(repo, "released")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	for _, published := range []bool{false, true} {
		tree := restoreTree(t, store)
		if published {
			if err := store.RetainTrees(ctx, []string{tree}); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.RetainTrees(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if bytes := privateObjectBytes(t, store); bytes != 0 {
			t.Fatalf("published=%v: empty history retained %d object bytes", published, bytes)
		}
		if got := readFile(t, repo, "completed"); got != "completed work stays intact" {
			t.Fatal("releasing undo changed completed work")
		}
	}
}

func TestPrivateStorage_DoesNotRunUserReferenceHooks(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("reference hook fixture requires a POSIX shell")
	}
	repo := newTestRepo(t)
	writeFile(t, repo, "file", "original")
	retentionGit(t, repo, "add", ".")
	retentionGit(t, repo, "commit", "-qm", "user baseline")
	head := retentionGit(t, repo, "rev-parse", "HEAD")
	sentinel := filepath.Join(t.TempDir(), "hook-ran")
	t.Setenv("BUCKLEY_TEST_HOOK_SENTINEL", sentinel)
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.Mkdir(hooks, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooks, "reference-transaction"), []byte("#!/bin/sh\nprintf hook >> \"$BUCKLEY_TEST_HOOK_SENTINEL\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "global-config"))
	retentionGit(t, repo, "config", "--global", "core.hooksPath", hooks)
	retentionGit(t, repo, "update-ref", "refs/user-hook-control", head)
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("control hook did not execute: %v", err)
	}
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}
	before := userGitFiles(t, repo)
	store, err := New(repo, "hooks")
	if err != nil {
		t.Fatal(err)
	}
	tree := restoreTree(t, store)
	if err := store.RetainTrees(context.Background(), []string{tree}); err != nil {
		t.Fatal(err)
	}
	if err := store.RetainTrees(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("internal private ref invoked the user's hook: %v", err)
	}
	if !reflect.DeepEqual(userGitFiles(t, repo), before) {
		t.Fatal("private reference operations changed user Git data")
	}
}

func logUserGitChanges(t *testing.T, before, after map[string][32]byte) {
	t.Helper()
	for name, previous := range before {
		current, exists := after[name]
		if !exists || previous != current {
			t.Logf("user Git metadata changed: path=%q before=%x after=%x exists=%v", name, previous, current, exists)
		}
	}
	for name, current := range after {
		if _, existed := before[name]; !existed {
			t.Logf("user Git metadata added: path=%q sha256=%x", name, current)
		}
	}
}
