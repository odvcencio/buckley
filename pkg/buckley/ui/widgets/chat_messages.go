package widgets

import (
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/buckley/ui/scrollback"
	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/clipboard"
	"github.com/odvcencio/fluffyui/markdown"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/scroll"
	"github.com/odvcencio/fluffyui/state"
	uistyle "github.com/odvcencio/fluffyui/style"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// ChatMessage represents a single chat entry.
type ChatMessage struct {
	ID      int
	Content string
	Source  string
	Time    time.Time
	Model   string
	UserAt  time.Time
}

// SearchMatchState summarizes search match position.
type SearchMatchState struct {
	Current int
	Total   int
}

// ChatMessagesConfig provides external state for message rendering.
type ChatMessagesConfig struct {
	Messages       state.Readable[[]ChatMessage]
	Thinking       state.Readable[bool]
	ModelName      state.Readable[string]
	SearchQuery    state.Readable[string]
	SearchMatches  state.Writable[SearchMatchState]
	SelectionText  state.Writable[string]
	SelectionActive state.Writable[bool]
	// MetadataMode controls metadata visibility ("always", "hover", "never").
	MetadataMode state.Readable[string]
}

// ChatMessages renders the scrollback list of chat messages.
type ChatMessages struct {
	uiwidgets.Base

	buffer     *scrollback.Buffer
	transcript *chatTranscript
	virtual    *chatMessagesVirtual
	scrollView *uiwidgets.ScrollView
	listBounds runtime.Rect
	stack      *uiwidgets.Stack
	overlay    *chatMessagesOverlay

	services runtime.Services
	subs     state.Subscriptions

	messagesSig       state.Readable[[]ChatMessage]
	thinkingSig       state.Readable[bool]
	ownedMessagesSig  *state.Signal[[]ChatMessage]
	ownedThinkingSig  *state.Signal[bool]
	modelNameSig      state.Readable[string]
	metadataModeSig   state.Readable[string]
	ownedModelNameSig *state.Signal[string]
	ownedMetadataSig  *state.Signal[string]
	searchQuerySig    state.Readable[string]
	searchMatchesSig  state.Writable[SearchMatchState]
	selectionTextSig  state.Writable[string]
	selectionActiveSig state.Writable[bool]
	ownedSearchQuerySig   *state.Signal[string]
	ownedSearchMatchesSig *state.Signal[SearchMatchState]
	ownedSelectionTextSig *state.Signal[string]
	ownedSelectionActiveSig *state.Signal[bool]

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
	mdRenderer       *markdown.Renderer
	codeBlockBG      backend.Style
	metadataStyle    backend.Style
	metadataMode     string
	modelName        string
	searchQuery      string
	hoveredMessageID int
	hoveredCodeStart int
	hoveredCodeEnd   int
	selecting        bool

	// Callbacks
	onScrollChange func(top, total, viewHeight int)
	onCodeAction   func(action, language, code string)
}

// NewChatMessages creates a new chat message list.
func NewChatMessages() *ChatMessages {
	return NewChatMessagesWithConfig(ChatMessagesConfig{})
}

// NewChatMessagesWithConfig creates message list with external state bindings.
func NewChatMessagesWithConfig(cfg ChatMessagesConfig) *ChatMessages {
	m := &ChatMessages{
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
		hoveredCodeStart: -1,
		hoveredCodeEnd:   -1,
	}
	m.transcript = newChatTranscript(m.buffer)
	m.transcript.onChange = m.syncListOffset
	m.ownedMessagesSig = state.NewSignal([]ChatMessage{})
	m.ownedThinkingSig = state.NewSignal(false)
	m.ownedModelNameSig = state.NewSignal("")
	m.ownedMetadataSig = state.NewSignal("always")
	m.ownedSearchQuerySig = state.NewSignal("")
	m.ownedSearchMatchesSig = state.NewSignal(SearchMatchState{})
	m.ownedSelectionTextSig = state.NewSignal("")
	m.ownedSelectionActiveSig = state.NewSignal(false)
	if cfg.Messages != nil {
		m.messagesSig = cfg.Messages
	} else {
		m.messagesSig = m.ownedMessagesSig
	}
	if cfg.Thinking != nil {
		m.thinkingSig = cfg.Thinking
	} else {
		m.thinkingSig = m.ownedThinkingSig
	}
	if cfg.ModelName != nil {
		m.modelNameSig = cfg.ModelName
	} else {
		m.modelNameSig = m.ownedModelNameSig
	}
	if cfg.MetadataMode != nil {
		m.metadataModeSig = cfg.MetadataMode
	} else {
		m.metadataModeSig = m.ownedMetadataSig
	}
	if cfg.SearchQuery != nil {
		m.searchQuerySig = cfg.SearchQuery
	} else {
		m.searchQuerySig = m.ownedSearchQuerySig
	}
	if cfg.SearchMatches != nil {
		m.searchMatchesSig = cfg.SearchMatches
	} else {
		m.searchMatchesSig = m.ownedSearchMatchesSig
	}
	if cfg.SelectionText != nil {
		m.selectionTextSig = cfg.SelectionText
	} else {
		m.selectionTextSig = m.ownedSelectionTextSig
	}
	if cfg.SelectionActive != nil {
		m.selectionActiveSig = cfg.SelectionActive
	} else {
		m.selectionActiveSig = m.ownedSelectionActiveSig
	}
	m.virtual = newChatMessagesVirtual(m)
	m.scrollView = uiwidgets.NewScrollView(m.virtual)
	m.scrollView.SetLabel("Chat messages")
	m.scrollView.SetBehavior(scroll.ScrollBehavior{Vertical: scroll.ScrollAuto, Horizontal: scroll.ScrollNever, MouseWheel: 3, PageSize: 1})
	m.overlay = &chatMessagesOverlay{owner: m}
	m.stack = uiwidgets.NewStack(m.scrollView, m.overlay)
	m.subscribe()
	return m
}

// SetStyles configures the message styles.
func (m *ChatMessages) SetStyles(user, assistant, system, tool, thinking backend.Style) {
	m.userStyle = user
	m.assistantStyle = assistant
	m.systemStyle = system
	m.toolStyle = tool
	m.thinkingStyle = thinking
}

// SetUIStyles configures scrollbar, selection, and background styles.
func (m *ChatMessages) SetUIStyles(scrollbar, thumb, selection, search, background backend.Style) {
	m.scrollbarStyle = scrollbar
	m.scrollThumb = thumb
	m.selectionStyle = selection
	m.searchStyle = search
	m.bgStyle = background
	if m.scrollView != nil {
		m.scrollView.SetStyle(background)
	}
}

// SetMarkdownRenderer configures markdown rendering for the chat list.
func (m *ChatMessages) SetMarkdownRenderer(renderer *markdown.Renderer, codeBlockBG backend.Style) {
	m.mdRenderer = renderer
	m.codeBlockBG = codeBlockBG
}

// SetMetadataStyle configures the metadata line style.
func (m *ChatMessages) SetMetadataStyle(style backend.Style) {
	m.metadataStyle = style
}

// SetMessageMetadataMode configures metadata visibility ("always", "hover", "never").
func (m *ChatMessages) SetMessageMetadataMode(mode string) {
	if !m.ownsMetadataMode() {
		return
	}
	if m.ownedMetadataSig != nil {
		m.ownedMetadataSig.Set(normalizeMetadataMode(mode))
	}
}

func normalizeMetadataMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "always", "hover", "never":
		return mode
	default:
		return "always"
	}
}

