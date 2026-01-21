// Package scrollback provides app-owned conversation history with selection and search.
package scrollback

import (
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/fluffy-ui/backend"
)

// Buffer stores conversation history with efficient scrolling and selection.
type Buffer struct {
	mu sync.RWMutex

	// Content
	lines     []Line
	maxLines  int
	totalRows int // Total rendered rows (accounting for wrapping)

	// Viewport
	width      int
	height     int
	scrollTop  int // First visible line index
	scrollMode ScrollMode

	// Selection
	selStart     Position
	selEnd       Position
	selecting    bool
	hasSelection bool

	// Search
	searchQuery   string
	searchMatches []Position
	searchIndex   int

	// Last message tracking for streaming replacement
	lastMessageStart int
	hasLastMessage   bool

	// Reasoning block tracking
	reasoningStart    int
	reasoningEnd      int
	hasReasoningBlock bool
	reasoningExpanded bool
	reasoningPreview  string
	reasoningFull     string
}

// Span represents a styled run of text.
type Span struct {
	Text  string
	Style backend.Style
}

// WrappedLine represents a wrapped line with optional styling.
type WrappedLine struct {
	Text  string
	Spans []Span
}

// Line represents a single line in the buffer.
type Line struct {
	Content                string
	Style                  LineStyle
	Timestamp              time.Time
	Source                 string // "user", "assistant", "system", "tool"
	Spans                  []Span
	Prefix                 []Span        // Repeated prefix for wrapped lines
	Wrapped                []WrappedLine // Pre-computed wrapped lines
	IsCode                 bool
	IsCodeHeader           bool
	Language               string
	MessageID              int
	CodeLineNumberWidth    int
	CodeLineNumberOptional bool
}

// LineStyle defines visual styling for a line.
type LineStyle struct {
	Prefix string // e.g., ">" for user, "●" for assistant
	FG     uint32 // Foreground color (RGB)
	BG     uint32 // Background color (RGB)
	Bold   bool
	Italic bool
	Dim    bool
}

// Position represents a location in the buffer.
type Position struct {
	Line   int
	Column int
}

// ScrollMode defines scrolling behavior.
type ScrollMode int

const (
	ScrollModeFollow ScrollMode = iota // Auto-scroll to bottom on new content
	ScrollModeManual                   // User is scrolling, don't auto-follow
)

// DefaultMaxLines is the default history limit.
const DefaultMaxLines = 10000

// NewBuffer creates a new scrollback buffer.
func NewBuffer(width, height int) *Buffer {
	return &Buffer{
		maxLines:   DefaultMaxLines,
		width:      width,
		height:     height,
		scrollMode: ScrollModeFollow,
		lines:      make([]Line, 0, 256),
	}
}

// SetMaxLines sets the maximum number of lines to retain.
func (b *Buffer) SetMaxLines(max int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.maxLines = max
	b.trimExcess()
}

// Resize updates viewport dimensions.
func (b *Buffer) Resize(width, height int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if width == b.width && height == b.height {
		return
	}

	b.width = width
	b.height = height

	// Rewrap all lines
	b.rewrapAll()

	// Adjust scroll position
	if b.scrollMode == ScrollModeFollow {
		b.scrollToBottom()
	} else {
		b.clampScroll()
	}
}

// AppendLine adds a new line to the buffer.
func (b *Buffer) AppendLine(content string, style LineStyle, source string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	line := Line{
		Content:   content,
		Style:     style,
		Timestamp: time.Now(),
		Source:    source,
	}
	b.appendLineLocked(line, true)
}

// AppendStyledLine adds a styled line with spans and prefix.
func (b *Buffer) AppendStyledLine(content string, spans, prefix []Span, source string, isCode bool, language string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	line := Line{
		Content:   content,
		Timestamp: time.Now(),
		Source:    source,
		Spans:     spans,
		Prefix:    prefix,
		IsCode:    isCode,
		Language:  language,
	}
	b.appendLineLocked(line, true)
}

