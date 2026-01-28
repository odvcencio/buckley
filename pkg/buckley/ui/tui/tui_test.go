package tui

import (
	"testing"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/terminal"
	"github.com/odvcencio/fluffyui/theme"
	"github.com/odvcencio/fluffyui/widgets"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
)

func TestLayoutConstants(t *testing.T) {
	// Verify layout constants are reasonable
	if theme.Layout.HeaderHeight < 1 {
		t.Error("HeaderHeight should be at least 1")
	}
	if theme.Layout.StatusHeight < 1 {
		t.Error("StatusHeight should be at least 1")
	}
	if theme.Layout.InputMinHeight < 1 {
		t.Error("InputMinHeight should be at least 1")
	}
	if theme.Layout.PickerMaxHeight < 5 {
		t.Error("PickerMaxHeight should be at least 5 for usability")
	}
}

func TestSymbolsNotEmpty(t *testing.T) {
	symbols := []struct {
		name  string
		value string
	}{
		{"Bullet", theme.Symbols.Bullet},
		{"Check", theme.Symbols.Check},
		{"Cross", theme.Symbols.Cross},
		{"BorderHorizontal", theme.Symbols.BorderHorizontal},
		{"BorderVertical", theme.Symbols.BorderVertical},
		{"User", theme.Symbols.User},
		{"Assistant", theme.Symbols.Assistant},
		{"ModeNormal", theme.Symbols.ModeNormal},
		{"ModeShell", theme.Symbols.ModeShell},
		{"ModeEnv", theme.Symbols.ModeEnv},
		{"ModeSearch", theme.Symbols.ModeSearch},
	}

	for _, s := range symbols {
		if s.value == "" {
			t.Errorf("Symbol %s is empty", s.name)
		}
	}
}

func TestCoalescerConfig(t *testing.T) {
	cfg := DefaultCoalescerConfig()
	if cfg.MaxWait <= 0 {
		t.Error("MaxWait should be positive")
	}
	if cfg.MaxChars <= 0 {
		t.Error("MaxChars should be positive")
	}
}

func TestRenderMetricsZeroValue(t *testing.T) {
	var m RenderMetrics
	if m.FrameCount != 0 {
		t.Error("FrameCount should be 0")
	}
	if m.DroppedFrames != 0 {
		t.Error("DroppedFrames should be 0")
	}
}

func TestInputAreaImplementsFocusable(t *testing.T) {
	inputArea := buckleywidgets.NewInputArea()

	// Verify InputArea implements Focusable
	var _ runtime.Focusable = inputArea

	// Verify CanFocus returns true
	if !inputArea.CanFocus() {
		t.Error("inputArea.CanFocus() returned false")
	}
}

func TestInputAreaFocusRegistration(t *testing.T) {
	// Create a minimal widget tree that mirrors the app's structure
	inputArea := buckleywidgets.NewInputArea()

	// First verify InputArea is actually Focusable
	focusable, ok := interface{}(inputArea).(runtime.Focusable)
	if !ok {
		t.Fatal("InputArea does not implement runtime.Focusable")
	}
	t.Logf("InputArea implements Focusable: CanFocus()=%v", focusable.CanFocus())

	// Create a VBox with the inputArea
	root := runtime.VBox(
		runtime.Fixed(inputArea),
	)

	// Verify root exposes children
	children := root.ChildWidgets()
	t.Logf("VBox has %d children", len(children))
	for i, child := range children {
		_, isFocusable := child.(runtime.Focusable)
		t.Logf("  child[%d]: type=%T focusable=%v", i, child, isFocusable)
	}

	// Create screen with auto-focus enabled
	screen := runtime.NewScreen(80, 24)
	screen.SetAutoRegisterFocus(true)
	screen.PushLayer(root, false)

	// Get the base layer's focus scope
	scope := screen.BaseFocusScope()
	if scope == nil {
		t.Fatal("BaseFocusScope() returned nil")
	}

	// Check that inputArea was registered
	count := scope.Count()
	t.Logf("Focus scope has %d registered focusables", count)
	if count == 0 {
		t.Fatal("No focusables registered in focus scope")
	}

	// Check what was actually registered
	current := scope.Current()
	t.Logf("Current focused widget: %T (same as inputArea: %v)", current, current == inputArea)

	// If inputArea is already focused (from auto-focus during registration),
	// SetFocus returns false because focus didn't change. This is expected behavior.
	// Let's verify that inputArea IS the currently focused widget.
	if current != inputArea {
		t.Errorf("Expected inputArea to be focused, got %T", current)
	}

	// Verify inputArea is now focused
	if !inputArea.IsFocused() {
		t.Error("inputArea.IsFocused() returned false")
	}

	t.Log("Focus registration test passed - inputArea is properly registered and focused")
}

