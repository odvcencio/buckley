package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/storage"
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
	for _, anchor := range []string{"none", "system", "summary"} {
		t.Run(anchor, func(t *testing.T) {
			repo := newUndoTestGitRepo(t)
			ctrl, _, sess := newUndoTestController(t, repo)
			ctrl.cfg = config.DefaultConfig()
			t.Cleanup(ctrl.closeUndoStores)
			if anchor != "none" {
				sess.Conversation.AddSystemMessage("pinned anchor")
				if anchor == "summary" {
					sess.Conversation.Messages[0].IsSummary = true
				}
			}
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
		})
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

func TestUndoRedo_ResumedWindowPreservesCanonicalPrefix(t *testing.T) {
	repo := newUndoTestGitRepo(t)
	ctrl, _, sess := newUndoTestController(t, repo)
	ctrl.cfg = config.DefaultConfig()
	t.Cleanup(ctrl.closeUndoStores)
	store, err := storage.New(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctrl.store = store
	if err := store.CreateSession(&storage.Session{ID: sess.ID, Status: storage.SessionStatusPaused}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	messages := make([]storage.Message, 480)
	for i := range messages {
		messages[i] = storage.Message{SessionID: sess.ID, Role: "assistant", Content: fmt.Sprintf("saved %d", i), Timestamp: now.Add(time.Duration(i)), Tokens: i + 1}
	}
	messages[0].Role = "system"
	messages[1].Role = "user"
	if err := store.ReplaceMessages(sess.ID, messages); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetAllMessages(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	embedding := []byte{1, 2, 3, 4}
	if err := store.SaveMessageEmbedding(t.Context(), before[len(before)-1].ID, embedding); err != nil {
		t.Fatal(err)
	}
	if err := sess.Conversation.LoadWorkingSet(store, sessionWorkingMemoryBytes(ctrl.cfg)); err != nil {
		t.Fatal(err)
	}
	boundary := ctrl.beginTurnUndo(sess)
	for _, text := range []string{"new task", "new result"} {
		sess.Conversation.AddAssistantMessage(text)
		if err := sess.Conversation.SaveMessage(store, sess.Conversation.Messages[len(sess.Conversation.Messages)-1]); err != nil {
			t.Fatal(err)
		}
	}
	ctrl.finishTurnUndo(sess, boundary)
	if len(sess.undoStack) != 1 {
		t.Fatal("resumed turn was not retained")
	}
	for _, direction := range []string{"undo", "redo", "undo"} {
		if direction == "undo" {
			ctrl.undoLastTurn()
		} else {
			ctrl.redoLastTurn()
		}
		after, err := store.GetAllMessages(sess.ID)
		want := 480
		if direction == "redo" {
			want += 2
		}
		if err != nil || len(after) != want {
			t.Fatalf("%s: messages=%d want=%d err=%v", direction, len(after), want, err)
		}
		if !reflect.DeepEqual(after[:480], before) {
			t.Fatalf("%s rewrote the saved prefix", direction)
		}
		var storedEmbedding []byte
		if err := store.DB().QueryRow("SELECT embedding FROM messages WHERE id = ?", before[len(before)-1].ID).Scan(&storedEmbedding); err != nil || !reflect.DeepEqual(storedEmbedding, embedding) {
			t.Fatalf("%s changed attached embedding: %x %v", direction, storedEmbedding, err)
		}
	}
}