// AppendAuxLine appends a line without marking it as the last message.
func (b *Buffer) AppendAuxLine(content string, style LineStyle, source string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	line := Line{
		Content:   content,
		Style:     style,
		Timestamp: time.Now(),
		Source:    source,
	}
	b.appendLineLocked(line, false)
}

// AppendMessage appends multiple lines as a single message.
func (b *Buffer) AppendMessage(lines []Line) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.lastMessageStart = len(b.lines)
	b.hasLastMessage = true
	for _, line := range lines {
		b.appendLineLocked(line, false)
	}
}

// ReplaceLastMessage replaces the last message's lines with new ones.
func (b *Buffer) ReplaceLastMessage(lines []Line) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.hasLastMessage {
		b.lastMessageStart = len(b.lines)
		b.hasLastMessage = true
	}

	if b.lastMessageStart < len(b.lines) {
		for i := b.lastMessageStart; i < len(b.lines); i++ {
			b.totalRows -= len(b.lines[i].Wrapped)
		}
		b.lines = b.lines[:b.lastMessageStart]
	}

	if b.hasSelection && (b.selStart.Line >= b.lastMessageStart || b.selEnd.Line >= b.lastMessageStart) {
		b.selecting = false
		b.hasSelection = false
		b.selStart = Position{}
		b.selEnd = Position{}
	}

	for _, line := range lines {
		b.appendLineLocked(line, false)
	}
}

// AppendText appends text to the last line (for streaming).
func (b *Buffer) AppendText(text string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.lines) == 0 {
		return
	}

	last := &b.lines[len(b.lines)-1]
	oldRows := len(last.Wrapped)

	last.Content += text
	if len(last.Spans) > 0 {
		lastSpan := &last.Spans[len(last.Spans)-1]
		lastSpan.Text += text
	}
	last.Wrapped = wrapLineWithSpans(last.Content, last.Spans, last.Prefix, b.width-2, last.IsCode)

	b.totalRows += len(last.Wrapped) - oldRows

	if b.scrollMode == ScrollModeFollow {
		b.scrollToBottom()
	}
}

func (b *Buffer) appendLineLocked(line Line, trackMessage bool) {
	if trackMessage {
		b.lastMessageStart = len(b.lines)
		b.hasLastMessage = true
	}

	if line.Timestamp.IsZero() {
		line.Timestamp = time.Now()
	}
	line.Wrapped = wrapLineWithSpans(line.Content, line.Spans, line.Prefix, b.width-2, line.IsCode)

	b.lines = append(b.lines, line)
	b.totalRows += len(line.Wrapped)

	b.trimExcess()

	// Auto-scroll if following
	if b.scrollMode == ScrollModeFollow {
		b.scrollToBottom()
	}
}

// Clear removes all content.
func (b *Buffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.lines = b.lines[:0]
	b.totalRows = 0
	b.scrollTop = 0
	// Clear selection inline (already holding lock)
	b.selecting = false
	b.hasSelection = false
	b.selStart = Position{}
	b.selEnd = Position{}
	b.searchQuery = ""
	b.searchMatches = nil
	b.lastMessageStart = 0
	b.hasLastMessage = false
}

// AppendReasoningLine appends a reasoning line (dimmed, collapsible).
func (b *Buffer) AppendReasoningLine(content string, style LineStyle) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.hasReasoningBlock {
		b.reasoningStart = len(b.lines)
		b.hasReasoningBlock = true
	}

	line := Line{
		Content:   content,
		Style:     style,
		Timestamp: time.Now(),
		Source:    "reasoning",
	}
	b.appendLineLocked(line, false)
	b.reasoningEnd = len(b.lines)
}

