package tui

import (
	"context"
	"fmt"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/ui/shadowgit"
)

// maxUndoDepth caps how many turns /undo can walk back, bounding both
// memory (retained message slices) and the live handle's retained trees.
const maxUndoDepth = 20

// turnBoundary is the state beginTurnUndo captures at the start of a turn,
// consumed by finishTurnUndo once the turn (streaming, tool loop) settles.
type turnBoundary struct {
	store          *shadowgit.Store
	beforeTree     string
	messagesBefore int
	historyVersion uint64
}

// sessionUndoStore lazily resolves and caches sess's shadow-git store. It
// returns nil, without retrying, once workDir is confirmed not to be a git
// repository, so /undo stays a fast no-op there instead of shelling out to
// git on every turn.
func (c *Controller) sessionUndoStore(sess *SessionState) *shadowgit.Store {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sess.undoStoreChecked {
		return sess.undoStore
	}
	sess.undoStoreChecked = true
	store, err := shadowgit.New(c.workDir, sess.ID)
	if err != nil {
		return nil
	}
	sess.undoStore = store
	return store
}

// beginTurnUndo records the pre-turn message count and worktree tree, and
// clears sess.redoStack: starting a new turn discards any pending redo
// history, matching standard undo/redo branch-discard semantics.
func (c *Controller) beginTurnUndo(sess *SessionState) turnBoundary {
	c.mu.Lock()
	messagesBefore := len(sess.Conversation.Messages)
	historyVersion := sess.historyVersion
	sess.redoStack = nil
	c.mu.Unlock()

	store := c.sessionUndoStore(sess)
	boundary := turnBoundary{store: store, messagesBefore: messagesBefore, historyVersion: historyVersion}
	if store == nil {
		return boundary
	}
	tree, err := store.WriteTree(context.Background())
	if err != nil {
		return boundary
	}
	boundary.beforeTree = tree
	return boundary
}

// finishTurnUndo closes out a turn started by beginTurnUndo: it snapshots
// the post-turn worktree, retains the supported undo/redo trees, and pushes
// an undo record when the turn appended conversation messages.
func (c *Controller) finishTurnUndo(sess *SessionState, boundary turnBoundary) {
	if boundary.store == nil || boundary.beforeTree == "" {
		return
	}
	ctx := context.Background()
	c.mu.Lock()
	if sess.historyVersion != boundary.historyVersion || len(sess.Conversation.Messages) <= boundary.messagesBefore {
		trees := undoTrees(sess.undoStack, sess.redoStack)
		c.mu.Unlock()
		_ = boundary.store.RetainTrees(ctx, trees)
		return
	}
	c.mu.Unlock()
	afterTree, err := boundary.store.WriteTree(ctx)
	if err != nil {
		return
	}

	c.mu.Lock()
	if sess.historyVersion != boundary.historyVersion {
		trees := undoTrees(sess.undoStack, sess.redoStack)
		c.mu.Unlock()
		_ = boundary.store.RetainTrees(ctx, trees)
		return
	}
	messagesAfter := len(sess.Conversation.Messages)
	if messagesAfter <= boundary.messagesBefore {
		trees := undoTrees(sess.undoStack, sess.redoStack)
		c.mu.Unlock()
		_ = boundary.store.RetainTrees(ctx, trees)
		return
	}
	messages := append([]conversation.Message(nil), sess.Conversation.Messages[boundary.messagesBefore:]...)
	record := turnUndoRecord{
		beforeTree:     boundary.beforeTree,
		afterTree:      afterTree,
		messagesBefore: boundary.messagesBefore,
		messages:       messages,
	}

	records := append(append([]turnUndoRecord(nil), sess.undoStack...), record)
	if len(records) > maxUndoDepth {
		excess := len(records) - maxUndoDepth
		clear(records[:excess])
		records = records[excess:]
	}
	limit := sessionWorkingMemoryBytes(c.cfg) / 4
	for limit > 0 && len(records) > 0 && undoMemoryBytes(records) > limit {
		records[0] = turnUndoRecord{}
		records = records[1:]
	}
	trees := undoTrees(records, sess.redoStack)
	c.mu.Unlock()
	if err := boundary.store.RetainTrees(ctx, trees); err != nil {
		return
	}
	c.mu.Lock()
	if sess.historyVersion == boundary.historyVersion {
		sess.undoStack = records
	}
	c.mu.Unlock()
}

func undoTrees(undo, redo []turnUndoRecord) []string {
	trees := make([]string, 0, 2*(len(undo)+len(redo)))
	for _, records := range [][]turnUndoRecord{undo, redo} {
		for _, record := range records {
			trees = append(trees, record.beforeTree, record.afterTree)
		}
	}
	return trees
}

func (c *Controller) closeUndoStores() {
	c.mu.Lock()
	var stores []*shadowgit.Store
	for _, sess := range c.sessions {
		if sess == nil {
			continue
		}
		sess.undoStoreChecked = true
		if sess.undoStore != nil {
			stores = append(stores, sess.undoStore)
		}
	}
	c.mu.Unlock()
	for _, store := range stores {
		_ = store.Close()
	}
}

