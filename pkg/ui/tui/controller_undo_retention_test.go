package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
)

func undoRetentionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestUndoRetention_DepthAndBranchDiscardSurviveCollection(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("version 0"), 0644); err != nil {
		t.Fatal(err)
	}
	undoRetentionGit(t, dir, "add", ".")
	undoRetentionGit(t, dir, "commit", "-qm", "user baseline")
	head := undoRetentionGit(t, dir, "rev-parse", "HEAD")
	ctrl, _, sess := newUndoTestController(t, dir)
	t.Cleanup(ctrl.closeUndoStores)
	for n := 1; n <= 45; n++ {
		simulateTurn(t, ctrl, sess, func() {
			if err := os.WriteFile(file, []byte(fmt.Sprintf("version %d", n)), 0644); err != nil {
				t.Fatal(err)
			}
		})
	}
	if len(sess.undoStack) != maxUndoDepth || undoRetentionGit(t, dir, "--git-dir="+sess.undoStore.StorageDir(), "rev-list", "--count", sess.undoStore.RefName()) != "1" {
		t.Fatal("retained undo history exceeds its depth")
	}
	oldTipTree := sess.undoStack[len(sess.undoStack)-1].afterTree
	// Collection is only performed inside this test's owned Git repository.
	undoRetentionGit(t, dir, "gc", "--prune=now")
	for n := 44; n >= 25; n-- {
		ctrl.undoLastTurn()
		data, err := os.ReadFile(file)
		if err != nil || string(data) != fmt.Sprintf("version %d", n) {
			t.Fatalf("undo after collection: expected version %d, got %s (%v)", n, data, err)
		}
	}
	if len(sess.undoStack) != 0 || len(sess.redoStack) != maxUndoDepth {
		t.Fatal("undo depth changed")
	}
	undoRetentionGit(t, dir, "gc", "--prune=now")
	for n := 26; n <= 45; n++ {
		ctrl.redoLastTurn()
		data, err := os.ReadFile(file)
		if err != nil || string(data) != fmt.Sprintf("version %d", n) {
			t.Fatalf("redo after collection: expected version %d, got %s (%v)", n, data, err)
		}
	}
	for range 5 {
		ctrl.undoLastTurn()
	}
	simulateTurn(t, ctrl, sess, func() {
		if err := os.WriteFile(file, []byte("new branch"), 0644); err != nil {
			t.Fatal(err)
		}
	})
	if len(sess.redoStack) != 0 {
		t.Fatal("new turn retained discarded redo")
	}
	undoRetentionGit(t, dir, "gc", "--prune=now")
	if clean, err := sess.undoStore.Clean(context.Background(), sess.undoStack[len(sess.undoStack)-1].afterTree); err != nil || !clean {
		t.Fatalf("private collection after branch discard: %v %v", clean, err)
	}
	if exec.Command("git", "--git-dir="+sess.undoStore.StorageDir(), "cat-file", "-e", oldTipTree).Run() == nil {
		t.Fatal("discarded redo snapshot remains pinned")
	}
	if undoRetentionGit(t, dir, "rev-parse", "HEAD") != head {
		t.Fatal("user HEAD changed")
	}
}

func TestUndoRetention_MemoryAndWindowInvalidationReleaseRoots(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	file := filepath.Join(dir, "notes.txt")
	ctrl, _, sess := newUndoTestController(t, dir)
	t.Cleanup(ctrl.closeUndoStores)
	ctrl.cfg = config.DefaultConfig()
	ctrl.cfg.Resources.SessionMemoryBytes = 4096
	simulateTurn(t, ctrl, sess, func() {
		if err := os.WriteFile(file, []byte("completed despite small undo budget"), 0644); err != nil {
			t.Fatal(err)
		}
	})
	if len(sess.undoStack) != 0 || len(sess.Conversation.Messages) != 2 {
		t.Fatal("undo budget changed the completed conversation")
	}
	if ref, _ := sess.undoStore.CurrentRef(context.Background()); ref != "" {
		t.Fatal("memory-evicted undo remains pinned")
	}
	ctrl.cfg = config.DefaultConfig()
	simulateTurn(t, ctrl, sess, func() {
		if err := os.WriteFile(file, []byte("supported turn"), 0644); err != nil {
			t.Fatal(err)
		}
	})
	boundary := ctrl.beginTurnUndo(sess)
	sess.Conversation.AddUserMessage("turn whose working view changes")
	if err := os.WriteFile(file, []byte("completed with projected context"), 0644); err != nil {
		t.Fatal(err)
	}
	sess.historyVersion++
	sess.undoStack, sess.redoStack = nil, nil
	ctrl.finishTurnUndo(sess, boundary)
	if ref, _ := sess.undoStore.CurrentRef(context.Background()); ref != "" {
		t.Fatal("invalidated history remains pinned")
	}
	blob := undoRetentionGit(t, dir, "hash-object", file)
	if exec.Command("git", "--git-dir="+sess.undoStore.StorageDir(), "cat-file", "-e", blob).Run() == nil {
		t.Fatal("invalidated turn unnecessarily wrote an after snapshot")
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "completed with projected context" {
		t.Fatal("invalidating undo lost completed file work")
	}
}

func TestCloseUndoStores_PreventsLateStoreCreation(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	ctrl, _, sess := newUndoTestController(t, dir)
	store := ctrl.sessionUndoStore(sess)
	if store == nil {
		t.Fatal("fixture store unavailable")
	}
	lazy := &SessionState{ID: "lazy", Conversation: conversation.New("lazy")}
	ctrl.sessions = append(ctrl.sessions, lazy)
	ctrl.closeUndoStores()
	if err := store.RetainTrees(context.Background(), nil); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed handle accepted late retention: %v", err)
	}
	if ctrl.sessionUndoStore(lazy) != nil {
		t.Fatal("shutdown created a new undo store")
	}
	late := &SessionState{ID: "late", Conversation: conversation.New("late")}
	ctrl.sessions = append(ctrl.sessions, late)
	if ctrl.sessionUndoStore(late) != nil {
		t.Fatal("a session created after shutdown opened a new undo store")
	}
}
