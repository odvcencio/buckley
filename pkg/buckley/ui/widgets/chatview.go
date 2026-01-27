package widgets

import (
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/buckley/ui/scrollback"
	"github.com/odvcencio/fluffy-ui/accessibility"
	"github.com/odvcencio/fluffy-ui/backend"
	"github.com/odvcencio/fluffy-ui/clipboard"
	"github.com/odvcencio/fluffy-ui/markdown"
	"github.com/odvcencio/fluffy-ui/runtime"
	"github.com/odvcencio/fluffy-ui/scroll"
	uistyle "github.com/odvcencio/fluffy-ui/style"
	"github.com/odvcencio/fluffy-ui/terminal"
	uiwidgets "github.com/odvcencio/fluffy-ui/widgets"
)

// ChatView displays the conversation history with scrolling.
type ChatView struct {
	uiwidgets.Base
	buffer *scrollback.Buffer
	list   *uiwidgets.VirtualList[scrollback.VisibleLine]

	layout     *runtime.Flex
	listBounds runtime.Rect
	services   runtime.Services

	// Styles for different message types
	userStyle      backend.Style
	assistantStyle backend.Style
	systemStyle    backend.Style
	toolStyle      backend.Style
	thinkingStyle  backend.Style

	// UI styles
	scrollbarStyle backend.Style
	scrollThumb    backend.Style
	selectionStyle backend.Style
	searchStyle    backend.Style
	bgStyle        backend.Style

	// Markdown rendering
	mdRenderer  *markdown.Renderer
	codeBlockBG backend.Style
	lastSource  string
	lastContent string
	lastMessage time.Time
	lastUserAt  time.Time

	metadataStyle backend.Style
	metadataMode  string
	modelName     string

	reasoningPanel     *uiwidgets.Panel
	reasoningAccordion *uiwidgets.Accordion
	reasoningSection   *uiwidgets.AccordionSection
	reasoningText      *TextBlock
	reasoningVisible   bool
	reasoningPreview   string
	reasoningBuilder   strings.Builder

	nextMessageID    int
	lastMessageID    int
	messages         map[int]messageEntry
	hoveredMessageID int
	hoveredCodeStart int
	hoveredCodeEnd   int

	// Callbacks
	onScrollChange func(top, total, viewHeight int)
}

type chatListAdapter struct {
	chat *ChatView
}

func (a chatListAdapter) Count() int {
	if a.chat == nil || a.chat.buffer == nil {
		return 0
	}
	return a.chat.buffer.RowCount()
}

func (a chatListAdapter) Item(index int) scrollback.VisibleLine {
	if a.chat == nil || a.chat.buffer == nil {
		return scrollback.VisibleLine{}
	}
	line, _ := a.chat.buffer.VisibleLineAtRow(index)
	return line
}

func (a chatListAdapter) Render(item scrollback.VisibleLine, index int, selected bool, ctx runtime.RenderContext) {
	if a.chat == nil {
		return
	}
	a.chat.renderVisibleLine(ctx, item)
}

type messageEntry struct {
	ID      int
	Content string
	Source  string
	Time    time.Time
	Model   string
	UserAt  time.Time
}

// NewChatView creates a new chat view widget.
func NewChatView() *ChatView {
	// Use larger default buffer size - Layout will resize to actual terminal dimensions.
	// This prevents content added before first Layout from being wrapped too narrow.
	chat := &ChatView{
		buffer:           scrollback.NewBuffer(200, 50),
		userStyle:        backend.DefaultStyle(),
		assistantStyle:   backend.DefaultStyle(),
		systemStyle:      backend.DefaultStyle().Dim(true),
		toolStyle:        backend.DefaultStyle(),
		thinkingStyle:    backend.DefaultStyle().Dim(true).Italic(true),
		scrollbarStyle:   backend.DefaultStyle(),
		scrollThumb:      backend.DefaultStyle(),
		selectionStyle:   backend.DefaultStyle().Reverse(true),
		searchStyle:      backend.DefaultStyle().Reverse(true),
		bgStyle:          backend.DefaultStyle(),
		metadataStyle:    backend.DefaultStyle().Dim(true),
		metadataMode:     "always",
		messages:         make(map[int]messageEntry),
		hoveredCodeStart: -1,
		hoveredCodeEnd:   -1,
	}
	chat.list = uiwidgets.NewVirtualList[scrollback.VisibleLine](chatListAdapter{chat: chat})
	chat.list.SetItemHeight(1)
	chat.list.SetOverscan(4)
	chat.list.SetLabel("Chat messages")

	chat.reasoningText = NewTextBlock("")
	chat.reasoningSection = uiwidgets.NewAccordionSection("Reasoning", chat.reasoningText, uiwidgets.WithSectionExpanded(false))
	chat.reasoningAccordion = uiwidgets.NewAccordion(chat.reasoningSection)
	chat.reasoningAccordion.SetAllowMultiple(false)
	chat.reasoningPanel = uiwidgets.NewPanel(chat.reasoningAccordion).WithBorder(backend.DefaultStyle())
	chat.reasoningPanel.SetTitle("Reasoning")
	chat.rebuildLayout()
	return chat
}