// ReplaceReasoningBlock replaces streaming reasoning with collapsed preview.
func (b *Buffer) ReplaceReasoningBlock(preview, full string, collapsedStyle LineStyle) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.hasReasoningBlock {
		return
	}

	// Remove all reasoning lines and update totalRows
	if b.reasoningStart < len(b.lines) {
		for i := b.reasoningStart; i < len(b.lines); i++ {
			b.totalRows -= len(b.lines[i].Wrapped)
		}
		b.lines = b.lines[:b.reasoningStart]
	}

	// Add collapsed preview line
	b.reasoningPreview = preview
	b.reasoningFull = full
	b.reasoningExpanded = false

	// Only show "..." if the text was actually truncated
	suffix := ""
	if len(full) > len(preview) {
		suffix = "..."
	}
	collapsedText := "▶ \"" + preview + suffix + "\" (click to expand)"
	line := Line{
		Content:   collapsedText,
		Style:     collapsedStyle,
		Timestamp: time.Now(),
		Source:    "reasoning-collapsed",
	}
	b.appendLineLocked(line, false)
	b.reasoningEnd = len(b.lines)
}

// ClearReasoningBlock clears reasoning block tracking.
func (b *Buffer) ClearReasoningBlock() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hasReasoningBlock = false
	b.reasoningExpanded = false
	b.reasoningPreview = ""
	b.reasoningFull = ""
}

// ToggleReasoningBlock toggles reasoning between collapsed and expanded states.
// Returns true if a toggle occurred.
func (b *Buffer) ToggleReasoningBlock(expandedStyle, collapsedStyle LineStyle) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.hasReasoningBlock || b.reasoningFull == "" {
		return false
	}

	// Find and remove the current reasoning display line
	if b.reasoningStart < len(b.lines) {
		// Update totalRows before removing
		for i := b.reasoningStart; i < len(b.lines) && i < b.reasoningEnd; i++ {
			b.totalRows -= len(b.lines[i].Wrapped)
		}
		b.lines = b.lines[:b.reasoningStart]
	}

	if b.reasoningExpanded {
		// Collapse: show preview
		suffix := ""
		if len(b.reasoningFull) > len(b.reasoningPreview) {
			suffix = "..."
		}
		collapsedText := "▶ \"" + b.reasoningPreview + suffix + "\" (click to expand)"
		line := Line{
			Content:   collapsedText,
			Style:     collapsedStyle,
			Timestamp: time.Now(),
			Source:    "reasoning-collapsed",
		}
		b.appendLineLocked(line, false)
		b.reasoningExpanded = false
	} else {
		// Expand: show full reasoning with gutter
		expandedHeader := "▼ \"" + b.reasoningPreview + "\" (click to collapse)"
		headerLine := Line{
			Content:   expandedHeader,
			Style:     collapsedStyle,
			Timestamp: time.Now(),
			Source:    "reasoning-header",
		}
		b.appendLineLocked(headerLine, false)

		// Add reasoning content lines with gutter
		lines := strings.Split(b.reasoningFull, "\n")
		for _, content := range lines {
			gutterLine := Line{
				Content:   "│ " + content,
				Style:     expandedStyle,
				Timestamp: time.Now(),
				Source:    "reasoning-expanded",
			}
			b.appendLineLocked(gutterLine, false)
		}
		b.reasoningExpanded = true
	}

	b.reasoningEnd = len(b.lines)
	return true
}

// IsReasoningLine returns true if the line at the given index is part of a reasoning block.
func (b *Buffer) IsReasoningLine(lineIdx int) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.hasReasoningBlock {
		return false
	}
	return lineIdx >= b.reasoningStart && lineIdx < b.reasoningEnd
}

// RemoveLastLineIfSource removes the last line if it has the given source.
// Returns true if a line was removed.
func (b *Buffer) RemoveLastLineIfSource(source string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.lines) == 0 {
		return false
	}

	last := b.lines[len(b.lines)-1]
	if last.Source != source {
		return false
	}

	// Remove the line
	b.totalRows -= len(last.Wrapped)
	b.lines = b.lines[:len(b.lines)-1]

	// Adjust scroll if needed
	if b.scrollMode == ScrollModeFollow {
		b.scrollToBottom()
	} else {
		b.clampScroll()
	}

	if b.hasLastMessage && b.lastMessageStart >= len(b.lines) {
		b.hasLastMessage = false
		b.lastMessageStart = 0
	}

	return true
}