// SetModelName updates the model name for metadata display.
func (m *ChatMessages) SetModelName(name string) {
	if !m.ownsModelName() {
		return
	}
	if m.ownedModelNameSig != nil {
		m.ownedModelNameSig.Set(strings.TrimSpace(name))
	}
}

// OnScrollChange sets a callback for scroll position changes.
func (m *ChatMessages) OnScrollChange(fn func(top, total, viewHeight int)) {
	m.onScrollChange = fn
}

// OnCodeAction registers a handler for code header actions ("copy", "open").
func (m *ChatMessages) OnCodeAction(fn func(action, language, code string)) {
	m.onCodeAction = fn
}

func (m *ChatMessages) ownsMessages() bool {
	if m == nil || m.messagesSig == nil || m.ownedMessagesSig == nil {
		return false
	}
	sig, ok := m.messagesSig.(*state.Signal[[]ChatMessage])
	return ok && sig == m.ownedMessagesSig
}

func (m *ChatMessages) ownsThinking() bool {
	if m == nil || m.thinkingSig == nil || m.ownedThinkingSig == nil {
		return false
	}
	sig, ok := m.thinkingSig.(*state.Signal[bool])
	return ok && sig == m.ownedThinkingSig
}

func (m *ChatMessages) ownsModelName() bool {
	if m == nil || m.modelNameSig == nil || m.ownedModelNameSig == nil {
		return false
	}
	sig, ok := m.modelNameSig.(*state.Signal[string])
	return ok && sig == m.ownedModelNameSig
}