// SetStyles configures the message styles.
func (c *ChatView) SetStyles(user, assistant, system, tool, thinking backend.Style) {
	c.userStyle = user
	c.assistantStyle = assistant
	c.systemStyle = system
	c.toolStyle = tool
	c.thinkingStyle = thinking
	if c.reasoningText != nil {
		c.reasoningText.SetStyle(thinking)
	}
}

// SetUIStyles configures scrollbar, selection, and background styles.
func (c *ChatView) SetUIStyles(scrollbar, thumb, selection, search, background backend.Style) {
	c.scrollbarStyle = scrollbar
	c.scrollThumb = thumb
	c.selectionStyle = selection
	c.searchStyle = search
	c.bgStyle = background
	if c.list != nil {
		c.list.SetStyle(background)
		c.list.SetSelectedStyle(selection)
	}
	if c.reasoningPanel != nil {
		c.reasoningPanel.SetStyle(background)
		c.reasoningPanel.WithBorder(scrollbar)
	}
}

// SetMarkdownRenderer configures markdown rendering for the chat view.
func (c *ChatView) SetMarkdownRenderer(renderer *markdown.Renderer, codeBlockBG backend.Style) {
	c.mdRenderer = renderer
	c.codeBlockBG = codeBlockBG
}

// SetMetadataStyle configures the metadata line style.
func (c *ChatView) SetMetadataStyle(style backend.Style) {
	c.metadataStyle = style
}

// SetMessageMetadataMode configures metadata visibility ("always" or "never").
func (c *ChatView) SetMessageMetadataMode(mode string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "always", "hover", "never":
	default:
		mode = "always"
	}
	c.metadataMode = mode
}

// SetModelName updates the model name for metadata display.
func (c *ChatView) SetModelName(name string) {
	c.modelName = strings.TrimSpace(name)
}

// OnScrollChange sets a callback for scroll position changes.
func (c *ChatView) OnScrollChange(fn func(top, total, viewHeight int)) {
	c.onScrollChange = fn
}

// AddMessage adds a message to the chat.
func (c *ChatView) AddMessage(content, source string) {
	switch source {
	case "thinking":
		c.buffer.AppendAuxLine("  ◦ thinking...", scrollback.LineStyle{
			FG:     extractFG(c.thinkingStyle),
			Italic: true,
			Dim:    true,
		}, "thinking")
		c.syncListOffset()
		return
	default:
		now := time.Now()
		c.lastMessage = now
		if source == "user" {
			c.lastUserAt = now
		}
		c.nextMessageID++
		messageID := c.nextMessageID
		c.lastMessageID = messageID
		if c.messages == nil {
			c.messages = make(map[int]messageEntry)
		}
		entry := messageEntry{
			ID:      messageID,
			Content: content,
			Source:  source,
			Time:    now,
			Model:   c.modelName,
		}
		if source == "assistant" {
			entry.UserAt = c.lastUserAt
		}
		c.messages[messageID] = entry
		lines := c.buildMessageLines(content, source, now, messageID)
		c.buffer.AppendMessage(lines)
		c.lastSource = source
		c.lastContent = content
		c.syncListOffset()
	}
}

// AppendText appends text to the last message (for streaming).
func (c *ChatView) AppendText(text string) {
	if c.lastSource == "" {
		c.buffer.AppendText(text)
		return
	}

	c.lastContent += text
	if entry, ok := c.messages[c.lastMessageID]; ok {
		entry.Content = c.lastContent
		c.messages[c.lastMessageID] = entry
	}
	messageTime := c.lastMessage
	if messageTime.IsZero() {
		messageTime = time.Now()
		c.lastMessage = messageTime
	}
	if c.mdRenderer == nil && c.metadataMode != "always" {
		c.buffer.AppendText(text)
		return
	}
	lines := c.buildMessageLines(c.lastContent, c.lastSource, messageTime, c.lastMessageID)
	c.buffer.ReplaceLastMessage(lines)
	c.syncListOffset()
}