// ScrollUp moves viewport up by n lines.
func (b *Buffer) ScrollUp(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.scrollMode = ScrollModeManual
	b.scrollTop -= n
	b.clampScroll()
}

// ScrollDown moves viewport down by n lines.
func (b *Buffer) ScrollDown(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.scrollTop += n
	b.clampScroll()

	// Resume following if at bottom
	if b.scrollTop >= b.totalRows-b.height {
		b.scrollMode = ScrollModeFollow
	}
}

// ScrollToTop moves to the beginning.
func (b *Buffer) ScrollToTop() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.scrollMode = ScrollModeManual
	b.scrollTop = 0
}

// ScrollToBottom moves to the end.
func (b *Buffer) ScrollToBottom() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.scrollToBottom()
	b.scrollMode = ScrollModeFollow
}

// PageUp scrolls up by viewport height.
func (b *Buffer) PageUp() {
	b.ScrollUp(b.height - 1)
}

// PageDown scrolls down by viewport height.
func (b *Buffer) PageDown() {
	b.ScrollDown(b.height - 1)
}

// StartSelection begins text selection at position.
func (b *Buffer) StartSelection(line, col int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.selStart = Position{Line: line, Column: col}
	b.selEnd = b.selStart
	b.selecting = true
	b.hasSelection = false
}

// UpdateSelection extends selection to position.
func (b *Buffer) UpdateSelection(line, col int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.selecting {
		return
	}

	b.selEnd = Position{Line: line, Column: col}
	b.hasSelection = b.selStart != b.selEnd
}

// EndSelection finishes selection.
func (b *Buffer) EndSelection() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.selecting = false
}

// ClearSelection removes selection.
func (b *Buffer) ClearSelection() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.selecting = false
	b.hasSelection = false
	b.selStart = Position{}
	b.selEnd = Position{}
}

// GetSelection returns selected text.
func (b *Buffer) GetSelection() string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if !b.hasSelection {
		return ""
	}

	start, end := b.selStart, b.selEnd
	if start.Line > end.Line || (start.Line == end.Line && start.Column > end.Column) {
		start, end = end, start
	}

	var result strings.Builder

	for i := start.Line; i <= end.Line && i < len(b.lines); i++ {
		line := b.lines[i].Content

		startCol := 0
		endCol := len(line)

		if i == start.Line {
			startCol = min(start.Column, len(line))
		}
		if i == end.Line {
			endCol = min(end.Column, len(line))
		}

		if startCol < endCol {
			result.WriteString(line[startCol:endCol])
		}

		if i < end.Line {
			result.WriteRune('\n')
		}
	}

	return result.String()
}

// HasSelection returns true if text is selected.
func (b *Buffer) HasSelection() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.hasSelection
}

// Search finds matches for query.
func (b *Buffer) Search(query string) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.searchQuery = query
	b.searchMatches = nil
	b.searchIndex = 0

	if query == "" {
		return 0
	}

	queryLower := strings.ToLower(query)

	for i, line := range b.lines {
		contentLower := strings.ToLower(line.Content)
		offset := 0
		for {
			idx := strings.Index(contentLower[offset:], queryLower)
			if idx == -1 {
				break
			}
			b.searchMatches = append(b.searchMatches, Position{
				Line:   i,
				Column: offset + idx,
			})
			offset += idx + len(query)
		}
	}

	return len(b.searchMatches)
}

// NextMatch moves to next search match.
func (b *Buffer) NextMatch() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.searchMatches) == 0 {
		return false
	}

	b.searchIndex = (b.searchIndex + 1) % len(b.searchMatches)
	b.scrollToMatch(b.searchMatches[b.searchIndex])
	return true
}