func TestInputAreaHandlesKeyMessages(t *testing.T) {
	inputArea := buckleywidgets.NewInputArea()

	// Focus the input area directly
	inputArea.Focus()

	if !inputArea.IsFocused() {
		t.Fatal("inputArea.IsFocused() returned false after Focus()")
	}

	// Send a key message - KeyRune indicates a regular character
	msg := runtime.KeyMsg{
		Key:  terminal.KeyRune,
		Rune: 'a',
	}

	t.Logf("Sending KeyMsg: Key=%d Rune=%q", msg.Key, msg.Rune)

	result := inputArea.HandleMessage(msg)
	t.Logf("HandleMessage returned: Handled=%v Commands=%v", result.Handled, result.Commands)

	// Check what the textarea received
	text := inputArea.Text()
	t.Logf("InputArea text after HandleMessage: %q", text)

	if !result.Handled {
		t.Error("HandleMessage for 'a' key returned Unhandled when input is focused")
	}

	// Verify the text was inserted
	if text != "a" {
		t.Errorf("inputArea.Text() = %q, want %q", text, "a")
	}
}

func TestInputAreaInFullWidgetTree(t *testing.T) {
	// Create a widget tree that mirrors the actual app structure
	inputArea := buckleywidgets.NewInputArea()
	chatView := buckleywidgets.NewChatView()

	mainArea := runtime.HBox(
		runtime.Expanded(chatView),
	)

	root := runtime.VBox(
		runtime.Expanded(mainArea),
		runtime.Fixed(inputArea),
	)

	// Create screen with auto-focus enabled
	screen := runtime.NewScreen(80, 24)
	screen.SetAutoRegisterFocus(true)
	screen.PushLayer(root, false)

	// Simulate how initFocus works
	scope := screen.BaseFocusScope()
	if scope == nil {
		t.Fatal("BaseFocusScope() returned nil")
	}

	t.Logf("Focus scope has %d registered focusables", scope.Count())

	// SetFocus on the inputArea
	ok := scope.SetFocus(inputArea)
	t.Logf("scope.SetFocus(inputArea) returned %v", ok)

	// If SetFocus returns false, it might be because inputArea is already focused
	// (auto-focused during registration)
	current := scope.Current()
	t.Logf("scope.Current() = %T (is inputArea: %v)", current, current == inputArea)

	// Verify inputArea is focused
	if !inputArea.IsFocused() {
		t.Fatal("inputArea.IsFocused() returned false")
	}

	// Now send a key message through the screen
	msg := runtime.KeyMsg{
		Key:  terminal.KeyRune,
		Rune: 'x',
	}

	// First try sending directly to inputArea (like handleKeyMsg does when focused)
	result := inputArea.HandleMessage(msg)
	t.Logf("inputArea.HandleMessage returned: Handled=%v", result.Handled)

	text := inputArea.Text()
	t.Logf("inputArea.Text() = %q", text)

	if !result.Handled {
		t.Error("inputArea.HandleMessage returned Unhandled when focused")
	}
	if text != "x" {
		t.Errorf("inputArea.Text() = %q, want %q", text, "x")
	}
}

func TestInputWithOverlayLayers(t *testing.T) {
	// Test that input works even with overlay layers (like in the real app)
	inputArea := buckleywidgets.NewInputArea()
	chatView := buckleywidgets.NewChatView()

	mainArea := runtime.HBox(
		runtime.Expanded(chatView),
	)

	root := runtime.VBox(
		runtime.Expanded(mainArea),
		runtime.Fixed(inputArea),
	)

	// Create empty overlay widgets (like alertBanner and toastStack)
	overlay1 := runtime.VBox() // Empty flex - no focusables
	overlay2 := runtime.VBox() // Empty flex - no focusables

	// Create screen with auto-focus enabled
	screen := runtime.NewScreen(80, 24)
	screen.SetAutoRegisterFocus(true)

	// Push layers in the same order as the real app
	screen.PushLayer(root, false)
	screen.PushLayer(overlay1, false)
	screen.PushLayer(overlay2, false)

	// Verify we have 3 layers
	if screen.LayerCount() != 3 {
		t.Fatalf("Expected 3 layers, got %d", screen.LayerCount())
	}

	// The BASE layer's focus scope should have the inputArea
	baseScope := screen.BaseFocusScope()
	if baseScope == nil {
		t.Fatal("BaseFocusScope() returned nil")
	}
	t.Logf("Base layer focus scope has %d focusables", baseScope.Count())

	// The TOP layer's focus scope might be empty
	topScope := screen.FocusScope()
	if topScope != nil {
		t.Logf("Top layer focus scope has %d focusables", topScope.Count())
	}

	// Set focus on inputArea in base scope
	baseScope.SetFocus(inputArea)

	// Verify inputArea is focused
	if !inputArea.IsFocused() {
		t.Fatal("inputArea.IsFocused() returned false")
	}

	// Verify that BaseFocusScope().Current() returns inputArea
	if baseScope.Current() != inputArea {
		t.Errorf("BaseFocusScope().Current() = %T, want inputArea", baseScope.Current())
	}

	// Now send a key message
	msg := runtime.KeyMsg{
		Key:  terminal.KeyRune,
		Rune: 'z',
	}

	result := inputArea.HandleMessage(msg)
	if !result.Handled {
		t.Error("inputArea.HandleMessage returned Unhandled when focused")
	}

	text := inputArea.Text()
	if text != "z" {
		t.Errorf("inputArea.Text() = %q, want %q", text, "z")
	}
}