func (m *ChatMessages) ownsMetadataMode() bool {
	if m == nil || m.metadataModeSig == nil || m.ownedMetadataSig == nil {
		return false
	}
	sig, ok := m.metadataModeSig.(*state.Signal[string])
	return ok && sig == m.ownedMetadataSig
}

// Bind stores app services for layout invalidation.
func (m *ChatMessages) Bind(services runtime.Services) {
	if m == nil {
		return
	}
	m.services = services
	m.subs.SetScheduler(services.Scheduler())
	m.subscribe()
}

// Unbind clears app services.
func (m *ChatMessages) Unbind() {
	if m == nil {
		return
	}
	m.subs.Clear()
	m.services = runtime.Services{}
}

func (m *ChatMessages) subscribe() {
	m.subs.Clear()
	if m.messagesSig != nil {
		m.subs.Observe(m.messagesSig, m.onMessagesChanged)
	}
	if m.thinkingSig != nil {
		m.subs.Observe(m.thinkingSig, m.onThinkingChanged)
	}
	if m.modelNameSig != nil {
		m.subs.Observe(m.modelNameSig, m.onModelNameChanged)
	}
	if m.metadataModeSig != nil {
		m.subs.Observe(m.metadataModeSig, m.onMetadataModeChanged)
	}
	if m.searchQuerySig != nil {
		m.subs.Observe(m.searchQuerySig, m.onSearchQueryChanged)
	}
	m.syncFromMessages()
	m.syncThinking()
	m.onModelNameChanged()
	m.onMetadataModeChanged()
	m.onSearchQueryChanged()
}

func (m *ChatMessages) onMessagesChanged() {
	m.syncFromMessages()
	m.requestInvalidate()
}

func (m *ChatMessages) onThinkingChanged() {
	m.syncThinking()
	m.requestInvalidate()
}

func (m *ChatMessages) onModelNameChanged() {
	if m.modelNameSig == nil {
		return
	}
	m.modelName = strings.TrimSpace(m.modelNameSig.Get())
	m.requestInvalidate()
}

func (m *ChatMessages) onMetadataModeChanged() {
	if m.metadataModeSig == nil {
		return
	}
	m.metadataMode = normalizeMetadataMode(m.metadataModeSig.Get())
	m.requestInvalidate()
}

func (m *ChatMessages) onSearchQueryChanged() {
	if m.searchQuerySig == nil {
		return
	}
	m.applySearchQuery(m.searchQuerySig.Get(), false)
}

func (m *ChatMessages) requestInvalidate() {
	if m == nil {
		return
	}
	if m.services != (runtime.Services{}) {
		m.services.Invalidate()
		return
	}
	m.Invalidate()
}

func (m *ChatMessages) syncFromMessages() {
	if m.messagesSig == nil || m.transcript == nil {
		return
	}
	m.transcript.syncFromMessages(m.messagesSig.Get(), m, thinkingLineStyle(m.thinkingStyle))
	m.ClearSelection()
	if m.searchQuerySig != nil {
		m.applySearchQuery(m.searchQuerySig.Get(), true)
	} else if m.searchQuery != "" {
		m.applySearchQuery(m.searchQuery, true)
	} else {
		m.updateSearchMatches()
	}
}

