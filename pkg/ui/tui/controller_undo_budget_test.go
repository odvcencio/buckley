package tui

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func undoBudgetPayload(turn int) []byte {
	data := make([]byte, 512<<10)
	for offset := 0; offset < len(data); offset += sha256.Size {
		digest := sha256.Sum256(fmt.Appendf(nil, "%d/%d", turn, offset))
		copy(data[offset:], digest[:])
	}
	return data
}

func TestUndoRetention_DiskBudgetExpiresOldRecordsWithoutLosingCompletedWork(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	file := filepath.Join(dir, "artifact")
	ctrl, app, sess := newUndoTestController(t, dir)
	t.Cleanup(ctrl.closeUndoStores)
	ctrl.cfg = config.DefaultConfig()
	ctrl.cfg.Resources.UndoStorageBytes = 2 << 20
	for turn := 1; turn <= 30; turn++ {
		simulateTurn(t, ctrl, sess, func() {
			if err := os.WriteFile(file, undoBudgetPayload(turn), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	depth := len(sess.undoStack)
	if depth < 1 || depth >= maxUndoDepth || len(sess.Conversation.Messages) != 60 {
		t.Fatalf("depth=%d messages=%d", depth, len(sess.Conversation.Messages))
	}
	for step := 1; step <= depth; step++ {
		ctrl.undoLastTurn()
		got, err := os.ReadFile(file)
		if err != nil || sha256.Sum256(got) != sha256.Sum256(undoBudgetPayload(30-step)) {
			t.Fatalf("undo %d lost retained file snapshot: %v", step, err)
		}
	}
	ctrl.undoLastTurn()
	if text := lastSystemMessage(t, app); !strings.Contains(text, "Nothing to undo") {
		t.Fatalf("expired history was offered as undo: %q", text)
	}
	for range depth {
		ctrl.redoLastTurn()
	}
	if got, err := os.ReadFile(file); err != nil || sha256.Sum256(got) != sha256.Sum256(undoBudgetPayload(30)) || len(sess.Conversation.Messages) != 60 {
		t.Fatal("redo lost completed file or conversation")
	}
	simulateTurn(t, ctrl, sess, func() {
		if err := os.WriteFile(file, append(undoBudgetPayload(31), undoBudgetPayload(32)...), 0600); err != nil {
			t.Fatal(err)
		}
	})
	// This pair still fits; force a larger next turn to expire every undo record.
	simulateTurn(t, ctrl, sess, func() {
		if err := os.WriteFile(file, append(undoBudgetPayload(33), append(undoBudgetPayload(34), undoBudgetPayload(35)...)...), 0600); err != nil {
			t.Fatal(err)
		}
	})
	if len(sess.undoStack) != 0 || len(sess.redoStack) != 0 || len(sess.Conversation.Messages) != 64 {
		t.Fatal("oversized turn did not expire only undo records")
	}
	if text := lastSystemMessage(t, app); !strings.Contains(text, "undo storage budget") {
		t.Fatalf("oversized turn was not explained: %q", text)
	}
	if got, err := os.ReadFile(file); err != nil || len(got) != 1536<<10 {
		t.Fatal("oversized turn lost completed output")
	}
}
