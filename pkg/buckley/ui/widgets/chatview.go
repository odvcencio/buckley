package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/state"
	"github.com/odvcencio/fluffyui/terminal"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// ChatView displays the conversation history with scrolling.
type ChatView struct {
	uiwidgets.FocusableBase
	*ChatMessages

	layout   *runtime.Flex
	services runtime.Services
	bgStyle  backend.Style

	reasoningTextSig    state.Readable[string]
	reasoningPreviewSig state.Readable[string]
	reasoningVisibleSig state.Readable[bool]

	ownedReasoningTextSig    *state.Signal[string]
	ownedReasoningPreviewSig *state.Signal[string]
	ownedReasoningVisibleSig *state.Signal[bool]

	reasoning *ChatReasoning
}

// ChatViewConfig provides external state for the chat view.
type ChatViewConfig struct {
	Messages         state.Readable[[]ChatMessage]
	Thinking         state.Readable[bool]
	ReasoningText    state.Readable[string]
	ReasoningPreview state.Readable[string]
	ReasoningVisible state.Readable[bool]
}

// NewChatView creates a new chat view widget.
func NewChatView() *ChatView {
	return NewChatViewWithConfig(ChatViewConfig{})
}

// NewChatViewWithConfig creates a chat view with external state bindings.
func NewChatViewWithConfig(cfg ChatViewConfig) *ChatView {
	chat := &ChatView{}
	chat.ChatMessages = NewChatMessagesWithConfig(ChatMessagesConfig{
		Messages: cfg.Messages,
		Thinking: cfg.Thinking,
	})

	chat.ownedReasoningTextSig = state.NewSignal("")
	chat.ownedReasoningPreviewSig = state.NewSignal("")
	chat.ownedReasoningVisibleSig = state.NewSignal(false)
	if cfg.ReasoningText != nil {
		chat.reasoningTextSig = cfg.ReasoningText
	} else {
		chat.reasoningTextSig = chat.ownedReasoningTextSig
	}
	if cfg.ReasoningPreview != nil {
		chat.reasoningPreviewSig = cfg.ReasoningPreview
	} else {
		chat.reasoningPreviewSig = chat.ownedReasoningPreviewSig
	}
	if cfg.ReasoningVisible != nil {
		chat.reasoningVisibleSig = cfg.ReasoningVisible
	} else {
		chat.reasoningVisibleSig = chat.ownedReasoningVisibleSig
	}

	chat.reasoning = NewChatReasoning(ChatReasoningBindings{
		Text:    chat.reasoningTextSig,
		Preview: chat.reasoningPreviewSig,
		Visible: chat.reasoningVisibleSig,
	})
	chat.reasoning.SetOnVisibilityChange(chat.onReasoningVisibilityChange)
	chat.rebuildLayout()
	return chat
}

// SetStyles configures the message styles.
func (c *ChatView) SetStyles(user, assistant, system, tool, thinking backend.Style) {
	if c.ChatMessages != nil {
		c.ChatMessages.SetStyles(user, assistant, system, tool, thinking)
	}
	if c.reasoning != nil {
		c.reasoning.SetTextStyle(thinking)
	}
}

// SetUIStyles configures scrollbar, selection, and background styles.
func (c *ChatView) SetUIStyles(scrollbar, thumb, selection, search, background backend.Style) {
	if c.ChatMessages != nil {
		c.ChatMessages.SetUIStyles(scrollbar, thumb, selection, search, background)
	}
	if c.reasoning != nil {
		c.reasoning.SetPanelStyle(scrollbar, background)
	}
	c.bgStyle = background
}

// OnCodeAction registers a handler for code block actions from the chat list.
func (c *ChatView) OnCodeAction(fn func(action, language, code string)) {
	if c.ChatMessages != nil {
		c.ChatMessages.OnCodeAction(fn)
	}
}

func (c *ChatView) ownsReasoning() bool {
	if c == nil || c.reasoningTextSig == nil || c.reasoningPreviewSig == nil || c.reasoningVisibleSig == nil {
		return false
	}
	if c.ownedReasoningTextSig == nil || c.ownedReasoningPreviewSig == nil || c.ownedReasoningVisibleSig == nil {
		return false
	}
	textSig, ok := c.reasoningTextSig.(*state.Signal[string])
	if !ok || textSig != c.ownedReasoningTextSig {
		return false
	}
	previewSig, ok := c.reasoningPreviewSig.(*state.Signal[string])
	if !ok || previewSig != c.ownedReasoningPreviewSig {
		return false
	}
	visibleSig, ok := c.reasoningVisibleSig.(*state.Signal[bool])
	return ok && visibleSig == c.ownedReasoningVisibleSig
}

// AppendReasoning appends streaming reasoning text (dimmed).
func (c *ChatView) AppendReasoning(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if c.ownsReasoning() {
		if c.ownedReasoningTextSig != nil {
			current := c.ownedReasoningTextSig.Get()
			c.ownedReasoningTextSig.Set(current + text)
		}
		if c.ownedReasoningVisibleSig != nil {
			c.ownedReasoningVisibleSig.Set(true)
		}
	}
}

