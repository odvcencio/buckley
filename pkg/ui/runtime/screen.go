package runtime

import "github.com/odvcencio/buckley/pkg/ui/theme"

// Layer represents a layer in the modal stack.
// Each layer has its own widget tree and focus scope.
type Layer struct {
	Root       Widget
	FocusScope *FocusScope
	Modal      bool // If true, blocks input to layers below
}

// Screen manages the widget tree, modal stack, and rendering.
type Screen struct {
	width, height int
	layers        []*Layer
	buffer        *Buffer
	theme         *theme.Theme
}

// NewScreen creates a new screen with the given dimensions.
func NewScreen(w, h int, th *theme.Theme) *Screen {
	if th == nil {
		th = theme.DefaultTheme()
	}
	return &Screen{
		width:  w,
		height: h,
		buffer: NewBuffer(w, h),
		theme:  th,
	}
}

// Size returns the screen dimensions.
func (s *Screen) Size() (w, h int) {
	return s.width, s.height
}

// Resize changes the screen dimensions.
func (s *Screen) Resize(w, h int) {
	s.width = w
	s.height = h
	s.buffer.Resize(w, h)

	// Re-layout all layers
	bounds := Rect{0, 0, w, h}
	for _, layer := range s.layers {
		if layer.Root != nil {
			layer.Root.Layout(bounds)
		}
	}
}

// Buffer returns the screen's render buffer.
func (s *Screen) Buffer() *Buffer {
	return s.buffer
}

// Theme returns the current theme.
func (s *Screen) Theme() *theme.Theme {
	return s.theme
}

// SetTheme changes the theme.
func (s *Screen) SetTheme(th *theme.Theme) {
	s.theme = th
}

// SetRoot sets the root widget of the base layer.
// Creates the base layer if it doesn't exist.
func (s *Screen) SetRoot(root Widget) {
	if len(s.layers) == 0 {
		s.layers = append(s.layers, &Layer{
			Root:       root,
			FocusScope: NewFocusScope(),
			Modal:      false,
		})
	} else {
		s.layers[0].Root = root
	}

	// Layout the root widget
	if root != nil {
		root.Layout(Rect{0, 0, s.width, s.height})
	}
}

// Root returns the base layer's root widget.
func (s *Screen) Root() Widget {
	if len(s.layers) == 0 {
		return nil
	}
	return s.layers[0].Root
}

// PushLayer adds a new layer on top of the stack.
// If modal is true, input won't pass to layers below.
func (s *Screen) PushLayer(root Widget, modal bool) {
	layer := &Layer{
		Root:       root,
		FocusScope: NewFocusScope(),
		Modal:      modal,
	}
	s.layers = append(s.layers, layer)

	// Layout the new layer
	if root != nil {
		root.Layout(Rect{0, 0, s.width, s.height})
	}
}

// PopLayer removes the top layer from the stack.
// Returns false if only the base layer remains (can't pop it).
func (s *Screen) PopLayer() bool {
	if len(s.layers) <= 1 {
		return false
	}

	// Clear focus on the layer being removed
	top := s.layers[len(s.layers)-1]
	top.FocusScope.ClearFocus()

	s.layers = s.layers[:len(s.layers)-1]
	return true
}

// TopLayer returns the topmost layer.
func (s *Screen) TopLayer() *Layer {
	if len(s.layers) == 0 {
		return nil
	}
	return s.layers[len(s.layers)-1]
}

// LayerCount returns the number of layers.
func (s *Screen) LayerCount() int {
	return len(s.layers)
}

// FocusScope returns the focus scope of the top layer.
func (s *Screen) FocusScope() *FocusScope {
	if top := s.TopLayer(); top != nil {
		return top.FocusScope
	}
	return nil
}

// Render draws all layers to the buffer.
func (s *Screen) Render() {
	s.buffer.Clear()

	ctx := RenderContext{
		Buffer:  s.buffer,
		Theme:   s.theme,
		Focused: false,
		Bounds:  Rect{0, 0, s.width, s.height},
	}

	// Render layers from bottom to top
	for i, layer := range s.layers {
		if layer.Root == nil {
			continue
		}

		// Determine if this layer contains focus
		isTopLayer := i == len(s.layers)-1
		ctx.Focused = isTopLayer

		layer.Root.Render(ctx)
	}
}

// HandleMessage dispatches a message to the appropriate layer.
// Messages go to the top layer. If not handled and not modal,
// they bubble down to lower layers.
func (s *Screen) HandleMessage(msg Message) HandleResult {
	// Process from top to bottom
	for i := len(s.layers) - 1; i >= 0; i-- {
		layer := s.layers[i]
		if layer.Root == nil {
			continue
		}

		result := layer.Root.HandleMessage(msg)

		// Process any commands
		for _, cmd := range result.Commands {
			s.handleCommand(cmd)
		}

		if result.Handled {
			return result
		}

		// If modal, don't pass to lower layers
		if layer.Modal {
			break
		}
	}

	return Unhandled()
}

// handleCommand processes a command from a widget.
func (s *Screen) handleCommand(cmd Command) {
	switch c := cmd.(type) {
	case FocusNext:
		if scope := s.FocusScope(); scope != nil {
			scope.FocusNext()
		}
	case FocusPrev:
		if scope := s.FocusScope(); scope != nil {
			scope.FocusPrev()
		}
	case PopOverlay:
		s.PopLayer()
	case PushOverlay:
		s.PushLayer(c.Widget, c.Modal)
	}
	// Other commands bubble up to App
}

// RenderContext provides context to widgets during rendering.
type RenderContext struct {
	Buffer  *Buffer
	Theme   *theme.Theme
	Focused bool   // Is the containing layer focused?
	Bounds  Rect   // Widget's allocated bounds
}

// Sub creates a new context for a child widget with adjusted bounds.
func (ctx RenderContext) Sub(bounds Rect) RenderContext {
	return RenderContext{
		Buffer:  ctx.Buffer,
		Theme:   ctx.Theme,
		Focused: ctx.Focused,
		Bounds:  bounds,
	}
}

// SubBuffer returns a buffer view clipped to the context bounds.
func (ctx RenderContext) SubBuffer() *SubBuffer {
	return ctx.Buffer.Sub(ctx.Bounds)
}
