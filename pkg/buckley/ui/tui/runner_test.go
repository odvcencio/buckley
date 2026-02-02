package tui

import (
	"context"
	"testing"
	"time"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/terminal"
	"github.com/odvcencio/fluffyui/toast"
	"github.com/odvcencio/fluffyui/widgets"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
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

func TestRunnerSidebarWidthConfig(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	cfg := RunnerConfig{
		Backend:         testBackend,
		SidebarWidth:    30,
		SidebarMinWidth: 20,
		SidebarMaxWidth: 40,
	}
	runner, err := NewRunner(cfg)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	if got := runner.state.SidebarWidth.Get(); got != 30 {
		t.Fatalf("expected sidebar width signal 30, got %d", got)
	}
	if runner.sidebar.Width() != 30 {
		t.Fatalf("expected sidebar width 30, got %d", runner.sidebar.Width())
	}

	testBackend = newTestBackend(80, 24)
	cfg = RunnerConfig{
		Backend:         testBackend,
		SidebarWidth:    10,
		SidebarMinWidth: 16,
		SidebarMaxWidth: 30,
	}
	runner, err = NewRunner(cfg)
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	if got := runner.state.SidebarWidth.Get(); got != 16 {
		t.Fatalf("expected clamped width 16, got %d", got)
	}
	if runner.sidebar.Width() != 16 {
		t.Fatalf("expected sidebar width 16, got %d", runner.sidebar.Width())
	}
}

func TestRunnerToggleSidebar(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	if !runner.state.SidebarVisible.Get() {
		t.Fatal("expected sidebar to be visible by default")
	}

	runner.toggleSidebar()
	if runner.state.SidebarVisible.Get() {
		t.Error("expected sidebar to be hidden after toggle")
	}

	runner.toggleSidebar()
	if !runner.state.SidebarVisible.Get() {
		t.Error("expected sidebar to be visible after second toggle")
	}
}

func TestRunnerOverlayPopFromWidgetCommand(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	cancel := startRunnerApp(t, runner)
	defer cancel()

	type overlaySnapshot struct {
		layerCount int
		stackLen   int
		depth      int
		popOK      bool
		popHandled bool
	}
	var afterPush, afterPop, afterFinal overlaySnapshot

	overlay := widgets.NewLabel("overlay")
	callErr := runner.app.Call(context.Background(), func(app *runtime.App) error {
		screen := app.Screen()
		if screen == nil {
			return nil
		}
		if ok := runner.handleCommand(runtime.PushOverlay{Widget: overlay, Modal: true}); !ok {
			return nil
		}
		afterPush = overlaySnapshot{
			layerCount: screen.LayerCount(),
			stackLen:   len(runner.overlayStack),
			depth:      runner.overlayDepth,
		}

		afterPop.popOK = screen.PopLayer()
		afterPop.layerCount = screen.LayerCount()

		afterFinal.popHandled = runner.handleCommand(runtime.PopOverlay{})
		afterFinal.layerCount = screen.LayerCount()
		afterFinal.stackLen = len(runner.overlayStack)
		afterFinal.depth = runner.overlayDepth
		return nil
	})
	if callErr != nil {
		t.Fatalf("app call failed: %v", callErr)
	}

	if afterPush.layerCount != 2 {
		t.Fatalf("expected 2 layers after push, got %d", afterPush.layerCount)
	}
	if afterPush.stackLen != 1 {
		t.Fatalf("expected overlay stack size 1, got %d", afterPush.stackLen)
	}
	if afterPush.depth != 1 {
		t.Fatalf("expected overlay depth 1, got %d", afterPush.depth)
	}

	if !afterPop.popOK {
		t.Fatal("expected screen pop to succeed")
	}
	if afterPop.layerCount != 1 {
		t.Fatalf("expected 1 layer after pop, got %d", afterPop.layerCount)
	}

	if !afterFinal.popHandled {
		t.Fatal("expected pop overlay command to be handled")
	}
	if afterFinal.stackLen != 0 {
		t.Fatalf("expected overlay stack size 0, got %d", afterFinal.stackLen)
	}
	if afterFinal.depth != 0 {
		t.Fatalf("expected overlay depth 0, got %d", afterFinal.depth)
	}
	if afterFinal.layerCount != 1 {
		t.Fatalf("expected base layer to remain, got %d", afterFinal.layerCount)
	}
}

func TestRunnerOverlayNonModalDoesNotAffectDepth(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}
	cancel := startRunnerApp(t, runner)
	defer cancel()

	type overlaySnapshot struct {
		layerCount int
		stackLen   int
		depth      int
		popOK      bool
		popHandled bool
	}
	var afterPush, afterFinal overlaySnapshot

	overlay := widgets.NewLabel("overlay")
	callErr := runner.app.Call(context.Background(), func(app *runtime.App) error {
		screen := app.Screen()
		if screen == nil {
			return nil
		}
		if ok := runner.handleCommand(runtime.PushOverlay{Widget: overlay, Modal: false}); !ok {
			return nil
		}
		afterPush = overlaySnapshot{
			layerCount: screen.LayerCount(),
			stackLen:   len(runner.overlayStack),
			depth:      runner.overlayDepth,
		}

		afterFinal.popOK = screen.PopLayer()
		afterFinal.popHandled = runner.handleCommand(runtime.PopOverlay{})
		afterFinal.layerCount = screen.LayerCount()
		afterFinal.stackLen = len(runner.overlayStack)
		afterFinal.depth = runner.overlayDepth
		return nil
	})
	if callErr != nil {
		t.Fatalf("app call failed: %v", callErr)
	}

	if afterPush.layerCount != 2 {
		t.Fatalf("expected 2 layers after push, got %d", afterPush.layerCount)
	}
	if afterPush.stackLen != 1 {
		t.Fatalf("expected overlay stack size 1, got %d", afterPush.stackLen)
	}
	if afterPush.depth != 0 {
		t.Fatalf("expected overlay depth 0 for non-modal, got %d", afterPush.depth)
	}

	if !afterFinal.popOK {
		t.Fatal("expected screen pop to succeed")
	}
	if !afterFinal.popHandled {
		t.Fatal("expected pop overlay command to be handled")
	}
	if afterFinal.stackLen != 0 {
		t.Fatalf("expected overlay stack size 0, got %d", afterFinal.stackLen)
	}
	if afterFinal.depth != 0 {
		t.Fatalf("expected overlay depth 0 after pop, got %d", afterFinal.depth)
	}
}

