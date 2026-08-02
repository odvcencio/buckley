package tui

import (
	"context"
	"fmt"

	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/ui/shadowgit"
)

// maxUndoDepth caps how many turns /undo can walk back, bounding both
// memory (retained message slices) and the shadow-git ref's commit chain.
const maxUndoDepth = 20

// turnBoundary is the state beginTurnUndo captures at the start of a turn,
// consumed by finishTurnUndo once the turn (streaming, tool loop) settles.
type turnBoundary struct {
	store          *shadowgit.Store
	beforeTree     string
	messagesBefore int
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
	sess.redoStack = nil
	c.mu.Unlock()

	store := c.sessionUndoStore(sess)
	boundary := turnBoundary{store: store, messagesBefore: messagesBefore}
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
// the post-turn worktree, chains both trees onto the session's shadow-git
// ref for reachability, and pushes an undo record when the turn appended
// any conversation messages.
func (c *Controller) finishTurnUndo(sess *SessionState, boundary turnBoundary) {
	if boundary.store == nil || boundary.beforeTree == "" {
		return
	}
	ctx := context.Background()
	afterTree, err := boundary.store.WriteTree(ctx)
	if err != nil {
		return
	}

	c.mu.Lock()
	messagesAfter := len(sess.Conversation.Messages)
	if messagesAfter <= boundary.messagesBefore {
		c.mu.Unlock()
		return
	}
	messages := append([]conversation.Message(nil), sess.Conversation.Messages[boundary.messagesBefore:]...)
	c.mu.Unlock()

	parent, _ := boundary.store.CurrentRef(ctx)
	beforeCommit, err := boundary.store.CommitTree(ctx, boundary.beforeTree, parent, "buckley-undo: before turn")
	if err != nil {
		return
	}
	afterCommit, err := boundary.store.CommitTree(ctx, afterTree, beforeCommit, "buckley-undo: after turn")
	if err != nil {
		return
	}
	if err := boundary.store.UpdateRef(ctx, afterCommit); err != nil {
		return
	}

	record := turnUndoRecord{
		beforeTree:     boundary.beforeTree,
		afterTree:      afterTree,
		messagesBefore: boundary.messagesBefore,
		messages:       messages,
	}

	c.mu.Lock()
	sess.undoStack = append(sess.undoStack, record)
	if len(sess.undoStack) > maxUndoDepth {
		sess.undoStack = sess.undoStack[len(sess.undoStack)-maxUndoDepth:]
	}
	c.mu.Unlock()
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