// PrevMatch moves to previous search match.
func (b *Buffer) PrevMatch() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.searchMatches) == 0 {
		return false
	}

	b.searchIndex--
	if b.searchIndex < 0 {
		b.searchIndex = len(b.searchMatches) - 1
	}
	b.scrollToMatch(b.searchMatches[b.searchIndex])
	return true
}

// SearchMatches returns current match info.
func (b *Buffer) SearchMatches() (current, total int) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if len(b.searchMatches) == 0 {
		return 0, 0
	}
	return b.searchIndex + 1, len(b.searchMatches)
}

// LatestCodeBlock returns the most recent code block content.
func (b *Buffer) LatestCodeBlock() (language, code string, ok bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	end := -1
	for i := len(b.lines) - 1; i >= 0; i-- {
		if b.lines[i].IsCode {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", false
	}

	start := end
	for start-1 >= 0 && b.lines[start-1].IsCode {
		start--
	}

	for i := start; i <= end; i++ {
		if b.lines[i].Language != "" {
			language = b.lines[i].Language
			break
		}
	}

	lines := make([]string, 0, end-start+1)
	for i := start; i <= end; i++ {
		if b.lines[i].IsCodeHeader {
			continue
		}
		lines = append(lines, b.lines[i].Content)
	}
	if len(lines) == 0 {
		return language, "", false
	}

	return language, strings.Join(lines, "\n"), true
}

// CodeBlockAt returns the code block content containing the given line index.
func (b *Buffer) CodeBlockAt(lineIndex int) (language, code string, ok bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if lineIndex < 0 || lineIndex >= len(b.lines) {
		return "", "", false
	}
	if !b.lines[lineIndex].IsCode {
		return "", "", false
	}

	start := lineIndex
	for start-1 >= 0 && b.lines[start-1].IsCode {
		start--
	}
	end := lineIndex
	for end+1 < len(b.lines) && b.lines[end+1].IsCode {
		end++
	}

	for i := start; i <= end; i++ {
		if b.lines[i].Language != "" {
			language = b.lines[i].Language
			break
		}
	}

	lines := make([]string, 0, end-start+1)
	for i := start; i <= end; i++ {
		if b.lines[i].IsCodeHeader {
			continue
		}
		lines = append(lines, b.lines[i].Content)
	}
	if len(lines) == 0 {
		return language, "", false
	}

	return language, strings.Join(lines, "\n"), true
}

// CodeBlockRange returns the start and end line indices for a code block.
func (b *Buffer) CodeBlockRange(lineIndex int) (start, end int, ok bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if lineIndex < 0 || lineIndex >= len(b.lines) {
		return 0, 0, false
	}
	if !b.lines[lineIndex].IsCode {
		return 0, 0, false
	}

	start = lineIndex
	for start-1 >= 0 && b.lines[start-1].IsCode {
		start--
	}
	end = lineIndex
	for end+1 < len(b.lines) && b.lines[end+1].IsCode {
		end++
	}
	return start, end, true
}

// GetVisibleLines returns lines currently in viewport.
func (b *Buffer) GetVisibleLines() []VisibleLine {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var result []VisibleLine
	rowIndex := 0

	for lineIdx, line := range b.lines {
		for wrapIdx, wrapped := range line.Wrapped {
			if rowIndex >= b.scrollTop && rowIndex < b.scrollTop+b.height {
				vl := VisibleLine{
					Content:                wrapped.Text,
					Spans:                  wrapped.Spans,
					Style:                  line.Style,
					Source:                 line.Source,
					LineIndex:              lineIdx,
					WrapIndex:              wrapIdx,
					RowIndex:               rowIndex - b.scrollTop,
					IsCode:                 line.IsCode,
					Language:               line.Language,
					MessageID:              line.MessageID,
					CodeLineNumberWidth:    line.CodeLineNumberWidth,
					CodeLineNumberOptional: line.CodeLineNumberOptional,
				}

				// Check selection
				if b.hasSelection {
					vl.Selected = b.isRowSelected(lineIdx, wrapIdx, wrapped.Text)
				}

				// Check search highlight
				if b.searchQuery != "" {
					vl.SearchHighlights = b.getSearchHighlights(lineIdx, wrapIdx, wrapped.Text)
				}

				result = append(result, vl)
			}
			rowIndex++

			if rowIndex >= b.scrollTop+b.height {
				return result
			}
		}
	}

	return result
}

// VisibleLineAt returns the visible line at the given viewport row.
func (b *Buffer) VisibleLineAt(row int) (VisibleLine, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if row < 0 || row >= b.height {
		return VisibleLine{}, false
	}

	absoluteRow := b.scrollTop + row
	if absoluteRow < 0 || absoluteRow >= b.totalRows {
		return VisibleLine{}, false
	}

	rowIndex := 0
	for lineIdx, line := range b.lines {
		for wrapIdx, wrapped := range line.Wrapped {
			if rowIndex == absoluteRow {
				return VisibleLine{
					Content:                wrapped.Text,
					Spans:                  wrapped.Spans,
					Style:                  line.Style,
					Source:                 line.Source,
					LineIndex:              lineIdx,
					WrapIndex:              wrapIdx,
					RowIndex:               row,
					IsCode:                 line.IsCode,
					Language:               line.Language,
					MessageID:              line.MessageID,
					CodeLineNumberWidth:    line.CodeLineNumberWidth,
					CodeLineNumberOptional: line.CodeLineNumberOptional,
				}, true
			}
			rowIndex++
			if rowIndex > absoluteRow {
				break
			}
		}
	}

	return VisibleLine{}, false
}

// LineAt returns the raw line at the given index.
func (b *Buffer) LineAt(index int) (Line, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if index < 0 || index >= len(b.lines) {
		return Line{}, false
	}
	return b.lines[index], true
}

// VisibleLine represents a line ready for rendering.
type VisibleLine struct {
	Content                string
	Spans                  []Span
	Style                  LineStyle
	Source                 string // "user", "assistant", "system", "tool"
	LineIndex              int    // Original line index
	WrapIndex              int    // Wrap segment index
	RowIndex               int    // Row in viewport (0 = top)
	Selected               bool
	SearchHighlights       []int // Column indices of search matches
	IsCode                 bool
	Language               string
	MessageID              int
	CodeLineNumberWidth    int
	CodeLineNumberOptional bool
}

// LineCount returns total line count.
func (b *Buffer) LineCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.lines)
}