func (m *ChatMessages) syncThinking() {
	if m.thinkingSig == nil || m.transcript == nil {
		return
	}
	m.transcript.syncThinking(m.thinkingSig.Get(), thinkingLineStyle(m.thinkingStyle))
}

// AddMessage adds a message to the chat.
func (m *ChatMessages) AddMessage(content, source string) {
	if m.ownsMessages() {
		if source == "thinking" {
			if m.ownedThinkingSig != nil {
				m.ownedThinkingSig.Set(true)
			}
			return
		}
		now := time.Now()
		messageID := 0
		if m.transcript != nil {
			messageID = m.transcript.nextID()
		}
		entry := ChatMessage{
			ID:      messageID,
			Content: content,
			Source:  source,
			Time:    now,
			Model:   m.modelName,
		}
		if source == "user" {
			if m.transcript != nil {
				m.transcript.lastUserAt = now
			}
		}
		if source == "assistant" {
			if m.transcript != nil {
				entry.UserAt = m.transcript.lastUserAt
			}
		}
		current := m.ownedMessagesSig.Get()
		cloned := append([]ChatMessage(nil), current...)
		cloned = append(cloned, entry)
		m.ownedMessagesSig.Set(cloned)
		return
	}

	if m.transcript == nil {
		return
	}
	m.transcript.appendMessageInternal(ChatMessage{
		Content: content,
		Source:  source,
		Time:    time.Now(),
		Model:   m.modelName,
	}, m, thinkingLineStyle(m.thinkingStyle))
}

// AppendText appends text to the last message (for streaming).
func (m *ChatMessages) AppendText(text string) {
	if m.ownsMessages() {
		current := m.ownedMessagesSig.Get()
		if len(current) == 0 {
			return
		}
		cloned := append([]ChatMessage(nil), current...)
		last := cloned[len(cloned)-1]
		last.Content += text
		cloned[len(cloned)-1] = last
		m.ownedMessagesSig.Set(cloned)
		return
	}
	if m.transcript == nil {
		return
	}
	simpleAppend := m.mdRenderer == nil && m.metadataMode != "always"
	m.transcript.appendText(text, m, simpleAppend)
	m.requestInvalidate()
}