func TestFluffyUITextAreaDirect(t *testing.T) {
	// Test using fluffyui's TextArea directly, not our InputArea wrapper
	textarea := widgets.NewTextArea()
	
	// Create a simple widget tree
	root := runtime.VBox(
		runtime.Fixed(textarea),
	)
	
	// Create screen
	screen := runtime.NewScreen(80, 24)
	screen.SetAutoRegisterFocus(true)
	screen.PushLayer(root, false)
	
	// Get focus scope and set focus
	scope := screen.BaseFocusScope()
	if scope == nil {
		t.Fatal("BaseFocusScope is nil")
	}
	
	t.Logf("Scope has %d focusables", scope.Count())
	
	// Set focus on textarea
	scope.SetFocus(textarea)
	textarea.Focus()
	
	t.Logf("textarea.IsFocused() = %v", textarea.IsFocused())
	
	if !textarea.IsFocused() {
		t.Fatal("TextArea is not focused after Focus() call")
	}
	
	// Send a key message
	msg := runtime.KeyMsg{
		Key:  terminal.KeyRune,
		Rune: 'x',
	}
	
	result := textarea.HandleMessage(msg)
	t.Logf("HandleMessage result: Handled=%v", result.Handled)
	
	text := textarea.Text()
	t.Logf("TextArea text: %q", text)
	
	if text != "x" {
		t.Errorf("Expected text 'x', got %q", text)
	}
}

func TestWidgetAppKeyInput(t *testing.T) {
	// Create a minimal WidgetApp using a test backend
	testBackend := &testBackendImpl{
		events: make(chan terminal.Event, 10),
		width:  80,
		height: 24,
	}
	
	cfg := WidgetAppConfig{
		Backend: testBackend,
	}
	
	app, err := NewWidgetApp(cfg)
	if err != nil {
		t.Fatalf("NewWidgetApp failed: %v", err)
	}
	
	// Verify inputArea is focused
	if !app.inputArea.IsFocused() {
		t.Fatal("inputArea is not focused after NewWidgetApp")
	}
	
	// Simulate a key event through the backend
	testBackend.events <- terminal.KeyEvent{
		Key:  terminal.KeyRune,
		Rune: 'z',
	}
	
	// Process the event manually (since we're not running the full loop)
	select {
	case ev := <-testBackend.events:
		if ke, ok := ev.(terminal.KeyEvent); ok {
			msg := KeyMsg{
				Key:  int(ke.Key),
				Rune: ke.Rune,
			}
			app.handleKeyMsg(msg)
		}
	default:
		t.Fatal("No event received")
	}
	
	// Check if text was entered
	text := app.inputArea.Text()
	t.Logf("InputArea text after key: %q", text)
	
	if text != "z" {
		t.Errorf("Expected 'z', got %q", text)
	}
	
	app.Quit()
}

// testBackendImpl is a minimal backend for testing
type testBackendImpl struct {
	events chan terminal.Event
	width  int
	height int
}

func (b *testBackendImpl) Init() error                                               { return nil }
func (b *testBackendImpl) Fini()                                                     {}
func (b *testBackendImpl) Size() (int, int)                                          { return b.width, b.height }
func (b *testBackendImpl) SetContent(x, y int, r rune, comb []rune, style backend.Style) {}
func (b *testBackendImpl) Show()                                                     {}
func (b *testBackendImpl) Sync()                                                     {}
func (b *testBackendImpl) Clear()                                                    {}
func (b *testBackendImpl) HideCursor()                                               {}
func (b *testBackendImpl) ShowCursor()                                               {}
func (b *testBackendImpl) SetCursorPos(x, y int)                                     {}
func (b *testBackendImpl) PollEvent() terminal.Event {
	select {
	case ev := <-b.events:
		return ev
	default:
		return nil
	}
}
func (b *testBackendImpl) PostEvent(ev terminal.Event) error {
	select {
	case b.events <- ev:
		return nil
	default:
		return nil
	}
}
func (b *testBackendImpl) Beep() {}