// GetAllContent returns all text content from the buffer.
func (b *Buffer) GetAllContent(limit int) []string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if limit <= 0 || limit > len(b.lines) {
		limit = len(b.lines)
	}

	// Get the last N lines
	start := len(b.lines) - limit
	if start < 0 {
		start = 0
	}

	result := make([]string, 0, limit)
	for i := start; i < len(b.lines); i++ {
		result = append(result, b.lines[i].Content)
	}
	return result
}

// RowCount returns total rendered row count.
func (b *Buffer) RowCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.totalRows
}

// ScrollPosition returns current scroll info.
func (b *Buffer) ScrollPosition() (top, total, viewHeight int) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.scrollTop, b.totalRows, b.height
}

// PositionForView maps a viewport row/column to a line/column position.
func (b *Buffer) PositionForView(row, col int) (line, column int, ok bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if row < 0 || row >= b.height || col < 0 {
		return 0, 0, false
	}

	absoluteRow := b.scrollTop + row
	if absoluteRow < 0 || absoluteRow >= b.totalRows {
		return 0, 0, false
	}

	rowIndex := 0
	for lineIdx, line := range b.lines {
		prefixLen := len([]rune(spansText(line.Prefix)))
		contentOffset := 0

		for _, wrapped := range line.Wrapped {
			if rowIndex == absoluteRow {
				wrappedLen := len([]rune(wrapped.Text))
				segmentLen := wrappedLen - prefixLen
				if segmentLen < 0 {
					segmentLen = 0
				}
				contentCol := col
				if contentCol < prefixLen {
					contentCol = 0
				} else {
					contentCol -= prefixLen
				}
				if contentCol > segmentLen {
					contentCol = segmentLen
				}

				position := contentOffset + contentCol
				if position < 0 {
					position = 0
				}
				return lineIdx, runeOffsetToByte(line.Content, position), true
			}

			wrappedLen := len([]rune(wrapped.Text))
			segmentLen := wrappedLen - prefixLen
			if segmentLen < 0 {
				segmentLen = 0
			}
			contentOffset += segmentLen
			rowIndex++
		}
	}

	return 0, 0, false
}