// undoLastTurn reverts the current session's most recent turn: its
// conversation tail (the messages it appended) and the file changes it
// made. It refuses when the worktree has changes the turn did not make,
// since applying the file-side revert would silently discard them.
func (c *Controller) undoLastTurn() {
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	if sess.Streaming {
		c.mu.Unlock()
		c.app.AddMessage("A response is still running. Use /cancel before undoing.", "system")
		return
	}
	if len(sess.undoStack) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("Nothing to undo.", "system")
		return
	}
	record := sess.undoStack[len(sess.undoStack)-1]
	store := sess.undoStore
	c.mu.Unlock()

	if store == nil {
		c.app.AddMessage("Undo is unavailable: this workspace is not a git repository.", "system")
		return
	}

	ctx := context.Background()
	clean, err := store.Clean(ctx, record.afterTree)
	if err != nil {
		c.app.AddMessage("Could not check worktree state: "+err.Error(), "system")
		return
	}
	if !clean {
		c.app.AddMessage("Worktree has changes this turn did not make; refusing to undo. Commit or stash them first.", "system")
		return
	}
	if err := store.Apply(ctx, record.afterTree, record.beforeTree); err != nil {
		c.app.AddMessage("Undo failed while restoring files: "+err.Error(), "system")
		return
	}

	c.mu.Lock()
	sess.undoStack[len(sess.undoStack)-1] = turnUndoRecord{}
	sess.undoStack = sess.undoStack[:len(sess.undoStack)-1]
	sess.redoStack = append(sess.redoStack, record)
	sess.Conversation.Messages = sess.Conversation.Messages[:record.messagesBefore]
	sess.Conversation.UpdateTokenCount()
	saveErr := (error)(nil)
	if c.store != nil {
		saveErr = sess.Conversation.SaveAllMessages(c.store)
	}
	messages := cloneMessages(sess.Conversation.Messages)
	c.mu.Unlock()

	c.rerenderTranscript(sess, messages)
	if saveErr != nil {
		c.app.AddMessage("Undid last turn, but failed to persist: "+saveErr.Error(), "system")
		return
	}
	c.app.AddMessage(fmt.Sprintf("Undid last turn (%d message(s), file changes reverted).", len(record.messages)), "system")
}

// redoLastTurn re-applies the most recently undone turn.
func (c *Controller) redoLastTurn() {
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	if sess.Streaming {
		c.mu.Unlock()
		c.app.AddMessage("A response is still running. Use /cancel before redoing.", "system")
		return
	}
	if len(sess.redoStack) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("Nothing to redo.", "system")
		return
	}
	record := sess.redoStack[len(sess.redoStack)-1]
	store := sess.undoStore
	c.mu.Unlock()

	if store == nil {
		c.app.AddMessage("Redo is unavailable: this workspace is not a git repository.", "system")
		return
	}

	ctx := context.Background()
	clean, err := store.Clean(ctx, record.beforeTree)
	if err != nil {
		c.app.AddMessage("Could not check worktree state: "+err.Error(), "system")
		return
	}
	if !clean {
		c.app.AddMessage("Worktree has changes since the undo; refusing to redo. Commit or stash them first.", "system")
		return
	}
	if err := store.Apply(ctx, record.beforeTree, record.afterTree); err != nil {
		c.app.AddMessage("Redo failed while restoring files: "+err.Error(), "system")
		return
	}

	c.mu.Lock()
	sess.redoStack[len(sess.redoStack)-1] = turnUndoRecord{}
	sess.redoStack = sess.redoStack[:len(sess.redoStack)-1]
	sess.undoStack = append(sess.undoStack, record)
	sess.Conversation.Messages = append(sess.Conversation.Messages, record.messages...)
	sess.Conversation.UpdateTokenCount()
	saveErr := (error)(nil)
	if c.store != nil {
		saveErr = sess.Conversation.SaveAllMessages(c.store)
	}
	messages := cloneMessages(sess.Conversation.Messages)
	c.mu.Unlock()

	c.rerenderTranscript(sess, messages)
	if saveErr != nil {
		c.app.AddMessage("Redid last turn, but failed to persist: "+saveErr.Error(), "system")
		return
	}
	c.app.AddMessage(fmt.Sprintf("Redid last turn (%d message(s), file changes restored).", len(record.messages)), "system")
}

// rerenderTranscript rebuilds the visible chat transcript from sess's
// current message list, the same way switchToSessionLocked does when
// switching sessions.
func (c *Controller) rerenderTranscript(sess *SessionState, messages []conversation.Message) {
	c.app.ClearScrollback()
	c.app.WelcomeScreen()
	c.app.addMessageImmediately("Session: "+sess.ID, "system")
	renderConversationHistoryImmediately(c.app, messages)
}