func (c *ChatView) buildMessageLines(content, source string, messageTime time.Time, messageID int) []scrollback.Line {
	if messageTime.IsZero() {
		messageTime = time.Now()
	}
	var lines []scrollback.Line
	if c.mdRenderer != nil {
		lines = c.renderMarkdownLines(content, source)
	} else {
		lines = c.renderPlainLines(content, source)
	}

	if c.lastSource != "" && c.lastSource != source {
		lines = append([]scrollback.Line{{Content: "", Source: source}}, lines...)
	}

	if len(lines) == 0 {
		lines = []scrollback.Line{{Content: "", Source: source}}
	}

	if meta := c.metadataLine(content, source, messageTime); meta != nil {
		lines = append(lines, *meta)
	}

	if source == "user" {
		prefix := []scrollback.Span{{Text: "▶ ", Style: c.userStyle.Bold(true)}}
		for i := range lines {
			if i == 0 && lines[i].Content == "" {
				continue
			}
			lines[i].Prefix = prependPrefix(lines[i].Prefix, prefix)
		}
	}

	strip := c.roleStripPrefix(source)
	if len(strip) > 0 {
		for i := range lines {
			lines[i].Prefix = prependPrefix(lines[i].Prefix, strip)
		}
	}

	if messageID > 0 {
		for i := range lines {
			lines[i].MessageID = messageID
		}
	}
	return lines
}

func (c *ChatView) renderPlainLines(content, source string) []scrollback.Line {
	style := c.styleForSource(source)
	lineStyle := scrollback.LineStyle{
		FG:     extractFG(style),
		Bold:   isBold(style),
		Italic: isItalic(style),
		Dim:    isDim(style),
	}

	parts := strings.Split(content, "\n")
	lines := make([]scrollback.Line, 0, len(parts))
	for _, part := range parts {
		spans := []scrollback.Span{{Text: part, Style: style}}
		lines = append(lines, scrollback.Line{
			Content: part,
			Style:   lineStyle,
			Source:  source,
			Spans:   spans,
		})
	}
	return lines
}

func (c *ChatView) renderMarkdownLines(content, source string) []scrollback.Line {
	mdLines := c.mdRenderer.Render(source, content)
	lines := make([]scrollback.Line, 0, len(mdLines))
	for _, line := range mdLines {
		spans := convertMarkdownSpans(line.Spans)
		prefix := convertMarkdownSpans(line.Prefix)
		text := spansText(spans)
		if line.BlankLine {
			text = ""
		}
		lines = append(lines, scrollback.Line{
			Content:                text,
			Spans:                  spans,
			Prefix:                 prefix,
			Source:                 source,
			IsCode:                 line.IsCode,
			IsCodeHeader:           line.IsCodeHeader,
			Language:               line.Language,
			CodeLineNumberWidth:    line.CodeLineNumberWidth,
			CodeLineNumberOptional: line.CodeLineNumberOptional,
		})
	}
	return lines
}

func (c *ChatView) metadataLine(content, source string, messageTime time.Time) *scrollback.Line {
	if c.metadataMode != "always" {
		return nil
	}
	meta := c.metadataText(content, source, messageTime, c.modelName, c.lastUserAt)
	if meta == "" {
		return nil
	}
	return &scrollback.Line{
		Content: meta,
		Spans:   []scrollback.Span{{Text: meta, Style: c.metadataStyle}},
		Source:  source,
	}
}

func (c *ChatView) metadataText(content, source string, messageTime time.Time, modelName string, userAt time.Time) string {
	// Skip metadata for thinking and system messages (like welcome screen) - they're not sent to the model
	if source == "thinking" || source == "system" || source == "tool" {
		return ""
	}
	parts := make([]string, 0, 4)
	tokenEstimate := estimateTokens(content)
	if tokenEstimate > 0 {
		parts = append(parts, "tokens "+formatTokenEstimate(tokenEstimate))
	}
	if source == "assistant" && !userAt.IsZero() {
		delta := messageTime.Sub(userAt)
		if delta < 0 {
			delta = 0
		}
		if delta > 0 {
			parts = append(parts, formatDuration(delta))
		}
	}
	if !messageTime.IsZero() {
		parts = append(parts, messageTime.Format("15:04"))
	}
	if source == "assistant" {
		model := shortModelName(modelName)
		if model != "" {
			parts = append(parts, model)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func (c *ChatView) metadataTextForMessage(messageID int) string {
	if c.metadataMode != "hover" {
		return ""
	}
	if messageID <= 0 {
		return ""
	}
	entry, ok := c.messages[messageID]
	if !ok {
		return ""
	}
	return c.metadataText(entry.Content, entry.Source, entry.Time, entry.Model, entry.UserAt)
}

func estimateTokens(content string) int {
	runes := len([]rune(content))
	if runes == 0 {
		return 0
	}
	return max(1, runes/4)
}

func formatTokenEstimate(tokens int) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fm", float64(tokens)/1_000_000)
	case tokens >= 10_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%dk", tokens/1_000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%ds", int(d.Seconds()+0.5))
}

func shortModelName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	parts := strings.Split(name, "/")
	return parts[len(parts)-1]
}