// IsFollowing returns true if auto-scrolling.
func (b *Buffer) IsFollowing() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.scrollMode == ScrollModeFollow
}

// Internal helpers

func (b *Buffer) scrollToBottom() {
	b.scrollTop = max(0, b.totalRows-b.height)
}

func (b *Buffer) clampScroll() {
	maxScroll := max(0, b.totalRows-b.height)
	b.scrollTop = max(0, min(b.scrollTop, maxScroll))
}

func (b *Buffer) trimExcess() {
	for len(b.lines) > b.maxLines {
		removed := b.lines[0]
		b.totalRows -= len(removed.Wrapped)
		b.lines = b.lines[1:]
		if b.hasLastMessage {
			b.lastMessageStart--
			if b.lastMessageStart < 0 {
				b.lastMessageStart = 0
				b.hasLastMessage = false
			}
		}

		// Adjust selection indices
		if b.hasSelection {
			b.selStart.Line--
			b.selEnd.Line--
			if b.selStart.Line < 0 || b.selEnd.Line < 0 {
				// Clear selection inline (already holding lock)
				b.selecting = false
				b.hasSelection = false
				b.selStart = Position{}
				b.selEnd = Position{}
			}
		}
	}
}

func (b *Buffer) rewrapAll() {
	b.totalRows = 0
	for i := range b.lines {
		b.lines[i].Wrapped = wrapLineWithSpans(b.lines[i].Content, b.lines[i].Spans, b.lines[i].Prefix, b.width-2, b.lines[i].IsCode)
		b.totalRows += len(b.lines[i].Wrapped)
	}
}

func (b *Buffer) scrollToMatch(pos Position) {
	// Convert line position to row position
	rowIndex := 0
	for i := 0; i < pos.Line && i < len(b.lines); i++ {
		rowIndex += len(b.lines[i].Wrapped)
	}

	// Center the match in viewport
	b.scrollTop = max(0, rowIndex-b.height/2)
	b.clampScroll()
	b.scrollMode = ScrollModeManual
}

func (b *Buffer) isRowSelected(lineIdx, wrapIdx int, content string) bool {
	// Simplified selection check
	start, end := b.selStart, b.selEnd
	if start.Line > end.Line {
		start, end = end, start
	}

	return lineIdx >= start.Line && lineIdx <= end.Line
}

func (b *Buffer) getSearchHighlights(lineIdx, wrapIdx int, content string) []int {
	var highlights []int
	queryLower := strings.ToLower(b.searchQuery)
	contentLower := strings.ToLower(content)

	offset := 0
	for {
		idx := strings.Index(contentLower[offset:], queryLower)
		if idx == -1 {
			break
		}
		highlights = append(highlights, offset+idx)
		offset += idx + 1
	}

	return highlights
}

// wrapLineWithSpans wraps text to width, preserving spans and prefix.
func wrapLineWithSpans(text string, spans, prefix []Span, width int, isCode bool) []WrappedLine {
	if width <= 0 {
		return []WrappedLine{{Text: text, Spans: spans}}
	}

	if len(spans) == 0 && len(prefix) == 0 {
		return wrapPlainLine(text, width, isCode)
	}
	return wrapStyledLine(text, spans, prefix, width, isCode)
}

