package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/v2/pkg/config"
	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/fluffyui/backend/sim"
)

func newUndoTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--quiet")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	return dir
}

func newUndoTestController(t *testing.T, workDir string) (*Controller, *WidgetApp, *SessionState) {
	t.Helper()
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.backend.Fini)
	sess := &SessionState{ID: "undo-session-1", Conversation: conversation.New("undo-session-1")}
	ctrl := &Controller{
		app:      app,
		cfg:      &config.Config{},
		workDir:  workDir,
		sessions: []*SessionState{sess},
	}
	return ctrl, app, sess
}

// simulateTurn runs a fake assistant turn: it begins the undo boundary,
// mutates the conversation and the file at relPath, and closes out the
// turn snapshot, mirroring what streamResponse does around runToolLoop.
func simulateTurn(t *testing.T, ctrl *Controller, sess *SessionState, mutate func()) {
	t.Helper()
	boundary := ctrl.beginTurnUndo(sess)
	sess.Conversation.AddUserMessage("do the thing")
	mutate()
	sess.Conversation.AddAssistantMessage("done")
	ctrl.finishTurnUndo(sess, boundary)
}

func drainAllMessages(app *WidgetApp) []Message {
	var out []Message
	for {
		select {
		case msg := <-app.messages:
			out = append(out, msg)
		default:
			return out
		}
	}
}

func lastSystemMessage(t *testing.T, app *WidgetApp) string {
	t.Helper()
	msgs := drainAllMessages(app)
	for i := len(msgs) - 1; i >= 0; i-- {
		if add, ok := msgs[i].(AddMessageMsg); ok {
			return add.Content
		}
	}
	t.Fatal("expected an AddMessageMsg in the queue")
	return ""
}

