package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
)

func TestUndoRedo_RecentTurnsSurviveWorkingWindowTrims(t *testing.T) {
	repo := newUndoTestGitRepo(t)
	ctrl, _, sess := newUndoTestController(t, repo)
	ctrl.cfg = config.DefaultConfig()
	t.Cleanup(ctrl.closeUndoStores)
	file := filepath.Join(repo, "state.txt")
	if err := os.WriteFile(file, []byte("initial"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := range 250 {
		sess.Conversation.AddAssistantMessage(fmt.Sprintf("old %d", i))
	}
	for turn := range 40 {
		boundary := ctrl.beginTurnUndo(sess)
		sess.Conversation.AddUserMessage(fmt.Sprintf("turn %d", turn))
		if err := os.WriteFile(file, []byte(fmt.Sprint(turn)), 0600); err != nil {
			t.Fatal(err)
		}
		for step := range 6 {
			sess.Conversation.AddAssistantMessage(fmt.Sprintf("step %d", step))
		}
		ctrl.mu.Lock()
		err := ctrl.boundSessionWorkingSetLocked(sess)
		ctrl.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		sess.Conversation.AddAssistantMessage(fmt.Sprintf("done %d", turn))
		ctrl.finishTurnUndo(sess, boundary)
	}
	if len(sess.undoStack) != maxUndoDepth || len(sess.Conversation.Messages) > 256 {
		t.Fatalf("undo=%d messages=%d", len(sess.undoStack), len(sess.Conversation.Messages))
	}
	for turn := 38; turn >= 19; turn-- {
		ctrl.undoLastTurn()
		data, err := os.ReadFile(file)
		if err != nil || string(data) != fmt.Sprint(turn) {
			t.Fatalf("undo want %d got %q: %v", turn, data, err)
		}
	}
	for turn := 20; turn <= 39; turn++ {
		ctrl.redoLastTurn()
		data, err := os.ReadFile(file)
		if err != nil || string(data) != fmt.Sprint(turn) {
			t.Fatalf("redo want %d got %q: %v", turn, data, err)
		}
	}
	if got := conversation.GetContentAsString(sess.Conversation.Messages[len(sess.Conversation.Messages)-1].Content); got != "done 39" {
		t.Fatalf("latest message = %q", got)
	}
}

func TestFinishTurnUndo_DoesNotRetainPartialOversizedFirstTurn(t *testing.T) {
	repo := newUndoTestGitRepo(t)
	ctrl, _, sess := newUndoTestController(t, repo)
	ctrl.cfg = config.DefaultConfig()
	t.Cleanup(ctrl.closeUndoStores)
	file := filepath.Join(repo, "state.txt")
	boundary := ctrl.beginTurnUndo(sess)
	for i := range 300 {
		sess.Conversation.AddAssistantMessage(fmt.Sprintf("completed %d", i))
	}
	if err := os.WriteFile(file, []byte("completed file"), 0600); err != nil {
		t.Fatal(err)
	}
	ctrl.finishTurnUndo(sess, boundary)
	if len(sess.undoStack) != 0 || len(sess.Conversation.Messages) > 256 {
		t.Fatal("partial turn was retained or memory bound was lost")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "completed file" {
		t.Fatalf("completed work changed: %q %v", data, err)
	}
}

func TestUndoLastTurn_RefusesUnrecordedConversationTail(t *testing.T) {
	repo := newUndoTestGitRepo(t)
	ctrl, app, sess := newUndoTestController(t, repo)
	t.Cleanup(ctrl.closeUndoStores)
	file := filepath.Join(repo, "state.txt")
	simulateTurn(t, ctrl, sess, func() {
		if err := os.WriteFile(file, []byte("completed"), 0600); err != nil {
			t.Fatal(err)
		}
	})
	sess.Conversation.AddUserMessage("later input outside the recorded turn")
	count := len(sess.Conversation.Messages)
	ctrl.undoLastTurn()
	if got := lastSystemMessage(t, app); !strings.Contains(got, "outside the current working context") {
		t.Fatalf("message=%q", got)
	}
	if len(sess.Conversation.Messages) != count {
		t.Fatal("unrecorded input was removed")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "completed" {
		t.Fatalf("completed work changed: %q %v", data, err)
	}
}