func wrapPlainLine(text string, width int, isCode bool) []WrappedLine {
	if len(text) == 0 {
		return []WrappedLine{{Text: ""}}
	}

	var lines []WrappedLine
	runes := []rune(text)
	wrapAtWord := !isCode
	trimSpaces := !isCode

	for len(runes) > 0 {
		end := min(len(runes), width)
		if wrapAtWord && end < len(runes) {
			for i := end - 1; i > end-20 && i > 0; i-- {
				if runes[i] == ' ' {
					end = i + 1
					break
				}
			}
		}

		lines = append(lines, WrappedLine{Text: string(runes[:end])})
		runes = runes[end:]

		if trimSpaces {
			for len(runes) > 0 && runes[0] == ' ' {
				runes = runes[1:]
			}
		}
	}

	if len(lines) == 0 {
		lines = []WrappedLine{{Text: ""}}
	}
	return lines
}

type styledRune struct {
	r     rune
	style backend.Style
}

func wrapStyledLine(text string, spans, prefix []Span, width int, isCode bool) []WrappedLine {
	prefixText := spansText(prefix)
	prefixWidth := len([]rune(prefixText))
	available := max(1, width-prefixWidth)

	runes := styledRunesFromSpans(text, spans)
	if len(runes) == 0 && text == "" {
		return []WrappedLine{{Text: prefixText, Spans: append([]Span{}, prefix...)}}
	}

	var lines []WrappedLine
	wrapAtWord := !isCode
	trimSpaces := !isCode

	for len(runes) > 0 {
		end := min(len(runes), available)
		if wrapAtWord && end < len(runes) {
			for i := end - 1; i > end-20 && i > 0; i-- {
				if runes[i].r == ' ' {
					end = i + 1
					break
				}
			}
		}

		segment := runes[:end]
		runes = runes[end:]

		if trimSpaces {
			for len(runes) > 0 && runes[0].r == ' ' {
				runes = runes[1:]
			}
		}

		lineSpans := make([]Span, 0, len(prefix)+len(segment))
		if len(prefix) > 0 {
			lineSpans = append(lineSpans, prefix...)
		}
		lineSpans = append(lineSpans, spansFromRunes(segment)...)
		lines = append(lines, WrappedLine{Text: spansText(lineSpans), Spans: lineSpans})
	}

	if len(lines) == 0 {
		lines = []WrappedLine{{Text: prefixText, Spans: append([]Span{}, prefix...)}}
	}
	return lines
}

func styledRunesFromSpans(text string, spans []Span) []styledRune {
	if len(spans) == 0 {
		out := make([]styledRune, 0, len(text))
		for _, r := range []rune(text) {
			out = append(out, styledRune{r: r, style: backend.DefaultStyle()})
		}
		return out
	}
	var out []styledRune
	for _, span := range spans {
		for _, r := range []rune(span.Text) {
			out = append(out, styledRune{r: r, style: span.Style})
		}
	}
	return out
}

func spansFromRunes(runes []styledRune) []Span {
	if len(runes) == 0 {
		return nil
	}
	var spans []Span
	current := Span{Style: runes[0].style}
	for _, sr := range runes {
		if current.Style == sr.style {
			current.Text += string(sr.r)
			continue
		}
		spans = append(spans, current)
		current = Span{Style: sr.style, Text: string(sr.r)}
	}
	spans = append(spans, current)
	return spans
}

func spansText(spans []Span) string {
	if len(spans) == 0 {
		return ""
	}
	var b strings.Builder
	for _, span := range spans {
		b.WriteString(span.Text)
	}
	return b.String()
}

func runeOffsetToByte(s string, offset int) int {
	if offset <= 0 {
		return 0
	}
	count := 0
	for i := range s {
		if count == offset {
			return i
		}
		count++
	}
	return len(s)
}