// CollapseReasoning collapses reasoning block to preview.
func (c *ChatView) CollapseReasoning(preview, full string) {
	if c.ownsReasoning() {
		if c.ownedReasoningPreviewSig != nil {
			c.ownedReasoningPreviewSig.Set(strings.TrimSpace(preview))
		}
		if c.ownedReasoningTextSig != nil {
			c.ownedReasoningTextSig.Set(full)
		}
		if c.ownedReasoningVisibleSig != nil {
			c.ownedReasoningVisibleSig.Set(true)
		}
	}
}

// ClearReasoningBlock clears reasoning state for new message.
func (c *ChatView) ClearReasoningBlock() {
	if c.ownsReasoning() {
		if c.ownedReasoningTextSig != nil {
			c.ownedReasoningTextSig.Set("")
		}
		if c.ownedReasoningPreviewSig != nil {
			c.ownedReasoningPreviewSig.Set("")
		}
		if c.ownedReasoningVisibleSig != nil {
			c.ownedReasoningVisibleSig.Set(false)
		}
	}
}

// ToggleReasoning toggles reasoning block between collapsed and expanded.
// Returns true if a toggle occurred.
func (c *ChatView) ToggleReasoning() bool {
	if c.reasoning == nil {
		return false
	}
	return c.reasoning.ToggleExpanded()
}

// Clear clears all messages.
func (c *ChatView) Clear() {
	if c.ChatMessages != nil {
		c.ChatMessages.Clear()
	}
	c.ClearReasoningBlock()
}

// Measure returns the preferred size.
func (c *ChatView) Measure(constraints runtime.Constraints) runtime.Size {
	return runtime.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight}
}

// Layout updates the layout bounds.
func (c *ChatView) Layout(bounds runtime.Rect) {
	c.FocusableBase.Layout(bounds)
	if c.layout == nil {
		c.rebuildLayout()
	}
	if c.layout != nil {
		c.layout.Layout(bounds)
	}
}

// Render draws the chat view.
func (c *ChatView) Render(ctx runtime.RenderContext) {
	bounds := c.FocusableBase.Bounds()
	if bounds.Width == 0 || bounds.Height == 0 {
		return
	}
	ctx.Clear(c.bgStyle)
	if c.layout == nil {
		c.rebuildLayout()
	}
	if c.layout != nil {
		c.layout.Render(ctx)
	}
}

// HandleMessage processes input events.
func (c *ChatView) HandleMessage(msg runtime.Message) runtime.HandleResult {
	switch m := msg.(type) {
	case runtime.KeyMsg:
		if c.FocusableBase.IsFocused() {
			switch m.Key {
			case terminal.KeyUp:
				c.ScrollUp(1)
				return runtime.Handled()
			case terminal.KeyDown:
				c.ScrollDown(1)
				return runtime.Handled()
			case terminal.KeyPageUp:
				c.PageUp()
				return runtime.Handled()
			case terminal.KeyPageDown:
				c.PageDown()
				return runtime.Handled()
			}
		}
	}
	if c.layout != nil {
		if result := c.layout.HandleMessage(msg); result.Handled {
			return result
		}
	}
	return runtime.Unhandled()
}

func (c *ChatView) rebuildLayout() {
	children := make([]runtime.FlexChild, 0, 2)
	if c.reasoning != nil && c.reasoning.Visible() {
		children = append(children, runtime.Fixed(c.reasoning.Widget()))
	}
	if c.ChatMessages != nil {
		children = append(children, runtime.Expanded(c.ChatMessages))
	}
	c.layout = runtime.VBox(children...)
}

func (c *ChatView) onReasoningVisibilityChange(visible bool) {
	c.rebuildLayout()
	if c.services != (runtime.Services{}) {
		c.services.Relayout()
		c.services.Invalidate()
		return
	}
}

// ReasoningContains reports whether a screen point falls within the reasoning panel.
func (c *ChatView) ReasoningContains(x, y int) bool {
	if c.reasoning == nil {
		return false
	}
	return c.reasoning.Contains(x, y)
}

// Bind stores app services for layout invalidation.
func (c *ChatView) Bind(services runtime.Services) {
	if c == nil {
		return
	}
	c.services = services
	if c.reasoning != nil {
		c.reasoning.Bind(services)
	}
}

// Unbind clears app services.
func (c *ChatView) Unbind() {
	if c == nil {
		return
	}
	if c.reasoning != nil {
		c.reasoning.Unbind()
	}
	c.services = runtime.Services{}
}

// ChildWidgets returns child widgets for proper widget tree traversal.
func (c *ChatView) ChildWidgets() []runtime.Widget {
	if c.layout == nil {
		return nil
	}
	return c.layout.ChildWidgets()
}

// Focus sets focus on the chat view.
func (c *ChatView) Focus() {
	c.FocusableBase.Focus()
}

// Blur removes focus from the chat view.
func (c *ChatView) Blur() {
	c.FocusableBase.Blur()
}

// IsFocused reports whether the chat view is focused.
func (c *ChatView) IsFocused() bool {
	return c.FocusableBase.IsFocused()
}

// Bounds returns the chat view bounds.
func (c *ChatView) Bounds() runtime.Rect {
	return c.FocusableBase.Bounds()
}