func convertMarkdownSpans(spans []markdown.StyledSpan) []scrollback.Span {
	if len(spans) == 0 {
		return nil
	}
	out := make([]scrollback.Span, 0, len(spans))
	for _, span := range spans {
		if span.Text == "" {
			continue
		}
		out = append(out, scrollback.Span{
			Text:  span.Text,
			Style: uistyle.ToBackend(span.Style),
		})
	}
	return out
}

func spansText(spans []scrollback.Span) string {
	var b strings.Builder
	for _, span := range spans {
		b.WriteString(span.Text)
	}
	return b.String()
}

func prependPrefix(existing, prefix []scrollback.Span) []scrollback.Span {
	if len(prefix) == 0 {
		return existing
	}
	combined := make([]scrollback.Span, 0, len(prefix)+len(existing))
	combined = append(combined, prefix...)
	combined = append(combined, existing...)
	return combined
}

func (c *ChatView) roleStripPrefix(source string) []scrollback.Span {
	if source == "thinking" {
		return nil
	}
	style := c.styleForSource(source)
	if style == (backend.Style{}) {
		return nil
	}
	return []scrollback.Span{
		{Text: "▍", Style: style},
		{Text: " ", Style: c.bgStyle},
	}
}

// RemoveThinkingIndicator removes the thinking indicator if present.
func (c *ChatView) RemoveThinkingIndicator() {
	c.buffer.RemoveLastLineIfSource("thinking")
	c.syncListOffset()
}

// AppendReasoning appends streaming reasoning text (dimmed).
func (c *ChatView) AppendReasoning(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	c.reasoningBuilder.WriteString(text)
	c.setReasoningVisible(true)
	if c.reasoningText != nil {
		c.reasoningText.SetText(c.reasoningBuilder.String())
	}
	c.Invalidate()
}

// CollapseReasoning collapses reasoning block to preview.
func (c *ChatView) CollapseReasoning(preview, full string) {
	c.reasoningPreview = strings.TrimSpace(preview)
	c.reasoningBuilder.Reset()
	c.reasoningBuilder.WriteString(full)
	c.setReasoningVisible(true)
	if c.reasoningSection != nil {
		title := "Reasoning"
		if c.reasoningPreview != "" {
			title = "Reasoning: " + c.reasoningPreview
		}
		c.reasoningSection.SetTitle(title)
		c.reasoningSection.SetExpanded(false)
	}
	if c.reasoningText != nil {
		c.reasoningText.SetText(full)
	}
	c.Invalidate()
}

// ClearReasoningBlock clears reasoning state for new message.
func (c *ChatView) ClearReasoningBlock() {
	c.reasoningBuilder.Reset()
	c.reasoningPreview = ""
	c.setReasoningVisible(false)
	if c.reasoningText != nil {
		c.reasoningText.SetText("")
	}
	c.Invalidate()
}

// ToggleReasoning toggles reasoning block between collapsed and expanded.
// Returns true if a toggle occurred.
func (c *ChatView) ToggleReasoning() bool {
	if !c.reasoningVisible || c.reasoningSection == nil {
		return false
	}
	c.reasoningSection.SetExpanded(!c.reasoningSection.Expanded())
	return true
}

// IsReasoningLine returns true if the line at the given index is part of a reasoning block.
func (c *ChatView) IsReasoningLine(lineIdx int) bool {
	return false
}

// Clear clears all messages.
func (c *ChatView) Clear() {
	c.buffer.Clear()
	c.lastSource = ""
	c.lastContent = ""
	c.lastMessage = time.Time{}
	c.lastUserAt = time.Time{}
	c.nextMessageID = 0
	c.lastMessageID = 0
	c.messages = make(map[int]messageEntry)
	c.hoveredMessageID = 0
	c.hoveredCodeStart = -1
	c.hoveredCodeEnd = -1
	c.reasoningBuilder.Reset()
	c.reasoningPreview = ""
	c.setReasoningVisible(false)
	if c.reasoningText != nil {
		c.reasoningText.SetText("")
	}
	c.syncListOffset()
}

// GetContent returns all text content from the chat view (last N lines).
func (c *ChatView) GetContent(limit int) []string {
	return c.buffer.GetAllContent(limit)
}

// ScrollUp scrolls up by n lines.
func (c *ChatView) ScrollUp(n int) {
	c.buffer.ScrollUp(n)
	c.syncListOffset()
	c.notifyScroll()
}

// ScrollDown scrolls down by n lines.
func (c *ChatView) ScrollDown(n int) {
	c.buffer.ScrollDown(n)
	c.syncListOffset()
	c.notifyScroll()
}