func TestNormalizeCtrlGChord(t *testing.T) {
	msg := runtime.KeyMsg{Key: terminal.KeyNone, Rune: 'g', Ctrl: true}
	normalized := normalizeCtrlGChord(msg)
	if normalized.Key != terminal.KeyRune {
		t.Fatalf("expected KeyRune, got %v", normalized.Key)
	}
	if normalized.Rune != 'g' {
		t.Fatalf("expected rune g, got %q", normalized.Rune)
	}
	if !normalized.Ctrl {
		t.Fatal("expected ctrl flag to remain true")
	}
}

func TestNormalizeCtrlGChord_IgnoresOtherCtrlKeys(t *testing.T) {
	msg := runtime.KeyMsg{Key: terminal.KeyNone, Rune: 'a', Ctrl: true}
	normalized := normalizeCtrlGChord(msg)
	if normalized.Key != terminal.KeyNone {
		t.Fatalf("expected KeyNone for non-g ctrl, got %v", normalized.Key)
	}
	if normalized.Rune != 'a' {
		t.Fatalf("expected rune a, got %q", normalized.Rune)
	}
	if !normalized.Ctrl {
		t.Fatal("expected ctrl flag to remain true")
	}
}

// TestRunnerSetStatus verifies SetStatus updates status state.
func TestRunnerSetStatus(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	// SetStatus should not panic
	runner.SetStatus("Test status")
	if runner.state.StatusText.Get() != "Test status" {
		t.Errorf("expected status text to update, got %q", runner.state.StatusText.Get())
	}
}

