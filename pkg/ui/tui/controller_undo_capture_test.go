package tui

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func undoObjectFileBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	if err := filepath.WalkDir(filepath.Join(root, "objects"), func(_ string, entry fs.DirEntry, err error) error {
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

func TestFinishTurnUndo_CaptureLimitReleasesUnfinishedSnapshots(t *testing.T) {
	for _, oversizedBefore := range []bool{false, true} {
		t.Run(map[bool]string{false: "after turn", true: "before turn"}[oversizedBefore], func(t *testing.T) {
			dir := newUndoTestGitRepo(t)
			ctrl, app, sess := newUndoTestController(t, dir)
			ctrl.cfg.Resources.UndoCaptureBytes = 1 << 20
			t.Cleanup(ctrl.closeUndoStores)
			path := filepath.Join(dir, "artifact")
			data := make([]byte, 2<<20)
			for turn := range 8 {
				if oversizedBefore {
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				simulateTurn(t, ctrl, sess, func() {
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
				})
				if len(sess.undoStack) != 0 || len(sess.Conversation.Messages) != 2*(turn+1) {
					t.Fatal("unsupported snapshot changed completed conversation work")
				}
				if bytes := undoObjectFileBytes(t, sess.undoStore.StorageDir()); bytes != 0 {
					t.Fatalf("turn %d retained %d bytes of unfinished snapshots", turn, bytes)
				}
				if msg := lastSystemMessage(t, app); !strings.Contains(msg, "resources.undo_capture_bytes") {
					t.Fatalf("capture limit was not explained: %s", msg)
				}
				got, err := os.ReadFile(path)
				if err != nil || sha256.Sum256(got) != sha256.Sum256(data) {
					t.Fatal("completed artifact was changed")
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestFinishTurnUndo_CaptureLimitPreservesSupportedHistory(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	path := filepath.Join(dir, "note")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	ctrl, _, sess := newUndoTestController(t, dir)
	ctrl.cfg.Resources.UndoCaptureBytes = 1 << 20
	t.Cleanup(ctrl.closeUndoStores)
	simulateTurn(t, ctrl, sess, func() {
		if err := os.WriteFile(path, []byte("after"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	if len(sess.undoStack) != 1 {
		t.Fatal("supported turn was not recorded")
	}
	prior := append([]turnUndoRecord(nil), sess.undoStack...)
	saved := cloneMessages(sess.Conversation.Messages)
	simulateTurn(t, ctrl, sess, func() {
		if err := os.WriteFile(filepath.Join(dir, "artifact"), make([]byte, 2<<20), 0600); err != nil {
			t.Fatal(err)
		}
	})
	if !reflect.DeepEqual(prior, sess.undoStack) || !reflect.DeepEqual(saved, sess.Conversation.Messages[:len(saved)]) {
		t.Fatal("capture refusal changed supported history")
	}
	if len(sess.Conversation.Messages) != len(saved)+2 {
		t.Fatal("capture refusal lost completed messages")
	}
	if _, err := sess.undoStore.Diff(context.Background(), prior[0].beforeTree, prior[0].afterTree); err != nil {
		t.Fatalf("supported snapshot objects were collected: %v", err)
	}
}