// PageUp scrolls up by one page.
func (c *ChatView) PageUp() {
	c.buffer.PageUp()
	c.syncListOffset()
	c.notifyScroll()
}

// PageDown scrolls down by one page.
func (c *ChatView) PageDown() {
	c.buffer.PageDown()
	c.syncListOffset()
	c.notifyScroll()
}

// ScrollToTop scrolls to the beginning.
func (c *ChatView) ScrollToTop() {
	c.buffer.ScrollToTop()
	c.syncListOffset()
	c.notifyScroll()
}

// ScrollToBottom scrolls to the end.
func (c *ChatView) ScrollToBottom() {
	c.buffer.ScrollToBottom()
	c.syncListOffset()
	c.notifyScroll()
}

// ScrollPosition returns scroll position info.
func (c *ChatView) ScrollPosition() (top, total, viewHeight int) {
	return c.buffer.ScrollPosition()
}

// PositionForPoint maps screen coordinates to a buffer position.
func (c *ChatView) PositionForPoint(x, y int) (line, col int, ok bool) {
	bounds := c.listBounds
	if x < bounds.X || y < bounds.Y || y >= bounds.Y+bounds.Height {
		return 0, 0, false
	}
	if x >= bounds.X+bounds.Width {
		return 0, 0, false
	}
	row := y - bounds.Y
	column := x - bounds.X
	return c.buffer.PositionForView(row, column)
}

// CodeHeaderActionAtPoint returns a code header action if the point targets copy/open.
func (c *ChatView) CodeHeaderActionAtPoint(x, y int) (action, language, code string, ok bool) {
	lineIndex, col, ok := c.PositionForPoint(x, y)
	if !ok {
		return "", "", "", false
	}

	line, ok := c.buffer.LineAt(lineIndex)
	if !ok || !line.IsCodeHeader {
		return "", "", "", false
	}
	header := line.Content
	if header == "" {
		return "", "", "", false
	}

	copyIdx := strings.Index(header, "[copy]")
	if copyIdx >= 0 && col >= copyIdx && col < copyIdx+len("[copy]") {
		language, code, ok = c.buffer.CodeBlockAt(lineIndex)
		if !ok {
			return "", "", "", false
		}
		return "copy", language, code, true
	}

	openIdx := strings.Index(header, "[open]")
	if openIdx >= 0 && col >= openIdx && col < openIdx+len("[open]") {
		language, code, _ = c.buffer.CodeBlockAt(lineIndex)
		return "open", language, code, true
	}

	return "", "", "", false
}

// StartSelection begins text selection.
func (c *ChatView) StartSelection(line, col int) {
	c.buffer.StartSelection(line, col)
}

// UpdateSelection extends the selection.
func (c *ChatView) UpdateSelection(line, col int) {
	c.buffer.UpdateSelection(line, col)
}

// EndSelection finishes selection.
func (c *ChatView) EndSelection() {
	c.buffer.EndSelection()
}

// ClearSelection clears any active selection.
func (c *ChatView) ClearSelection() {
	c.buffer.ClearSelection()
}

// HasSelection returns true if a selection exists.
func (c *ChatView) HasSelection() bool {
	return c.buffer.HasSelection()
}

// SelectionText returns the selected text.
func (c *ChatView) SelectionText() string {
	return c.buffer.GetSelection()
}

// Search highlights matching text.
func (c *ChatView) Search(query string) {
	c.buffer.Search(query)
}

// NextMatch moves to the next search match.
func (c *ChatView) NextMatch() {
	c.buffer.NextMatch()
}

// PrevMatch moves to the previous search match.
func (c *ChatView) PrevMatch() {
	c.buffer.PrevMatch()
}

// SearchMatches returns current and total matches.
func (c *ChatView) SearchMatches() (current, total int) {
	return c.buffer.SearchMatches()
}

// ClearSearch clears search highlighting.
func (c *ChatView) ClearSearch() {
	c.buffer.Search("")
}

// LatestCodeBlock returns the most recent code block content.
func (c *ChatView) LatestCodeBlock() (language, code string, ok bool) {
	return c.buffer.LatestCodeBlock()
}

// Measure returns the preferred size.
func (c *ChatView) Measure(constraints runtime.Constraints) runtime.Size {
	// ChatView expands to fill available space
	return runtime.Size{
		Width:  constraints.MaxWidth,
		Height: constraints.MaxHeight,
	}
}

// Layout updates the scrollback buffer size.
func (c *ChatView) Layout(bounds runtime.Rect) {
	c.Base.Layout(bounds)
	content := bounds
	if content.Width > 0 {
		content.Width -= 1
	}
	if content.Width < 0 {
		content.Width = 0
	}
	if c.layout == nil {
		c.rebuildLayout()
	}
	if c.layout != nil {
		c.layout.Layout(content)
	}
	if c.list != nil {
		c.listBounds = c.list.Bounds()
	} else {
		c.listBounds = content
	}
	if c.buffer != nil {
		c.buffer.Resize(c.listBounds.Width, c.listBounds.Height)
	}
	c.syncListOffset()
}