// TestRunnerAddMessage verifies AddMessage updates chat state.
func TestRunnerAddMessage(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	// AddMessage should not panic
	runner.AddMessage("Test message", "user")
	runner.AddMessage("Response", "assistant")
	msgs := runner.state.ChatMessages.Get()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Content != "Test message" || msgs[0].Source != "user" {
		t.Errorf("unexpected first message: %+v", msgs[0])
	}
	if msgs[1].Content != "Response" || msgs[1].Source != "assistant" {
		t.Errorf("unexpected second message: %+v", msgs[1])
	}
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
	if !runner.state.IsStreaming.Get() {
		t.Error("expected streaming true")
	}
	runner.StreamChunk("session-1", "Hello ")
	runner.StreamChunk("session-1", "World!")
	runner.StreamEnd("session-1", "Hello World!")
	runner.SetStreaming(false)
	if runner.state.IsStreaming.Get() {
		t.Error("expected streaming false")
	}
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

	if runner.state.StatusMode.Get() != "classic" {
		t.Errorf("expected execution mode classic, got %q", runner.state.StatusMode.Get())
	}
	if runner.state.StatusTokens.Get() != 1000 {
		t.Errorf("expected tokens 1000, got %d", runner.state.StatusTokens.Get())
	}
	if runner.state.ContextUsed.Get() != 5000 {
		t.Errorf("expected context used 5000, got %d", runner.state.ContextUsed.Get())
	}
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
	if got := runner.state.ProgressItems.Get(); len(got) != len(items) {
		t.Errorf("expected %d progress items, got %d", len(items), len(got))
	}
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

// TestRunnerSetChatMessages verifies chat history can be replaced.
func TestRunnerSetChatMessages(t *testing.T) {
	testBackend := newTestBackend(80, 24)
	runner, err := NewRunner(RunnerConfig{Backend: testBackend})
	if err != nil {
		t.Fatalf("NewRunner failed: %v", err)
	}

	now := time.Now()
	messages := []buckleywidgets.ChatMessage{
		{ID: 1, Content: "Hello", Source: "system", Time: now},
		{ID: 2, Content: "Hey", Source: "user", Time: now.Add(time.Second)},
	}

	runner.SetChatMessages(messages)
	got := runner.state.ChatMessages.Get()
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].Content != "Hello" || got[0].Source != "system" {
		t.Errorf("unexpected first message: %+v", got[0])
	}
	if got[1].Content != "Hey" || got[1].Source != "user" {
		t.Errorf("unexpected second message: %+v", got[1])
	}
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

func startRunnerApp(t *testing.T, runner *Runner) func() {
	t.Helper()
	if runner == nil || runner.app == nil {
		t.Fatal("expected runner app")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = runner.app.Run(ctx)
		close(done)
	}()
	deadline := time.Now().Add(200 * time.Millisecond)
	for runner.app.Screen() == nil {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("timed out waiting for app screen")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return func() {
		cancel()
		<-done
	}
}

// testRunnerBackend is a minimal backend for testing Runner.
type testRunnerBackend struct {
	events chan terminal.Event
	width  int
	height int
}

func (b *testRunnerBackend) Init() error                                                   { return nil }
func (b *testRunnerBackend) Fini()                                                         {}
func (b *testRunnerBackend) Size() (int, int)                                              { return b.width, b.height }
func (b *testRunnerBackend) SetContent(x, y int, r rune, comb []rune, style backend.Style) {}
func (b *testRunnerBackend) Show()                                                         {}
func (b *testRunnerBackend) Sync()                                                         {}
func (b *testRunnerBackend) Clear()                                                        {}
func (b *testRunnerBackend) HideCursor()                                                   {}
func (b *testRunnerBackend) ShowCursor()                                                   {}
func (b *testRunnerBackend) SetCursorPos(x, y int)                                         {}
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
