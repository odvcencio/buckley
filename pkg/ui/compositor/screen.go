package compositor

import (
	"sync"

	"github.com/mattn/go-runewidth"
)

// Screen manages a virtual terminal buffer.
type Screen struct {
	mu      sync.RWMutex
	width   int
	height  int
	current [][]Cell

	// Cursor position for input
	cursorX, cursorY int
	cursorVisible    bool
}

// NewScreen creates a new screen buffer with the given dimensions.
func NewScreen(width, height int) *Screen {
	s := &Screen{
		width:  width,
		height: height,
	}
	s.current = s.allocBuffer(width, height)
	return s
}

// allocBuffer creates a new buffer filled with empty cells.
func (s *Screen) allocBuffer(w, h int) [][]Cell {
	buf := make([][]Cell, h)
	for y := range buf {
		buf[y] = make([]Cell, w)
		for x := range buf[y] {
			buf[y][x] = EmptyCell()
		}
	}
	return buf
}

// Resize changes screen dimensions, preserving content where possible.
func (s *Screen) Resize(width, height int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if width == s.width && height == s.height {
		return
	}

	newCurrent := s.allocBuffer(width, height)

	// Copy existing content that fits
	for y := 0; y < min(height, s.height); y++ {
		for x := 0; x < min(width, s.width); x++ {
			newCurrent[y][x] = s.current[y][x]
		}
	}

	s.current = newCurrent
	s.width = width
	s.height = height
}

// Clear resets the current buffer to empty cells.
func (s *Screen) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for y := range s.current {
		for x := range s.current[y] {
			s.current[y][x] = EmptyCell()
		}
	}
}

// Set places a rune at the given position with style.
// Handles wide characters (CJK) by placing a continuation cell.
func (s *Screen) Set(x, y int, r rune, style Style) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.setUnsafe(x, y, r, style)
}

// setUnsafe places a rune without locking (caller must hold lock).
func (s *Screen) setUnsafe(x, y int, r rune, style Style) {
	if x < 0 || x >= s.width || y < 0 || y >= s.height {
		return
	}

	width := runewidth.RuneWidth(r)
	if width == 0 {
		width = 1 // Control characters, treat as single width
	}

	s.current[y][x] = Cell{Rune: r, Width: uint8(width), Style: style}

	// Wide characters occupy two cells; mark second as continuation
	if width == 2 && x+1 < s.width {
		s.current[y][x+1] = Cell{Rune: 0, Width: 0, Style: style}
	}
}

// SetString writes a string starting at position, returns number of columns written.
func (s *Screen) SetString(x, y int, str string, style Style) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	if y < 0 || y >= s.height {
		return 0
	}

	col := x
	for _, r := range str {
		if col >= s.width {
			break
		}
		if col < 0 {
			// Skip characters before visible area, but track position
			col += runewidth.RuneWidth(r)
			continue
		}

		s.setUnsafe(col, y, r, style)
		col += runewidth.RuneWidth(r)
	}

	if col < x {
		return 0
	}
	return col - x
}

// FillRect fills a rectangle with a character and style.
func (s *Screen) FillRect(x, y, w, h int, r rune, style Style) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clamp bounds
	startX := max(0, x)
	startY := max(0, y)
	endX := min(s.width, x+w)
	endY := min(s.height, y+h)

	for row := startY; row < endY; row++ {
		for col := startX; col < endX; col++ {
			s.setUnsafe(col, row, r, style)
		}
	}
}

// FillRectCell fills a rectangle with a specific cell.
func (s *Screen) FillRectCell(x, y, w, h int, cell Cell) {
	s.mu.Lock()
	defer s.mu.Unlock()

	startX := max(0, x)
	startY := max(0, y)
	endX := min(s.width, x+w)
	endY := min(s.height, y+h)

	for row := startY; row < endY; row++ {
		for col := startX; col < endX; col++ {
			s.current[row][col] = cell
		}
	}
}

// HLine draws a horizontal line with a character.
func (s *Screen) HLine(x, y, length int, r rune, style Style) {
	s.FillRect(x, y, length, 1, r, style)
}

// VLine draws a vertical line with a character.
func (s *Screen) VLine(x, y, length int, r rune, style Style) {
	s.FillRect(x, y, 1, length, r, style)
}

// Box draws a box border using Unicode box-drawing characters.
func (s *Screen) Box(x, y, w, h int, style Style) {
	if w < 2 || h < 2 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Corners
	s.setUnsafe(x, y, '┌', style)
	s.setUnsafe(x+w-1, y, '┐', style)
	s.setUnsafe(x, y+h-1, '└', style)
	s.setUnsafe(x+w-1, y+h-1, '┘', style)

	// Top and bottom edges
	for col := x + 1; col < x+w-1; col++ {
		s.setUnsafe(col, y, '─', style)
		s.setUnsafe(col, y+h-1, '─', style)
	}

	// Left and right edges
	for row := y + 1; row < y+h-1; row++ {
		s.setUnsafe(x, row, '│', style)
		s.setUnsafe(x+w-1, row, '│', style)
	}
}

// SetCursor sets the cursor position and visibility.
func (s *Screen) SetCursor(x, y int, visible bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cursorX = x
	s.cursorY = y
	s.cursorVisible = visible
}

// Cursor returns current cursor position and visibility.
func (s *Screen) Cursor() (x, y int, visible bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.cursorX, s.cursorY, s.cursorVisible
}

// Size returns current dimensions.
func (s *Screen) Size() (width, height int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.width, s.height
}

// Get returns the cell at the given position.
// Returns EmptyCell if out of bounds.
func (s *Screen) Get(x, y int) Cell {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if x < 0 || x >= s.width || y < 0 || y >= s.height {
		return EmptyCell()
	}
	return s.current[y][x]
}