// Render draws the chat view.
func (c *ChatView) Render(ctx runtime.RenderContext) {
	bounds := c.Bounds()
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

	// Hover metadata overlay
	lines := c.buffer.GetVisibleLines()
	if c.metadataMode == "hover" && c.hoveredMessageID > 0 && c.listBounds.Width > 0 {
		meta := c.metadataTextForMessage(c.hoveredMessageID)
		if meta != "" {
			metaLine := -1
			for i := len(lines) - 1; i >= 0; i-- {
				if lines[i].MessageID == c.hoveredMessageID {
					metaLine = i
					break
				}
			}
			if metaLine >= 0 && metaLine < c.listBounds.Height {
				availableWidth := c.listBounds.Width
				metaLen := len([]rune(meta))
				if metaLen <= availableWidth {
					content := strings.TrimRight(lines[metaLine].Content, " ")
					contentLen := len([]rune(content))
					startX := c.listBounds.X + availableWidth - metaLen
					if startX > c.listBounds.X+contentLen {
						ctx.Buffer.SetString(startX, c.listBounds.Y+metaLine, meta, c.metadataStyle)
					}
				}
			}
		}
	}

	// Draw scrollbar
	c.renderScrollbar(ctx)
}

func (c *ChatView) renderVisibleLine(ctx runtime.RenderContext, line scrollback.VisibleLine) {
	bounds := ctx.Bounds
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return
	}

	hoveredCodeStart := c.hoveredCodeStart
	hoveredCodeEnd := c.hoveredCodeEnd
	maxX := bounds.X + bounds.Width
	hideLineNumbers := line.IsCode &&
		line.CodeLineNumberOptional &&
		line.CodeLineNumberWidth > 0 &&
		(line.LineIndex < hoveredCodeStart || line.LineIndex > hoveredCodeEnd)

	if line.Selected {
		text := line.Content
		if hideLineNumbers {
			runes := []rune(text)
			for i := 0; i < line.CodeLineNumberWidth && i < len(runes); i++ {
				runes[i] = ' '
			}
			text = string(runes)
		}
		runes := []rune(text)
		if len(runes) > bounds.Width {
			text = string(runes[:bounds.Width])
		}
		ctx.Buffer.SetString(bounds.X, bounds.Y, text, c.selectionStyle)
		for x := bounds.X + len([]rune(text)); x < maxX; x++ {
			ctx.Buffer.Set(x, bounds.Y, ' ', c.selectionStyle)
		}
		return
	}

	if len(line.Spans) > 0 {
		highlightSet := make(map[int]bool, len(line.SearchHighlights))
		for _, idx := range line.SearchHighlights {
			highlightSet[idx] = true
		}

		x := bounds.X
		pos := 0
		for _, span := range line.Spans {
			for _, r := range span.Text {
				if x >= maxX {
					break
				}
				if hideLineNumbers && pos < line.CodeLineNumberWidth {
					ctx.Buffer.Set(x, bounds.Y, ' ', c.codeBlockBG)
					x++
					pos++
					continue
				}
				style := span.Style
				if highlightSet[pos] {
					style = c.searchStyle
				}
				ctx.Buffer.Set(x, bounds.Y, r, style)
				x++
				pos++
			}
			if x >= maxX {
				break
			}
		}

		if line.IsCode {
			fillStyle := c.codeBlockBG
			for ; x < maxX; x++ {
				ctx.Buffer.Set(x, bounds.Y, ' ', fillStyle)
			}
		}
		return
	}

	// Fallback for plain lines
	style := c.styleForSource(line.Source)
	if line.Style.Bold {
		style = style.Bold(true)
	}
	if line.Style.Italic {
		style = style.Italic(true)
	}
	if line.Style.Dim {
		style = style.Dim(true)
	}

	text := line.Content
	if hideLineNumbers {
		runes := []rune(text)
		for i := 0; i < line.CodeLineNumberWidth && i < len(runes); i++ {
			runes[i] = ' '
		}
		text = string(runes)
	}
	runes := []rune(text)
	if len(runes) > bounds.Width {
		text = string(runes[:bounds.Width])
	}
	ctx.Buffer.SetString(bounds.X, bounds.Y, text, style)
	if line.IsCode {
		fillStyle := c.codeBlockBG
		for x := bounds.X + len([]rune(text)); x < maxX; x++ {
			ctx.Buffer.Set(x, bounds.Y, ' ', fillStyle)
		}
	}
}