func (m *ChatMessages) buildMessageLines(content, source string, messageTime time.Time, messageID int) []scrollback.Line {
	if messageTime.IsZero() {
		messageTime = time.Now()
	}
	var lines []scrollback.Line
	if m.mdRenderer != nil {
		lines = m.renderMarkdownLines(content, source)
	} else {
		lines = m.renderPlainLines(content, source)
	}

	lastSource := ""
	if m.transcript != nil {
		lastSource = m.transcript.lastSourceValue()
	}
	if lastSource != "" && lastSource != source {
		lines = append([]scrollback.Line{{Content: "", Source: source}}, lines...)
	}

	if len(lines) == 0 {
		lines = []scrollback.Line{{Content: "", Source: source}}
	}

	if meta := m.metadataLine(content, source, messageTime); meta != nil {
		lines = append(lines, *meta)
	}

	if source == "user" {
		prefix := []scrollback.Span{{Text: "▶ ", Style: m.userStyle.Bold(true)}}
		for i := range lines {
			if i == 0 && lines[i].Content == "" {
				continue
			}
			lines[i].Prefix = prependPrefix(lines[i].Prefix, prefix)
		}
	}

	strip := m.roleStripPrefix(source)
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

func (m *ChatMessages) renderPlainLines(content, source string) []scrollback.Line {
	style := m.styleForSource(source)
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

func (m *ChatMessages) renderMarkdownLines(content, source string) []scrollback.Line {
	mdLines := m.mdRenderer.Render(source, content)
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

func (m *ChatMessages) metadataLine(content, source string, messageTime time.Time) *scrollback.Line {
	if m.metadataMode != "always" {
		return nil
	}
	userAt := time.Time{}
	if m.transcript != nil {
		userAt = m.transcript.lastUserTime()
	}
	meta := m.metadataText(content, source, messageTime, m.modelName, userAt)
	if meta == "" {
		return nil
	}
	return &scrollback.Line{
		Content: meta,
		Spans:   []scrollback.Span{{Text: meta, Style: m.metadataStyle}},
		Source:  source,
	}
}

func (m *ChatMessages) metadataText(content, source string, messageTime time.Time, modelName string, userAt time.Time) string {
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

func (m *ChatMessages) metadataTextForMessage(messageID int) string {
	if m.metadataMode != "hover" {
		return ""
	}
	if messageID <= 0 {
		return ""
	}
	if m.transcript == nil {
		return ""
	}
	entry, ok := m.transcript.messageByID(messageID)
	if !ok {
		return ""
	}
	return m.metadataText(entry.Content, entry.Source, entry.Time, entry.Model, entry.UserAt)
}

// RemoveThinkingIndicator removes the thinking indicator if present.
func (m *ChatMessages) RemoveThinkingIndicator() {
	if m.ownsThinking() {
		if m.ownedThinkingSig != nil {
			m.ownedThinkingSig.Set(false)
		}
		return
	}
	if m.transcript == nil {
		return
	}
	m.transcript.removeThinkingIndicator()
}

// Clear clears all messages.
func (m *ChatMessages) Clear() {
	if m.ownsMessages() {
		if m.ownedMessagesSig != nil {
			m.ownedMessagesSig.Set(nil)
		}
		if m.ownedThinkingSig != nil {
			m.ownedThinkingSig.Set(false)
		}
		return
	}
	if m.transcript != nil {
		m.transcript.clearInternal()
	}
	m.hoveredMessageID = 0
	m.hoveredCodeStart = -1
	m.hoveredCodeEnd = -1
	m.selecting = false
	m.ClearSelection()
	m.setSearchQuery("")
	if m.scrollView != nil {
		m.scrollView.ScrollTo(0, 0)
	}
}

// LatestCodeBlock returns the most recent code block content.
func (m *ChatMessages) LatestCodeBlock() (language, code string, ok bool) {
	return m.buffer.LatestCodeBlock()
}

// Measure returns the preferred size.
// ChatMessages is a scrollable widget, so it returns minimal size to allow
// flex containers to properly calculate available space for Expanded children.
// When flex containers measure children with unbounded constraints (maxInt),
// returning maxInt would cause the flex shrink algorithm to shrink this widget to 0.
func (m *ChatMessages) Measure(constraints runtime.Constraints) runtime.Size {
	return constraints.Constrain(runtime.Size{Width: 1, Height: 1})
}

// Layout updates the scrollback buffer size.
func (m *ChatMessages) Layout(bounds runtime.Rect) {
	m.Base.Layout(bounds)
	content := bounds
	if m.stack != nil {
		m.stack.Layout(content)
	} else if m.scrollView != nil {
		m.scrollView.Layout(content)
	}
	if m.scrollView != nil {
		m.listBounds = m.scrollView.ContentBounds()
	} else {
		m.listBounds = content
	}
	if m.buffer != nil {
		m.buffer.Resize(m.listBounds.Width, m.listBounds.Height)
	}
	m.syncListOffset()
}

// Render draws the chat list.
func (m *ChatMessages) Render(ctx runtime.RenderContext) {
	bounds := m.Bounds()
	if bounds.Width == 0 || bounds.Height == 0 {
		return
	}
	ctx.Clear(m.bgStyle)
	if m.stack != nil {
		m.stack.Render(ctx)
		return
	}
	if m.scrollView != nil {
		m.scrollView.Render(ctx)
	}
}

func (m *ChatMessages) renderMetadataOverlay(ctx runtime.RenderContext) {
	lines := m.buffer.GetVisibleLines()
	if m.metadataMode == "hover" && m.hoveredMessageID > 0 && m.listBounds.Width > 0 {
		meta := m.metadataTextForMessage(m.hoveredMessageID)
		if meta != "" {
			metaLine := -1
			for i := len(lines) - 1; i >= 0; i-- {
				if lines[i].MessageID == m.hoveredMessageID {
					metaLine = i
					break
				}
			}
			if metaLine >= 0 && metaLine < m.listBounds.Height {
				availableWidth := m.listBounds.Width
				metaLen := textWidth(meta)
				if metaLen <= availableWidth {
					content := strings.TrimRight(lines[metaLine].Content, " ")
					contentLen := textWidth(content)
					startX := m.listBounds.X + availableWidth - metaLen
					if startX > m.listBounds.X+contentLen {
						ctx.Buffer.SetString(startX, m.listBounds.Y+metaLine, meta, m.metadataStyle)
					}
				}
			}
		}
	}
}

func (m *ChatMessages) renderVisibleLine(ctx runtime.RenderContext, line scrollback.VisibleLine) {
	bounds := ctx.Bounds
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return
	}

	hoveredCodeStart := m.hoveredCodeStart
	hoveredCodeEnd := m.hoveredCodeEnd
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
		ctx.Buffer.SetString(bounds.X, bounds.Y, text, m.selectionStyle)
		for x := bounds.X + textWidth(text); x < maxX; x++ {
			ctx.Buffer.Set(x, bounds.Y, ' ', m.selectionStyle)
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
					ctx.Buffer.Set(x, bounds.Y, ' ', m.codeBlockBG)
					x++
					pos++
					continue
				}
				style := span.Style
				if highlightSet[pos] {
					style = m.searchStyle
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
			fillStyle := m.codeBlockBG
			for ; x < maxX; x++ {
				ctx.Buffer.Set(x, bounds.Y, ' ', fillStyle)
			}
		}
		return
	}

	style := m.styleForSource(line.Source)
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
		fillStyle := m.codeBlockBG
		for x := bounds.X + textWidth(text); x < maxX; x++ {
			ctx.Buffer.Set(x, bounds.Y, ' ', fillStyle)
		}
	}
}

// HandleMessage processes input events.
func (m *ChatMessages) HandleMessage(msg runtime.Message) runtime.HandleResult {
	switch evt := msg.(type) {
	case runtime.MouseMsg:
		return m.handleMouse(evt)
	default:
		return runtime.Unhandled()
	}
}

func (m *ChatMessages) handleMouse(evt runtime.MouseMsg) runtime.HandleResult {
	switch evt.Button {
	case runtime.MouseWheelUp:
		m.ScrollUp(3)
		return runtime.Handled()
	case runtime.MouseWheelDown:
		m.ScrollDown(3)
		return runtime.Handled()
	}

	switch evt.Action {
	case runtime.MousePress:
		if evt.Button == runtime.MouseLeft {
			if action, language, code, ok := m.CodeHeaderActionAtPoint(evt.X, evt.Y); ok {
				m.handleCodeAction(action, language, code)
				return runtime.Handled()
			}
			if m.startSelectionAt(evt.X, evt.Y) {
				return runtime.Handled()
			}
		}
	case runtime.MouseMove:
		if m.selecting {
			if m.updateSelectionAt(evt.X, evt.Y) {
				return runtime.Handled()
			}
		}
	case runtime.MouseRelease:
		if evt.Button == runtime.MouseLeft && m.selecting {
			m.endSelection()
			return runtime.Handled()
		}
	}

	if evt.Button != runtime.MouseNone || (evt.Action != runtime.MouseRelease && evt.Action != runtime.MouseMove) {
		return runtime.Unhandled()
	}
	lineIndex, _, ok := m.PositionForPoint(evt.X, evt.Y)
	if !ok {
		return m.clearHover()
	}
	line, ok := m.buffer.LineAt(lineIndex)
	if !ok {
		return m.clearHover()
	}
	messageID := line.MessageID
	codeStart := -1
	codeEnd := -1
	if line.IsCode {
		if start, end, ok := m.buffer.CodeBlockRange(lineIndex); ok {
			codeStart = start
			codeEnd = end
		}
	}
	if messageID == m.hoveredMessageID && codeStart == m.hoveredCodeStart && codeEnd == m.hoveredCodeEnd {
		return runtime.Unhandled()
	}
	m.hoveredMessageID = messageID
	m.hoveredCodeStart = codeStart
	m.hoveredCodeEnd = codeEnd
	m.requestInvalidate()
	return runtime.Handled()
}

func (m *ChatMessages) handleCodeAction(action, language, code string) {
	if strings.TrimSpace(code) == "" {
		return
	}
	if m.onCodeAction != nil {
		m.onCodeAction(action, language, code)
		return
	}
	if action == "copy" {
		m.copyToClipboard(code)
	}
}

func (m *ChatMessages) copyToClipboard(text string) bool {
	if m.services == (runtime.Services{}) {
		return false
	}
	cb := m.services.Clipboard()
	if cb == nil || !cb.Available() {
		return false
	}
	_ = cb.Write(text)
	return true
}

func (m *ChatMessages) styleForSource(source string) backend.Style {
	switch source {
	case "user":
		return m.userStyle
	case "assistant":
		return m.assistantStyle
	case "system":
		return m.systemStyle
	case "tool":
		return m.toolStyle
	case "thinking":
		return m.thinkingStyle
	default:
		return m.assistantStyle
	}
}

func (m *ChatMessages) roleStripPrefix(source string) []scrollback.Span {
	if source == "thinking" {
		return nil
	}
	style := m.styleForSource(source)
	if style == (backend.Style{}) {
		return nil
	}
	return []scrollback.Span{
		{Text: "▍", Style: style},
		{Text: " ", Style: m.bgStyle},
	}
}

func (m *ChatMessages) ClipboardCopy() (string, bool) {
	if m.HasSelection() {
		text := m.SelectionText()
		if text == "" {
			return "", false
		}
		return text, true
	}
	_, code, ok := m.LatestCodeBlock()
	if ok && code != "" {
		return code, true
	}
	return "", false
}

func (m *ChatMessages) ClipboardCut() (string, bool) {
	return "", false
}

func (m *ChatMessages) ClipboardPaste(text string) bool {
	return false
}

var _ clipboard.Target = (*ChatMessages)(nil)
var _ scroll.Controller = (*ChatMessages)(nil)
var _ accessibility.Accessible = (*ChatMessages)(nil)
var _ runtime.Widget = (*ChatMessages)(nil)
var _ runtime.Bindable = (*ChatMessages)(nil)
var _ runtime.Unbindable = (*ChatMessages)(nil)

// AccessibleRole returns the accessibility role for the chat view.
func (m *ChatMessages) AccessibleRole() accessibility.Role {
	return accessibility.RoleList
}

// AccessibleLabel returns a label describing the chat view's current state.
func (m *ChatMessages) AccessibleLabel() string {
	if m == nil || m.buffer == nil {
		return "Chat View"
	}
	count := m.buffer.LineCount()
	if count == 0 {
		return "Chat View (empty)"
	}
	return fmt.Sprintf("Chat View (%d messages)", count)
}

// AccessibleDescription returns additional context about the chat view.
func (m *ChatMessages) AccessibleDescription() string {
	if m == nil || m.buffer == nil {
		return ""
	}
	lines := m.buffer.GetVisibleLines()
	if len(lines) == 0 {
		return ""
	}
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
func (m *ChatMessages) AccessibleState() accessibility.StateSet {
	return accessibility.StateSet{ReadOnly: true}
}

// AccessibleValue returns scroll position information.
func (m *ChatMessages) AccessibleValue() *accessibility.ValueInfo {
	if m == nil || m.buffer == nil {
		return nil
	}
	top, total, height := m.buffer.ScrollPosition()
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

// Style helper functions - extract info from backend.Style
func extractFG(s backend.Style) uint32 {
	fg, _, _ := s.Decompose()
	if fg.IsRGB() {
		r, g, b := fg.RGB()
		return (uint32(r) << 16) | (uint32(g) << 8) | uint32(b)
	}
	return uint32(fg)
}

func thinkingLineStyle(style backend.Style) scrollback.LineStyle {
	return scrollback.LineStyle{
		FG:     extractFG(style),
		Italic: true,
		Dim:    true,
	}
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
