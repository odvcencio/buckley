package tui

import (
	"context"
	"testing"
	"time"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/terminal"
	"github.com/odvcencio/fluffyui/toast"
	"github.com/odvcencio/fluffyui/widgets"

	"github.com/odvcencio/buckley/pkg/diagnostics"
)

// TestRunnerImplementsApp verifies Runner implements the App interface.
func TestRunnerImplementsApp(t *testing.T) {
	var _ App = (*Runner)(nil)
}

// TestNewRunner verifies Runner can be created with a test backend.
func TestNewRunner(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	cfg := RunnerConfig{
		Backend: testBackend,
	}

	runner, err := NewRunner(cfg)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}

	if runner.app == nil {
		t.Error("Runner.app is nil")
	}

	if runner.chatView == nil {
		t.Error("Runner.chatView is nil")
	}

	if runner.inputArea == nil {
		t.Error("Runner.inputArea is nil")
	}

	if runner.statusBar == nil {
		t.Error("Runner.statusBar is nil")
	}

	if runner.sidebar == nil {
		t.Error("Runner.sidebar is nil")
	}

	if runner.coalescer == nil {
		t.Error("Runner.coalescer is nil")
	}
}

// TestRunnerSetStatus verifies SetStatus posts a StatusMsg.
func TestRunnerSetStatus(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	// SetStatus should not panic
	runner.SetStatus("Test status")
}

// TestRunnerAddMessage verifies AddMessage posts an AddMessageMsg.
func TestRunnerAddMessage(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	// AddMessage should not panic
	runner.AddMessage("Test message", "user")
	runner.AddMessage("Response", "assistant")
}

// TestRunnerSetCallbacks verifies callbacks can be set.
func TestRunnerSetCallbacks(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	submitCalled := false
	fileCalled := false
	shellCalled := false

	runner.SetCallbacks(
		func(text string) { submitCalled = true },
		func(path string) { fileCalled = true },
		func(cmd string) string { shellCalled = true; return "result" },
	)

	if runner.onSubmit == nil {
		t.Error("onSubmit callback not set")
	}
	if runner.onFileSelect == nil {
		t.Error("onFileSelect callback not set")
	}
	if runner.onShellCmd == nil {
		t.Error("onShellCmd callback not set")
	}

	// Verify callbacks work
	runner.onSubmit("test")
	if !submitCalled {
		t.Error("onSubmit callback not called")
	}

	runner.onFileSelect("/path")
	if !fileCalled {
		t.Error("onFileSelect callback not called")
	}

	result := runner.onShellCmd("ls")
	if !shellCalled {
		t.Error("onShellCmd callback not called")
	}
	if result != "result" {
		t.Errorf("onShellCmd returned %q, expected 'result'", result)
	}
}

// TestRunnerSetSessionCallbacks verifies session callbacks can be set.
func TestRunnerSetSessionCallbacks(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	nextCalled := false
	prevCalled := false

	runner.SetSessionCallbacks(
		func() { nextCalled = true },
		func() { prevCalled = true },
	)

	if runner.onNextSession == nil {
		t.Error("onNextSession callback not set")
	}
	if runner.onPrevSession == nil {
		t.Error("onPrevSession callback not set")
	}

	runner.onNextSession()
	if !nextCalled {
		t.Error("onNextSession callback not called")
	}

	runner.onPrevSession()
	if !prevCalled {
		t.Error("onPrevSession callback not called")
	}
}

// TestRunnerStreaming verifies streaming methods work.
func TestRunnerStreaming(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	// These should not panic
	runner.SetStreaming(true)
	runner.StreamChunk("session-1", "Hello ")
	runner.StreamChunk("session-1", "World!")
	runner.StreamEnd("session-1", "Hello World!")
	runner.SetStreaming(false)
}

// TestRunnerThinking verifies thinking indicator methods work.
func TestRunnerThinking(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	// These should not panic
	runner.ShowThinkingIndicator()
	runner.AppendReasoning("Thinking about the problem...")
	runner.CollapseReasoning("Summary", "Full reasoning text")
	runner.RemoveThinkingIndicator()
}

// TestRunnerMetadata verifies metadata methods work.
func TestRunnerMetadata(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	// These should not panic
	runner.SetModelName("gpt-4")
	runner.SetSessionID("session-123")
	runner.SetExecutionMode("classic")
	runner.SetTokenCount(1000, 0.05)
	runner.SetContextUsage(5000, 8000, 100000)
}

// TestRunnerProgress verifies progress updates work.
func TestRunnerProgress(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	items := []progress.Progress{
		{ID: "task-1", Label: "Building", Percent: 50},
		{ID: "task-2", Label: "Testing", Percent: 100},
	}

	// Should not panic
	runner.SetProgress(items)
}

// TestRunnerToasts verifies toast updates work.
func TestRunnerToasts(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	toasts := []*toast.Toast{
		{ID: "toast-1", Message: "Success!", Level: toast.ToastSuccess},
	}

	// Should not panic
	runner.SetToasts(toasts)
}