// renderScrollbar draws the scrollbar on the right edge.
func (c *ChatView) renderScrollbar(ctx runtime.RenderContext) {
	bounds := c.listBounds
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return
	}
	top, total, viewH := c.buffer.ScrollPosition()

	if total <= viewH {
		return // No scrollbar needed
	}

	// Calculate thumb position and size
	thumbSize := max(1, (viewH*viewH)/total)
	thumbPos := (top * (viewH - thumbSize)) / (total - viewH)

	scrollX := bounds.X + bounds.Width

	for y := 0; y < bounds.Height; y++ {
		var r rune
		var style backend.Style
		if y >= thumbPos && y < thumbPos+thumbSize {
			r = '█'
			style = c.scrollThumb
		} else {
			r = '░'
			style = c.scrollbarStyle
		}
		ctx.Buffer.Set(scrollX, bounds.Y+y, r, style)
	}
}

// HandleMessage processes input events.
func (c *ChatView) HandleMessage(msg runtime.Message) runtime.HandleResult {
	switch m := msg.(type) {
	case runtime.KeyMsg:
		if c.layout != nil {
			if result := c.layout.HandleMessage(msg); result.Handled {
				return result
			}
		}
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
	case runtime.MouseMsg:
		if c.layout != nil {
			if result := c.layout.HandleMessage(msg); result.Handled {
				return result
			}
		}
		return c.handleMouse(m)
	}

	return runtime.Unhandled()
}

func (c *ChatView) handleMouse(m runtime.MouseMsg) runtime.HandleResult {
	// Handle hover (mouse move without button)
	if m.Button != runtime.MouseNone || m.Action != runtime.MouseRelease {
		return runtime.Unhandled()
	}
	lineIndex, _, ok := c.PositionForPoint(m.X, m.Y)
	if !ok {
		return c.clearHover()
	}
	line, ok := c.buffer.LineAt(lineIndex)
	if !ok {
		return c.clearHover()
	}
	messageID := line.MessageID
	codeStart := -1
	codeEnd := -1
	if line.IsCode {
		if start, end, ok := c.buffer.CodeBlockRange(lineIndex); ok {
			codeStart = start
			codeEnd = end
		}
	}
	if messageID == c.hoveredMessageID && codeStart == c.hoveredCodeStart && codeEnd == c.hoveredCodeEnd {
		return runtime.Unhandled()
	}
	c.hoveredMessageID = messageID
	c.hoveredCodeStart = codeStart
	c.hoveredCodeEnd = codeEnd
	return runtime.Handled()
}

func (c *ChatView) clearHover() runtime.HandleResult {
	if c.hoveredMessageID == 0 && c.hoveredCodeStart == -1 && c.hoveredCodeEnd == -1 {
		return runtime.Unhandled()
	}
	c.hoveredMessageID = 0
	c.hoveredCodeStart = -1
	c.hoveredCodeEnd = -1
	return runtime.Handled()
}

func (c *ChatView) styleForSource(source string) backend.Style {
	switch source {
	case "user":
		return c.userStyle
	case "assistant":
		return c.assistantStyle
	case "system":
		return c.systemStyle
	case "tool":
		return c.toolStyle
	case "thinking":
		return c.thinkingStyle
	default:
		return c.assistantStyle
	}
}

func (c *ChatView) notifyScroll() {
	if c.onScrollChange != nil {
		top, total, viewH := c.buffer.ScrollPosition()
		c.onScrollChange(top, total, viewH)
	}
}

func (c *ChatView) rebuildLayout() {
	children := make([]runtime.FlexChild, 0, 2)
	if c.reasoningVisible && c.reasoningPanel != nil {
		children = append(children, runtime.Fixed(c.reasoningPanel))
	}
	if c.list != nil {
		children = append(children, runtime.Expanded(c.list))
	}
	c.layout = runtime.VBox(children...)
}

func (c *ChatView) setReasoningVisible(visible bool) {
	if c.reasoningVisible == visible {
		return
	}
	c.reasoningVisible = visible
	c.rebuildLayout()
	c.services.Relayout()
	c.Invalidate()
}

func (c *ChatView) syncListOffset() {
	if c.list == nil || c.buffer == nil {
		return
	}
	top, _, _ := c.buffer.ScrollPosition()
	if top < 0 {
		top = 0
	}
	c.list.ScrollToOffset(top)
}

// ReasoningContains reports whether a screen point falls within the reasoning panel.
func (c *ChatView) ReasoningContains(x, y int) bool {
	if !c.reasoningVisible || c.reasoningPanel == nil {
		return false
	}
	return c.reasoningPanel.Bounds().Contains(x, y)
}

