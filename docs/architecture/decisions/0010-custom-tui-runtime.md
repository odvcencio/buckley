# ADR 0010: Custom TUI Runtime with tcell Backend

## Status

Accepted

## Context

Buckley needs a terminal UI for:
- Streaming LLM responses with real-time updates
- Tool approval dialogs with diff previews
- Multi-pane layouts (chat, sidebar, status)
- Rich text rendering (markdown, syntax highlighting)

Requirements:
- Smooth streaming without flicker
- Testable rendering (golden-frame tests)
- Efficient partial updates (dirty-region tracking)
- Complex layouts (panels, modals, scrolling)

Options considered:
1. **Bubbletea** - Elm-architecture, string-based View(), no dirty tracking
2. **tview** - Widget-based but opinionated, hard to customize
3. **Charm's lipgloss + bubbletea** - Better styling but still string-concat rendering
4. **Custom runtime on tcell** - Full control, retained-mode, testable

## Decision

Build a custom retained-mode TUI framework:

### Architecture

```
pkg/ui/
├── backend/       # Terminal abstraction (tcell, simulation)
├── runtime/       # Buffer, dirty tracking, event loop
├── compositor/    # ANSI generation, delta rendering
├── widgets/       # Reusable UI components
├── terminal/      # Event types, key handling
└── tui/           # Application-level orchestration
```

### Key Components

**Backend Abstraction**
```go
type Backend interface {
    Init() error
    Fini()
    Size() (width, height int)
    SetContent(x, y int, mainc rune, comb []rune, style Style)
    Show()
    PollEvent() terminal.Event
}
```

Allows swapping tcell for simulation backend in tests.

**Retained-Mode Buffer**
```go
type Buffer struct {
    cells      []Cell
    dirty      []bool      // Per-cell dirty tracking
    dirtyRect  Rect        // Bounding box for partial redraws
}

func (b *Buffer) Set(x, y int, r rune, s Style) {
    // Only mark dirty if content actually changed
    if old.Rune != r || old.Style != s {
        b.cells[idx] = Cell{Rune: r, Style: s}
        b.markCellDirty(x, y, idx)
    }
}
```

**Widget System**
```go
type Widget interface {
    Measure(constraints Constraints) Size
    Layout(bounds Rect)
    Render(buf *Buffer)
    HandleMessage(msg Message) Result
}
```

Widgets: ApprovalWidget, ChatView, FilePicker, Header, InputArea, Palette, Panel, Search, Sidebar, Status

### Why Not Bubbletea?

1. **String concatenation rendering** - Bubbletea's `View() string` rebuilds entire screen each frame. No dirty tracking, expensive for large UIs.

2. **No backend abstraction** - Direct terminal writes make golden-frame testing difficult.

3. **Streaming performance** - Appending to chat view triggers full re-render. Our approach only updates changed cells.

4. **Complex layouts** - Bubbletea leaves layout to the user. We needed constraint-based layout with proper clipping.

## Consequences

### Positive
- Efficient partial updates via dirty tracking
- Testable with simulation backend and golden frames
- Full control over rendering pipeline
- Smooth streaming with minimal redraws
- Clean separation of concerns

### Negative
- Significant implementation effort (~4000 LOC)
- Maintenance burden vs. community-maintained libraries
- Learning curve for contributors
- Must implement common widgets from scratch

### Testing Strategy
```go
func TestApprovalWidget_Render(t *testing.T) {
    backend := NewSimulationBackend(80, 24)
    widget := NewApprovalWidget(req)
    widget.Render(backend.Buffer())

    // Compare against golden frame
    golden.Assert(t, backend.Capture(), "approval_dialog.golden")
}
```

### Performance Characteristics
- Cell-level dirty tracking: O(1) per cell change
- Dirty rect computation: O(dirty cells)
- Partial redraw: only changed regions flushed to terminal
- Streaming text: ~1000 chars/sec with no visible flicker