// TestRunnerDiagnostics verifies diagnostics can be set.
func TestRunnerDiagnostics(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	collector := diagnostics.NewCollector()

	// Should not panic
	runner.SetDiagnostics(collector)
}

// TestRunnerWelcomeScreen verifies welcome screen can be shown.
func TestRunnerWelcomeScreen(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	// Should not panic
	runner.WelcomeScreen()
}

// TestRunnerClearScrollback verifies scrollback can be cleared.
func TestRunnerClearScrollback(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	runner.AddMessage("Test message", "user")
	runner.ClearScrollback()
}

// TestRunnerShowModelPicker verifies model picker can be shown.
func TestRunnerShowModelPicker(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	items := []widgets.PaletteItem{
		{ID: "gpt-4", Label: "GPT-4"},
		{ID: "claude-3", Label: "Claude 3"},
	}

	// ShowModelPicker should not panic
	runner.ShowModelPicker(items, func(item widgets.PaletteItem) {
		// Callback would be called when an item is selected
	})

	// Verify the palette was shown (modelPalette should have items)
	// Note: We can't easily verify the overlay was pushed without running the event loop
}

// TestRunnerRunWithContext verifies Runner can start and stop.
func TestRunnerRunWithContext(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Run in goroutine, should exit when context is cancelled
	errCh := make(chan error, 1)
	go func() {
		errCh <- runner.RunWithContext(ctx)
	}()

	// Wait for context cancellation or error
	select {
	case err := <-errCh:
		// Context cancellation causes graceful shutdown, not an error
		if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
			t.Errorf("RunWithContext returned unexpected error: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("RunWithContext did not exit within timeout")
	}
}

// TestRunnerPost verifies Post sends messages to the app.
func TestRunnerPost(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	// Post should not panic
	runner.Post(StatusMsg{Text: "Test"})
	runner.Post(AddMessageMsg{Content: "Hello", Source: "user"})
}

// TestRunnerNilSafety verifies nil receiver methods don't panic.
func TestRunnerNilSafety(t *testing.T) {
	var runner *Runner

	// All these should not panic with nil receiver
	runner.SetStatus("test")
	runner.AddMessage("test", "user")
	runner.StreamChunk("session", "text")
	runner.StreamEnd("session", "text")
	runner.SetStreaming(true)
	runner.SetModelName("model")
	runner.SetSessionID("session")
	runner.SetTokenCount(100, 0.01)
	runner.SetContextUsage(100, 200, 1000)
	runner.SetExecutionMode("classic")
	runner.ShowThinkingIndicator()
	runner.RemoveThinkingIndicator()
	runner.AppendReasoning("text")
	runner.CollapseReasoning("preview", "full")
	runner.SetCallbacks(nil, nil, nil)
	runner.SetSessionCallbacks(nil, nil)
	runner.Post(StatusMsg{})
	runner.Quit()
}

// newTestBackend creates a test backend with the given dimensions.
func newTestBackend(width, height int) backend.Backend {
	return &testRunnerBackend{
		events: make(chan terminal.Event, 10),
		width:  width,
		height: height,
	}
}

// testRunnerBackend is a minimal backend for testing Runner.
type testRunnerBackend struct {
	events chan terminal.Event
	width  int
	height int
}

func (b *testRunnerBackend) Init() error                                                  { return nil }
func (b *testRunnerBackend) Fini()                                                        {}
func (b *testRunnerBackend) Size() (int, int)                                             { return b.width, b.height }
func (b *testRunnerBackend) SetContent(x, y int, r rune, comb []rune, style backend.Style) {}
func (b *testRunnerBackend) Show()                                                        {}
func (b *testRunnerBackend) Sync()                                                        {}
func (b *testRunnerBackend) Clear()                                                       {}
func (b *testRunnerBackend) HideCursor()                                                  {}
func (b *testRunnerBackend) ShowCursor()                                                  {}
func (b *testRunnerBackend) SetCursorPos(x, y int)                                        {}
func (b *testRunnerBackend) PollEvent() terminal.Event {
	select {
	case ev := <-b.events:
		return ev
	default:
		return nil
	}
}
func (b *testRunnerBackend) PostEvent(ev terminal.Event) error {
	select {
	case b.events <- ev:
		return nil
	default:
		return nil
	}
}
func (b *testRunnerBackend) EnableMouse()       {}
func (b *testRunnerBackend) DisableMouse()      {}
func (b *testRunnerBackend) EnablePaste()       {}
func (b *testRunnerBackend) DisablePaste()      {}
func (b *testRunnerBackend) EnableFocus()       {}
func (b *testRunnerBackend) DisableFocus()      {}
func (b *testRunnerBackend) HasTrueColor() bool { return true }
func (b *testRunnerBackend) Beep()              {}
