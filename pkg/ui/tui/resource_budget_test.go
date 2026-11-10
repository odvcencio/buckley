package tui

import (
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/storage"
)

func TestController_ResourceBudgetBoundsQueuedInputAndInvalidatesTrimmedUndo(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Resources.SessionMemoryBytes = 128 << 10
	c := &Controller{cfg: cfg}
	sess := &SessionState{Conversation: conversation.New("bounded")}
	sess.Conversation.AddUserMessage("original task")
	for range 100 {
		sess.Conversation.AddAssistantMessage(strings.Repeat("x", 4096))
	}
	sess.undoStack = []turnUndoRecord{{messages: append([]conversation.Message(nil), sess.Conversation.Messages...)}}
	sess.redoStack = sess.undoStack
	if err := c.boundSessionWorkingSetLocked(sess); err != nil {
		t.Fatal(err)
	}
	if sess.historyVersion != 1 || len(sess.undoStack) != 0 || len(sess.redoStack) != 0 || sess.Conversation.WorkingSetBytes() > cfg.Resources.SessionMemoryBytes/2 {
		t.Fatal("trim retained stale undo data or exceeded working allocation")
	}
	for range 30 {
		if err := c.checkQueueMemory(sess, strings.Repeat("q", 1024)); err != nil {
			break
		}
		sess.MessageQueue = append(sess.MessageQueue, QueuedMessage{Content: strings.Repeat("q", 1024)})
	}
	if err := c.checkQueueMemory(sess, strings.Repeat("q", 1024)); !errors.Is(err, storage.ErrSessionMemoryBudget) {
		t.Fatalf("full queue admission=%v", err)
	}
	if queuedMessageBytes(sess.MessageQueue) > cfg.Resources.SessionMemoryBytes/8 {
		t.Fatal("queue exceeded its allocation")
	}
}