func TestUndoLastTurn_RevertsFileAndConversation(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	filePath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(filePath, []byte("original"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctrl, app, sess := newUndoTestController(t, dir)

	simulateTurn(t, ctrl, sess, func() {
		if err := os.WriteFile(filePath, []byte("edited by turn"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	})

	if len(sess.undoStack) != 1 {
		t.Fatalf("undoStack len = %d, want 1", len(sess.undoStack))
	}
	if len(sess.Conversation.Messages) != 2 {
		t.Fatalf("Messages len = %d, want 2", len(sess.Conversation.Messages))
	}

	ctrl.undoLastTurn()

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "original" {
		t.Fatalf("file content = %q, want original", string(data))
	}
	if len(sess.Conversation.Messages) != 0 {
		t.Fatalf("Messages len after undo = %d, want 0", len(sess.Conversation.Messages))
	}
	if len(sess.undoStack) != 0 {
		t.Fatalf("undoStack len after undo = %d, want 0", len(sess.undoStack))
	}
	if len(sess.redoStack) != 1 {
		t.Fatalf("redoStack len after undo = %d, want 1", len(sess.redoStack))
	}

	if got := lastSystemMessage(t, app); !strings.Contains(got, "Undid last turn") {
		t.Fatalf("last message = %q, want undo confirmation", got)
	}
}

func TestRedoLastTurn_RestoresFileAndConversation(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	filePath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(filePath, []byte("original"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctrl, app, sess := newUndoTestController(t, dir)

	simulateTurn(t, ctrl, sess, func() {
		if err := os.WriteFile(filePath, []byte("edited by turn"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	})
	ctrl.undoLastTurn()
	drainAllMessages(app)

	ctrl.redoLastTurn()

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "edited by turn" {
		t.Fatalf("file content after redo = %q, want edited by turn", string(data))
	}
	if len(sess.Conversation.Messages) != 2 {
		t.Fatalf("Messages len after redo = %d, want 2", len(sess.Conversation.Messages))
	}
	if len(sess.undoStack) != 1 {
		t.Fatalf("undoStack len after redo = %d, want 1", len(sess.undoStack))
	}
	if len(sess.redoStack) != 0 {
		t.Fatalf("redoStack len after redo = %d, want 0", len(sess.redoStack))
	}

	if got := lastSystemMessage(t, app); !strings.Contains(got, "Redid last turn") {
		t.Fatalf("last message = %q, want redo confirmation", got)
	}
}

func TestUndoLastTurn_RefusesOnDirtyWorktree(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	filePath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(filePath, []byte("original"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctrl, app, sess := newUndoTestController(t, dir)

	simulateTurn(t, ctrl, sess, func() {
		if err := os.WriteFile(filePath, []byte("edited by turn"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	})
	drainAllMessages(app)

	// A manual edit the turn did not make.
	otherPath := filepath.Join(dir, "manual.txt")
	if err := os.WriteFile(otherPath, []byte("manual change"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ctrl.undoLastTurn()

	if got := lastSystemMessage(t, app); !strings.Contains(got, "refusing to undo") {
		t.Fatalf("last message = %q, want refusal", got)
	}
	if len(sess.undoStack) != 1 {
		t.Fatal("expected the undo record to remain after a refused undo")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "edited by turn" {
		t.Fatalf("file content = %q, want unchanged (refused undo)", string(data))
	}
}

func TestUndoLastTurn_NothingToUndo(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	ctrl, app, _ := newUndoTestController(t, dir)

	ctrl.undoLastTurn()

	if got := lastSystemMessage(t, app); got != "Nothing to undo." {
		t.Fatalf("last message = %q, want 'Nothing to undo.'", got)
	}
}

func TestRedoLastTurn_NothingToRedo(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	ctrl, app, _ := newUndoTestController(t, dir)

	ctrl.redoLastTurn()

	if got := lastSystemMessage(t, app); got != "Nothing to redo." {
		t.Fatalf("last message = %q, want 'Nothing to redo.'", got)
	}
}

func TestUndoLastTurn_NoActiveSession(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.backend.Fini)
	ctrl := &Controller{app: app, cfg: &config.Config{}}

	ctrl.undoLastTurn()

	if got := lastSystemMessage(t, app); got != "No active session." {
		t.Fatalf("last message = %q, want 'No active session.'", got)
	}
}

func TestUndoLastTurn_RefusesWhileStreaming(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	ctrl, app, sess := newUndoTestController(t, dir)
	sess.Streaming = true
	sess.undoStack = []turnUndoRecord{{}}

	ctrl.undoLastTurn()

	if got := lastSystemMessage(t, app); !strings.Contains(got, "still running") {
		t.Fatalf("last message = %q, want streaming refusal", got)
	}
}

func TestBeginTurnUndo_ClearsRedoStack(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	ctrl, _, sess := newUndoTestController(t, dir)
	sess.redoStack = []turnUndoRecord{{}}

	ctrl.beginTurnUndo(sess)

	if len(sess.redoStack) != 0 {
		t.Fatalf("redoStack len after beginTurnUndo = %d, want 0", len(sess.redoStack))
	}
}

func TestFinishTurnUndo_NoOpWhenNoMessagesAdded(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	ctrl, _, sess := newUndoTestController(t, dir)

	boundary := ctrl.beginTurnUndo(sess)
	ctrl.finishTurnUndo(sess, boundary)

	if len(sess.undoStack) != 0 {
		t.Fatalf("undoStack len = %d, want 0 when the turn added no messages", len(sess.undoStack))
	}
}

func TestSessionUndoStore_NilForNonGitWorkdir(t *testing.T) {
	dir := t.TempDir() // not a git repo
	ctrl, _, sess := newUndoTestController(t, dir)

	store := ctrl.sessionUndoStore(sess)
	if store != nil {
		t.Fatal("expected a nil undo store for a non-git workDir")
	}
	if !sess.undoStoreChecked {
		t.Fatal("expected undoStoreChecked to be set after the lookup")
	}

	// beginTurnUndo/finishTurnUndo should be safe no-ops.
	boundary := ctrl.beginTurnUndo(sess)
	sess.Conversation.AddUserMessage("hi")
	sess.Conversation.AddAssistantMessage("hello")
	ctrl.finishTurnUndo(sess, boundary)

	if len(sess.undoStack) != 0 {
		t.Fatalf("undoStack len = %d, want 0 without a git repo", len(sess.undoStack))
	}
}

func TestUndoStore_DoesNotCreateVisibleBranches(t *testing.T) {
	dir := newUndoTestGitRepo(t)
	filePath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(filePath, []byte("original"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ctrl, _, sess := newUndoTestController(t, dir)

	simulateTurn(t, ctrl, sess, func() {
		if err := os.WriteFile(filePath, []byte("edited"), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	})

	out, err := exec.CommandContext(context.Background(), "git", "-C", dir, "branch", "--list").CombinedOutput()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if len(strings.TrimSpace(string(out))) != 0 {
		t.Fatalf("expected no visible branches, got: %s", out)
	}

	refOut, err := exec.CommandContext(context.Background(), "git", "-C", dir, "for-each-ref", "refs/buckley/undo").CombinedOutput()
	if err != nil {
		t.Fatalf("git for-each-ref: %v", err)
	}
	if !strings.Contains(string(refOut), "refs/buckley/undo/undo-session-1") {
		t.Fatalf("expected the hidden undo ref to exist, got: %s", refOut)
	}
}
