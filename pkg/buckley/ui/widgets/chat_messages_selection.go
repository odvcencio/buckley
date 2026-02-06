package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/runtime"
)

// PositionForPoint maps screen coordinates to a buffer position.
func (m *ChatMessages) PositionForPoint(x, y int) (line, col int, ok bool) {
	bounds := m.listBounds
	// Fallback to widget bounds if listBounds is not set
	if bounds.Width <= 0 || bounds.Height <= 0 {
		bounds = m.Bounds()
	}
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return 0, 0, false
	}
	if x < bounds.X || y < bounds.Y || y >= bounds.Y+bounds.Height {
		return 0, 0, false
	}
	if x >= bounds.X+bounds.Width {
		return 0, 0, false
	}
	row := y - bounds.Y
	column := x - bounds.X
	return m.buffer.PositionForView(row, column)
}

// CodeHeaderActionAtPoint returns a code header action if the point targets copy/open.
func (m *ChatMessages) CodeHeaderActionAtPoint(x, y int) (action, language, code string, ok bool) {
	lineIndex, col, ok := m.PositionForPoint(x, y)
	if !ok {
		return "", "", "", false
	}

	line, ok := m.buffer.LineAt(lineIndex)
	if !ok || !line.IsCodeHeader {
		return "", "", "", false
	}
	header := line.Content
	if header == "" {
		return "", "", "", false
	}

	copyIdx := strings.Index(header, "[copy]")
	if copyIdx >= 0 && col >= copyIdx && col < copyIdx+len("[copy]") {
		language, code, ok = m.buffer.CodeBlockAt(lineIndex)
		if !ok {
			return "", "", "", false
		}
		return "copy", language, code, true
	}

	openIdx := strings.Index(header, "[open]")
	if openIdx >= 0 && col >= openIdx && col < openIdx+len("[open]") {
		language, code, _ = m.buffer.CodeBlockAt(lineIndex)
		return "open", language, code, true
	}

	return "", "", "", false
}

// StartSelection begins text selection.
func (m *ChatMessages) StartSelection(line, col int) {
	m.buffer.StartSelection(line, col)
	m.updateSelectionSignals(true, "")
}

// UpdateSelection extends the selection.
func (m *ChatMessages) UpdateSelection(line, col int) {
	m.buffer.UpdateSelection(line, col)
}

// EndSelection finishes selection.
func (m *ChatMessages) EndSelection() {
	m.buffer.EndSelection()
	m.updateSelectionSignals(m.buffer.HasSelection(), m.buffer.GetSelection())
}

// ClearSelection clears any active selection.
func (m *ChatMessages) ClearSelection() {
	m.buffer.ClearSelection()
	m.updateSelectionSignals(false, "")
}

// HasSelection returns true if a selection exists.
func (m *ChatMessages) HasSelection() bool {
	return m.buffer.HasSelection()
}

// SelectionText returns the selected text.
func (m *ChatMessages) SelectionText() string {
	return m.buffer.GetSelection()
}

func (m *ChatMessages) startSelectionAt(x, y int) bool {
	line, col, ok := m.PositionForPoint(x, y)
	if !ok {
		return false
	}
	m.buffer.StartSelection(line, col)
	m.selecting = true
	m.updateSelectionSignals(true, "")
	m.requestInvalidate()
	return true
}

func (m *ChatMessages) updateSelectionAt(x, y int) bool {
	line, col, ok := m.PositionForPoint(x, y)
	if !ok {
		return false
	}
	m.buffer.UpdateSelection(line, col)
	m.requestInvalidate()
	return true
}

func (m *ChatMessages) endSelection() {
	if m.selecting {
		m.buffer.EndSelection()
		m.selecting = false
		m.updateSelectionSignals(m.buffer.HasSelection(), m.buffer.GetSelection())
		m.requestInvalidate()
	}
}

func (m *ChatMessages) clearHover() runtime.HandleResult {
	if m.hoveredMessageID == 0 && m.hoveredCodeStart == -1 && m.hoveredCodeEnd == -1 {
		return runtime.Unhandled()
	}
	m.hoveredMessageID = 0
	m.hoveredCodeStart = -1
	m.hoveredCodeEnd = -1
	m.requestInvalidate()
	return runtime.Handled()
}

func (m *ChatMessages) updateSelectionSignals(active bool, text string) {
	if m == nil {
		return
	}
	if m.selectionActiveSig != nil {
		m.selectionActiveSig.Set(active)
	}
	if m.selectionTextSig != nil {
		m.selectionTextSig.Set(text)
	}
}
