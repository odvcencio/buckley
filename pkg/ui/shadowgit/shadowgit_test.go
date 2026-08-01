package shadowgit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runInit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runInit("init", "--quiet")
	runInit("config", "user.email", "test@example.com")
	runInit("config", "user.name", "Test")
	return dir
}

func writeFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestNew_RejectsNonGitDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := New(dir, "session-1"); err != ErrNotAGitRepo {
		t.Fatalf("New() error = %v, want ErrNotAGitRepo", err)
	}
}

func TestSanitizeRefComponent(t *testing.T) {
	tests := map[string]string{
		"session-1":      "session-1",
		"has spaces":     "has_spaces",
		"weird/../chars": "weird_.._chars",
		"":               "session",
		"...":            "session",
	}
	for input, want := range tests {
		if got := SanitizeRefComponent(input); got != want {
			t.Errorf("SanitizeRefComponent(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWriteTree_ReflectsWorktreeContent(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	store, err := New(dir, "session-1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	tree1, err := store.WriteTree(ctx)
	if err != nil {
		t.Fatalf("WriteTree: %v", err)
	}
	if tree1 == "" {
		t.Fatal("expected a non-empty tree SHA")
	}

	// Writing the same content again should produce the same tree.
	tree1Again, err := store.WriteTree(ctx)
	if err != nil {
		t.Fatalf("WriteTree (again): %v", err)
	}
	if tree1 != tree1Again {
		t.Fatalf("WriteTree not stable: %s != %s", tree1, tree1Again)
	}

	writeFile(t, dir, "a.txt", "hello world")
	tree2, err := store.WriteTree(ctx)
	if err != nil {
		t.Fatalf("WriteTree (changed): %v", err)
	}
	if tree2 == tree1 {
		t.Fatal("expected tree to change after editing a file")
	}
}

func TestWriteTree_DoesNotTouchRealIndex(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	store, err := New(dir, "session-1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if _, err := store.WriteTree(ctx); err != nil {
		t.Fatalf("WriteTree: %v", err)
	}

	realIndex := filepath.Join(dir, ".git", "index")
	if _, err := os.Stat(realIndex); !os.IsNotExist(err) {
		t.Fatalf("expected no real .git/index to be created, stat err = %v", err)
	}
}

func TestCommitTreeAndUpdateRef_RoundTrip(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	store, err := New(dir, "session-1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if sha, err := store.CurrentRef(ctx); err != nil || sha != "" {
		t.Fatalf("CurrentRef before any commit = (%q, %v), want empty", sha, err)
	}

	tree, err := store.WriteTree(ctx)
	if err != nil {
		t.Fatalf("WriteTree: %v", err)
	}
	commit, err := store.CommitTree(ctx, tree, "", "turn 1")
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}
	if err := store.UpdateRef(ctx, commit); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}

	got, err := store.CurrentRef(ctx)
	if err != nil {
		t.Fatalf("CurrentRef: %v", err)
	}
	if got != commit {
		t.Fatalf("CurrentRef = %q, want %q", got, commit)
	}

	gotTree, err := store.CommitTreeSHA(ctx, commit)
	if err != nil {
		t.Fatalf("CommitTreeSHA: %v", err)
	}
	if gotTree != tree {
		t.Fatalf("CommitTreeSHA = %q, want %q", gotTree, tree)
	}

	// The ref must not be a visible branch.
	branchOut, err := exec.Command("git", "-C", dir, "branch", "--list").CombinedOutput()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if len(branchOut) != 0 {
		t.Fatalf("expected no visible branches, got: %s", branchOut)
	}
}

func TestApply_RestoresModifiedAddedAndDeletedFiles(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "keep.txt", "unchanged")
	writeFile(t, dir, "modify.txt", "before")
	writeFile(t, dir, "delete.txt", "will be deleted")
	store, err := New(dir, "session-1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	before, err := store.WriteTree(ctx)
	if err != nil {
		t.Fatalf("WriteTree(before): %v", err)
	}

	// Simulate a turn's edits: modify one file, delete another, add a new one.
	writeFile(t, dir, "modify.txt", "after")
	if err := os.Remove(filepath.Join(dir, "delete.txt")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	writeFile(t, dir, "added.txt", "brand new")

	after, err := store.WriteTree(ctx)
	if err != nil {
		t.Fatalf("WriteTree(after): %v", err)
	}

	clean, err := store.Clean(ctx, after)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if !clean {
		t.Fatal("expected worktree to match the after tree before undo")
	}

	// Undo: restore from after -> before.
	if err := store.Apply(ctx, after, before); err != nil {
		t.Fatalf("Apply(after -> before): %v", err)
	}

	if got := readFile(t, dir, "modify.txt"); got != "before" {
		t.Fatalf("modify.txt = %q, want before", got)
	}
	if got := readFile(t, dir, "delete.txt"); got != "will be deleted" {
		t.Fatalf("delete.txt = %q, want restored content", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "added.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected added.txt to be removed by undo, stat err = %v", err)
	}
	if got := readFile(t, dir, "keep.txt"); got != "unchanged" {
		t.Fatalf("keep.txt = %q, want unchanged (untouched by undo)", got)
	}

	cleanAfterUndo, err := store.Clean(ctx, before)
	if err != nil {
		t.Fatalf("Clean(before): %v", err)
	}
	if !cleanAfterUndo {
		t.Fatal("expected worktree to match the before tree after undo")
	}

	// Redo: restore from before -> after.
	if err := store.Apply(ctx, before, after); err != nil {
		t.Fatalf("Apply(before -> after): %v", err)
	}
	if got := readFile(t, dir, "modify.txt"); got != "after" {
		t.Fatalf("modify.txt after redo = %q, want after", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected delete.txt to be removed again by redo, stat err = %v", err)
	}
	if got := readFile(t, dir, "added.txt"); got != "brand new" {
		t.Fatalf("added.txt after redo = %q, want brand new", got)
	}
}

func TestClean_DetectsDriftFromUntrackedTurn(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "a.txt", "hello")
	store, err := New(dir, "session-1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	tree, err := store.WriteTree(ctx)
	if err != nil {
		t.Fatalf("WriteTree: %v", err)
	}

	// A manual edit not tracked by any turn.
	writeFile(t, dir, "a.txt", "manually edited")

	clean, err := store.Clean(ctx, tree)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if clean {
		t.Fatal("expected Clean to report drift after an untracked manual edit")
	}
}

func TestApply_PreservesExecutableBit(t *testing.T) {
	dir := newTestRepo(t)
	store, err := New(dir, "session-1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	before, err := store.WriteTree(ctx)
	if err != nil {
		t.Fatalf("WriteTree(before): %v", err)
	}

	scriptPath := filepath.Join(dir, "run.sh")
	writeFile(t, dir, "run.sh", "#!/bin/sh\necho hi\n")
	if err := os.Chmod(scriptPath, 0o755); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	after, err := store.WriteTree(ctx)
	if err != nil {
		t.Fatalf("WriteTree(after): %v", err)
	}

	// Undo then redo, and confirm the executable bit survives the round trip.
	if err := store.Apply(ctx, after, before); err != nil {
		t.Fatalf("Apply(undo): %v", err)
	}
	if err := store.Apply(ctx, before, after); err != nil {
		t.Fatalf("Apply(redo): %v", err)
	}

	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("expected run.sh to remain executable after undo/redo, mode = %v", info.Mode())
	}
}

func readFile(t *testing.T, dir, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(data)
}