// Style helper functions - extract info from backend.Style
func extractFG(s backend.Style) uint32 {
	fg, _, _ := s.Decompose()
	if fg.IsRGB() {
		r, g, b := fg.RGB()
		return (uint32(r) << 16) | (uint32(g) << 8) | uint32(b)
	}
	return uint32(fg)
}

func isBold(s backend.Style) bool {
	_, _, attrs := s.Decompose()
	return attrs&backend.AttrBold != 0
}

func isItalic(s backend.Style) bool {
	_, _, attrs := s.Decompose()
	return attrs&backend.AttrItalic != 0
}

func isDim(s backend.Style) bool {
	_, _, attrs := s.Decompose()
	return attrs&backend.AttrDim != 0
}

func (c *ChatView) ClipboardCopy() (string, bool) {
	if c.HasSelection() {
		text := c.SelectionText()
		if text == "" {
			return "", false
		}
		return text, true
	}
	_, code, ok := c.LatestCodeBlock()
	if ok && code != "" {
		return code, true
	}
	return "", false
}

func (c *ChatView) ClipboardCut() (string, bool) {
	return "", false
}

func (c *ChatView) ClipboardPaste(text string) bool {
	return false
}

func (c *ChatView) ScrollBy(dx, dy int) {
	if dy < 0 {
		c.ScrollUp(-dy)
		return
	}
	if dy > 0 {
		c.ScrollDown(dy)
	}
}

func (c *ChatView) ScrollTo(x, y int) {
	top, _, _ := c.ScrollPosition()
	if y < top {
		c.ScrollUp(top - y)
		return
	}
	if y > top {
		c.ScrollDown(y - top)
	}
}

func (c *ChatView) PageBy(pages int) {
	if pages < 0 {
		for i := 0; i < -pages; i++ {
			c.PageUp()
		}
		return
	}
	for i := 0; i < pages; i++ {
		c.PageDown()
	}
}

func (c *ChatView) ScrollToStart() {
	c.ScrollToTop()
}

func (c *ChatView) ScrollToEnd() {
	c.ScrollToBottom()
}

var _ clipboard.Target = (*ChatView)(nil)
var _ scroll.Controller = (*ChatView)(nil)
var _ accessibility.Accessible = (*ChatView)(nil)
var _ runtime.ChildProvider = (*ChatView)(nil)
var _ runtime.Bindable = (*ChatView)(nil)
var _ runtime.Unbindable = (*ChatView)(nil)

// Bind stores app services for layout invalidation.
func (c *ChatView) Bind(services runtime.Services) {
	c.services = services
}

// Unbind clears app services.
func (c *ChatView) Unbind() {
	c.services = runtime.Services{}
}

// ChildWidgets returns child widgets for proper widget tree traversal.
func (c *ChatView) ChildWidgets() []runtime.Widget {
	if c.layout == nil {
		return nil
	}
	return c.layout.ChildWidgets()
}

// AccessibleRole returns the accessibility role for the chat view.
func (c *ChatView) AccessibleRole() accessibility.Role {
	return accessibility.RoleList
}

// AccessibleLabel returns a label describing the chat view's current state.
func (c *ChatView) AccessibleLabel() string {
	if c == nil || c.buffer == nil {
		return "Chat View"
	}
	count := c.buffer.LineCount()
	if count == 0 {
		return "Chat View (empty)"
	}
	return fmt.Sprintf("Chat View (%d messages)", count)
}

// AccessibleDescription returns additional context about the chat view.
func (c *ChatView) AccessibleDescription() string {
	if c == nil || c.buffer == nil {
		return ""
	}
	// Include recent message preview for debugging
	lines := c.buffer.GetVisibleLines()
	if len(lines) == 0 {
		return ""
	}
	// Get last non-empty line content
	for i := len(lines) - 1; i >= 0; i-- {
		content := strings.TrimSpace(lines[i].Content)
		if content != "" {
			if len(content) > 80 {
				content = content[:80] + "..."
			}
			return fmt.Sprintf("Last: %s", content)
		}
	}
	return ""
}

// AccessibleState returns the current state of the chat view.
func (c *ChatView) AccessibleState() accessibility.StateSet {
	return accessibility.StateSet{
		ReadOnly: true,
	}
}

// AccessibleValue returns scroll position information.
func (c *ChatView) AccessibleValue() *accessibility.ValueInfo {
	if c == nil || c.buffer == nil {
		return nil
	}
	top, total, height := c.buffer.ScrollPosition()
	if total == 0 {
		return nil
	}
	pct := float64(top) / float64(total) * 100
	return &accessibility.ValueInfo{
		Min:     0,
		Max:     float64(total),
		Current: float64(top),
		Text:    fmt.Sprintf("Line %d of %d (%.0f%%), viewport %d lines", top+1, total, pct, height),
	}
}
