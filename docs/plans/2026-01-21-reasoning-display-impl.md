# Reasoning Display Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Display model reasoning content progressively during streaming, with collapsible UI after completion.

**Architecture:** Add `ReasoningDetail` type for OpenRouter format, create `ThinkTagParser` state machine for `<think>` tag streaming, add `OnReasoningEnd()` callback, update TUI with collapsible reasoning blocks.

**Tech Stack:** Go, fluffy-ui framework (`github.com/odvcencio/fluffy-ui`), scrollback buffer, existing widget system

---

## Task 1: Add ReasoningDetail Type

**Files:**
- Modify: `pkg/model/types.go:150-156`
- Test: `pkg/model/types_test.go` (create if needed)

**Step 1: Add ReasoningDetail struct and update MessageDelta**

In `pkg/model/types.go`, add after line 156:

```go
// ReasoningDetail represents a reasoning block from OpenRouter's reasoning_details format.
type ReasoningDetail struct {
	Type    string `json:"type"`              // "reasoning.text", "reasoning.summary", "reasoning.encrypted"
	ID      string `json:"id,omitempty"`
	Index   int    `json:"index,omitempty"`
	Text    string `json:"text,omitempty"`    // For reasoning.text
	Summary string `json:"summary,omitempty"` // For reasoning.summary
	Format  string `json:"format,omitempty"`
}
```

Update `MessageDelta` struct to add `ReasoningDetails` field:

```go
type MessageDelta struct {
	Role             string            `json:"role,omitempty"`
	Content          string            `json:"content,omitempty"`
	Reasoning        string            `json:"reasoning,omitempty"`
	ReasoningDetails []ReasoningDetail `json:"reasoning_details,omitempty"`
	ToolCalls        []ToolCallDelta   `json:"tool_calls,omitempty"`
}
```

**Step 2: Verify compilation**

Run: `go build ./pkg/model/...`
Expected: SUCCESS

---

## Task 2: Add OnReasoningEnd to StreamHandler Interfaces

**Files:**
- Modify: `pkg/toolrunner/runner.go:83-90`
- Modify: `pkg/execution/strategy.go:103-119`
- Modify: `pkg/execution/toolrunner_adapter.go`
- Modify: `pkg/execution/rlm_stream_adapter.go`
- Modify: `pkg/agent/executor.go:219-254`
- Modify: `pkg/buckley/ui/tui/controller.go:1511-1540`

**Step 1: Update toolrunner StreamHandler interface**

In `pkg/toolrunner/runner.go`, update the interface:

```go
type StreamHandler interface {
	OnText(text string)
	OnReasoning(reasoning string)
	OnReasoningEnd()
	OnToolStart(name string, arguments string)
	OnToolEnd(name string, result string, err error)
	OnComplete(result *Result)
}
```

**Step 2: Update execution StreamHandler interface**

In `pkg/execution/strategy.go`, update:

```go
type StreamHandler interface {
	// OnText is called when text content is generated.
	OnText(text string)

	// OnReasoning is called when reasoning content is generated (thinking models).
	OnReasoning(reasoning string)

	// OnReasoningEnd is called when a reasoning block completes.
	OnReasoningEnd()

	// OnToolStart is called when a tool execution begins.
	OnToolStart(name string, arguments string)

	// OnToolEnd is called when a tool execution completes.
	OnToolEnd(name string, result string, err error)

	// OnComplete is called when execution finishes.
	OnComplete(result *ExecutionResult)
}
```

**Step 3: Update toolrunner_adapter.go**

In `pkg/execution/toolrunner_adapter.go`, add:

```go
func (a *toolrunnerStreamAdapter) OnReasoningEnd() {
	if a.handler != nil {
		a.handler.OnReasoningEnd()
	}
}
```

**Step 4: Update rlm_stream_adapter.go**

In `pkg/execution/rlm_stream_adapter.go`, add method to satisfy interface (find where other On* methods are and add):

```go
func (a *RLMStreamAdapter) OnReasoningEnd() {
	if a.handler != nil {
		a.handler.OnReasoningEnd()
	}
}
```

**Step 5: Update agent executor**

In `pkg/agent/executor.go`, add to `executorStreamHandler`:

```go
func (h *executorStreamHandler) OnReasoningEnd() {}
```

**Step 6: Update TUI controller**

In `pkg/buckley/ui/tui/controller.go`, add to `tuiStreamHandler`:

```go
func (h *tuiStreamHandler) OnReasoningEnd() {
	// Will be implemented in Task 5
}
```

**Step 7: Verify compilation**

Run: `go build ./...`
Expected: SUCCESS

---

## Task 3: Create ThinkTagParser

**Files:**
- Create: `pkg/toolrunner/think_parser.go`
- Create: `pkg/toolrunner/think_parser_test.go`

**Step 1: Write failing tests**

Create `pkg/toolrunner/think_parser_test.go`:

```go
package toolrunner

import (
	"strings"
	"testing"
)

func TestThinkTagParser_BasicThinkTag(t *testing.T) {
	var reasoning, text strings.Builder
	var reasoningEnded bool

	p := NewThinkTagParser(
		func(s string) { reasoning.WriteString(s) },
		func(s string) { text.WriteString(s) },
		func() { reasoningEnded = true },
	)

	p.Write("<think>I am thinking</think>Hello world")
	p.Flush()

	if reasoning.String() != "I am thinking" {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), "I am thinking")
	}
	if text.String() != "Hello world" {
		t.Errorf("text = %q, want %q", text.String(), "Hello world")
	}
	if !reasoningEnded {
		t.Error("OnReasoningEnd was not called")
	}
}

func TestThinkTagParser_StreamedChunks(t *testing.T) {
	var reasoning, text strings.Builder
	var reasoningEndCount int

	p := NewThinkTagParser(
		func(s string) { reasoning.WriteString(s) },
		func(s string) { text.WriteString(s) },
		func() { reasoningEndCount++ },
	)

	// Simulate streaming chunks
	chunks := []string{"<thi", "nk>thinking", " content</th", "ink>response"}
	for _, chunk := range chunks {
		p.Write(chunk)
	}
	p.Flush()

	if reasoning.String() != "thinking content" {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), "thinking content")
	}
	if text.String() != "response" {
		t.Errorf("text = %q, want %q", text.String(), "response")
	}
	if reasoningEndCount != 1 {
		t.Errorf("reasoningEndCount = %d, want 1", reasoningEndCount)
	}
}

func TestThinkTagParser_NoThinkTags(t *testing.T) {
	var reasoning, text strings.Builder

	p := NewThinkTagParser(
		func(s string) { reasoning.WriteString(s) },
		func(s string) { text.WriteString(s) },
		func() {},
	)

	p.Write("Just normal text without tags")
	p.Flush()

	if reasoning.String() != "" {
		t.Errorf("reasoning = %q, want empty", reasoning.String())
	}
	if text.String() != "Just normal text without tags" {
		t.Errorf("text = %q, want %q", text.String(), "Just normal text without tags")
	}
}

func TestThinkTagParser_UnclosedTag(t *testing.T) {
	var reasoning, text strings.Builder
	var reasoningEnded bool

	p := NewThinkTagParser(
		func(s string) { reasoning.WriteString(s) },
		func(s string) { text.WriteString(s) },
		func() { reasoningEnded = true },
	)

	p.Write("<think>unclosed reasoning content")
	p.Flush()

	// Unclosed tag: treat as reasoning, call OnReasoningEnd on Flush
	if reasoning.String() != "unclosed reasoning content" {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), "unclosed reasoning content")
	}
	if text.String() != "" {
		t.Errorf("text = %q, want empty", text.String())
	}
	if !reasoningEnded {
		t.Error("OnReasoningEnd should be called on Flush for unclosed tag")
	}
}

func TestThinkTagParser_MultipleThinkBlocks(t *testing.T) {
	var reasoning, text strings.Builder
	var reasoningEndCount int

	p := NewThinkTagParser(
		func(s string) { reasoning.WriteString(s) },
		func(s string) { text.WriteString(s) },
		func() { reasoningEndCount++ },
	)

	p.Write("<think>first</think>middle<think>second</think>end")
	p.Flush()

	if reasoning.String() != "firstsecond" {
		t.Errorf("reasoning = %q, want %q", reasoning.String(), "firstsecond")
	}
	if text.String() != "middleend" {
		t.Errorf("text = %q, want %q", text.String(), "middleend")
	}
	if reasoningEndCount != 2 {
		t.Errorf("reasoningEndCount = %d, want 2", reasoningEndCount)
	}
}

func TestThinkTagParser_EmptyThinkTag(t *testing.T) {
	var reasoning, text strings.Builder
	var reasoningEnded bool

	p := NewThinkTagParser(
		func(s string) { reasoning.WriteString(s) },
		func(s string) { text.WriteString(s) },
		func() { reasoningEnded = true },
	)

	p.Write("<think></think>content")
	p.Flush()

	if reasoning.String() != "" {
		t.Errorf("reasoning = %q, want empty", reasoning.String())
	}
	if text.String() != "content" {
		t.Errorf("text = %q, want %q", text.String(), "content")
	}
	// Empty think tag should not trigger OnReasoningEnd
	if reasoningEnded {
		t.Error("OnReasoningEnd should not be called for empty think tag")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./pkg/toolrunner/... -run TestThinkTagParser -v`
Expected: FAIL (ThinkTagParser not defined)

**Step 3: Implement ThinkTagParser**

Create `pkg/toolrunner/think_parser.go`:

```go
package toolrunner

// ThinkTagParser parses streaming content for <think> tags,
// routing reasoning content and regular text to separate callbacks.
type ThinkTagParser struct {
	onReasoning    func(string)
	onText         func(string)
	onReasoningEnd func()

	buffer       []byte
	inThinkTag   bool
	hasReasoning bool // Track if any non-empty reasoning was emitted
}

// NewThinkTagParser creates a parser that routes content to callbacks.
func NewThinkTagParser(onReasoning, onText func(string), onReasoningEnd func()) *ThinkTagParser {
	return &ThinkTagParser{
		onReasoning:    onReasoning,
		onText:         onText,
		onReasoningEnd: onReasoningEnd,
	}
}

// Write processes a chunk of streaming content.
func (p *ThinkTagParser) Write(chunk string) {
	for i := 0; i < len(chunk); i++ {
		c := chunk[i]
		p.buffer = append(p.buffer, c)

		if p.inThinkTag {
			// Look for </think>
			if p.bufferEndsWith("</think>") {
				// Remove </think> from buffer and emit reasoning
				content := string(p.buffer[:len(p.buffer)-8])
				if content != "" {
					p.onReasoning(content)
					p.hasReasoning = true
				}
				p.buffer = p.buffer[:0]
				p.inThinkTag = false
				if p.hasReasoning {
					p.onReasoningEnd()
					p.hasReasoning = false
				}
			}
		} else {
			// Look for <think>
			if p.bufferEndsWith("<think>") {
				// Emit any text before <think>
				content := string(p.buffer[:len(p.buffer)-7])
				if content != "" {
					p.onText(content)
				}
				p.buffer = p.buffer[:0]
				p.inThinkTag = true
			}
		}
	}

	// Flush completed content that can't be part of a tag
	p.flushSafeContent()
}

// bufferEndsWith checks if buffer ends with the given suffix.
func (p *ThinkTagParser) bufferEndsWith(suffix string) bool {
	if len(p.buffer) < len(suffix) {
		return false
	}
	return string(p.buffer[len(p.buffer)-len(suffix):]) == suffix
}

// flushSafeContent emits content that can't possibly be part of a tag.
func (p *ThinkTagParser) flushSafeContent() {
	// Keep potential partial tags in buffer
	// Max partial tag length is 7 for "<think>" or 8 for "</think>"
	maxPartial := 8
	if len(p.buffer) <= maxPartial {
		return
	}

	safeLen := len(p.buffer) - maxPartial
	safe := string(p.buffer[:safeLen])
	p.buffer = p.buffer[safeLen:]

	if safe != "" {
		if p.inThinkTag {
			p.onReasoning(safe)
			p.hasReasoning = true
		} else {
			p.onText(safe)
		}
	}
}

// Flush emits any remaining buffered content.
func (p *ThinkTagParser) Flush() {
	if len(p.buffer) == 0 {
		return
	}

	content := string(p.buffer)
	p.buffer = p.buffer[:0]

	if content != "" {
		if p.inThinkTag {
			p.onReasoning(content)
			p.hasReasoning = true
		} else {
			p.onText(content)
		}
	}

	// If we were in a think tag (unclosed), signal end
	if p.inThinkTag && p.hasReasoning {
		p.onReasoningEnd()
		p.hasReasoning = false
	}
	p.inThinkTag = false
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./pkg/toolrunner/... -run TestThinkTagParser -v`
Expected: PASS

---

## Task 4: Integrate ThinkTagParser and ReasoningDetails into Runner

**Files:**
- Modify: `pkg/toolrunner/runner.go:475-520`
- Modify: `pkg/toolrunner/runner_test.go`

**Step 1: Write failing test for reasoning_details**

Add to `pkg/toolrunner/runner_test.go`:

```go
func TestRunner_ReasoningDetails(t *testing.T) {
	registry := tool.NewRegistry()

	mockClient := &MockModelClient{}

	runner := New(Config{
		Models:          mockClient,
		Registry:        registry,
		EnableReasoning: true,
	})

	var reasoningText string
	var reasoningEnded bool
	runner.SetStreamHandler(&testStreamHandler{
		onReasoning:    func(s string) { reasoningText += s },
		onReasoningEnd: func() { reasoningEnded = true },
	})

	// Simulate OpenRouter response with reasoning_details
	mockClient.StreamResponses = [][]model.StreamChunk{{
		{
			Choices: []model.StreamChoice{{
				Delta: model.MessageDelta{
					ReasoningDetails: []model.ReasoningDetail{{
						Type: "reasoning.text",
						Text: "Let me think about this...",
					}},
				},
			}},
		},
		{
			Choices: []model.StreamChoice{{
				Delta: model.MessageDelta{
					Content: "The answer is 42.",
				},
				FinishReason: ptr("stop"),
			}},
		},
	}}

	result, err := runner.Run(context.Background(), Request{
		Messages: []model.Message{{Role: "user", Content: "test"}},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reasoningText != "Let me think about this..." {
		t.Errorf("reasoning = %q, want %q", reasoningText, "Let me think about this...")
	}
	if !reasoningEnded {
		t.Error("OnReasoningEnd was not called")
	}
	if result.Content != "The answer is 42." {
		t.Errorf("content = %q, want %q", result.Content, "The answer is 42.")
	}
}

type testStreamHandler struct {
	onText         func(string)
	onReasoning    func(string)
	onReasoningEnd func()
}

func (h *testStreamHandler) OnText(s string)                         { if h.onText != nil { h.onText(s) } }
func (h *testStreamHandler) OnReasoning(s string)                    { if h.onReasoning != nil { h.onReasoning(s) } }
func (h *testStreamHandler) OnReasoningEnd()                         { if h.onReasoningEnd != nil { h.onReasoningEnd() } }
func (h *testStreamHandler) OnToolStart(name, args string)           {}
func (h *testStreamHandler) OnToolEnd(name, result string, err error) {}
func (h *testStreamHandler) OnComplete(result *Result)               {}

func ptr(s string) *string { return &s }
```

**Step 2: Update runner.go streaming logic**

In `pkg/toolrunner/runner.go`, replace the streaming content handling section (around lines 475-490) with integrated parsing:

Find this section and update `executeWithTools` to use ThinkTagParser:

1. Add think parser initialization before the streaming loop
2. Route content through parser instead of directly to OnText
3. Handle reasoning_details from delta
4. Call parser.Flush() after streaming ends
5. Update the reasoning-only error handling to show reasoning instead of error

The key changes in the streaming loop:

```go
// Before the streaming for loop, initialize parser:
var thinkParser *ThinkTagParser
var hasReasoningDetails bool

if r.streamHandler != nil {
	thinkParser = NewThinkTagParser(
		r.streamHandler.OnReasoning,
		r.streamHandler.OnText,
		r.streamHandler.OnReasoningEnd,
	)
}

// In the streaming loop, replace the OnText/OnReasoning calls:
if r.streamHandler != nil && len(chunk.Choices) > 0 {
	delta := chunk.Choices[0].Delta

	// Handle reasoning_details (OpenRouter format)
	for _, rd := range delta.ReasoningDetails {
		hasReasoningDetails = true
		text := rd.Text
		if text == "" {
			text = rd.Summary
		}
		if text != "" {
			r.streamHandler.OnReasoning(text)
		}
	}

	// Handle legacy reasoning field
	if delta.Reasoning != "" && !hasReasoningDetails {
		r.streamHandler.OnReasoning(delta.Reasoning)
	}

	// Handle content - route through think parser unless reasoning_details present
	if delta.Content != "" {
		filtered := model.FilterToolCallTokens(delta.Content)
		if filtered != "" {
			if hasReasoningDetails {
				// reasoning_details takes precedence, don't parse think tags
				r.streamHandler.OnText(filtered)
			} else {
				thinkParser.Write(filtered)
			}
		}
	}
}

// After the streaming loop ends, flush the parser:
if thinkParser != nil {
	thinkParser.Flush()
}
```

Also update the reasoning-only response handling (around line 516-521) to NOT return an error:

```go
if strings.TrimSpace(content) == "" {
	if result.Reasoning != "" {
		// Model provided reasoning but no response - this is valid
		result.Content = ""  // Leave content empty, reasoning is in result.Reasoning
		if r.streamHandler != nil {
			r.streamHandler.OnComplete(result)
		}
		return result, nil
	}
	return result, fmt.Errorf("model returned empty response")
}
```

**Step 3: Run tests**

Run: `go test ./pkg/toolrunner/... -v`
Expected: PASS

---

## Task 5: Add TUI Message Types for Reasoning

**Files:**
- Modify: `pkg/buckley/ui/tui/messages.go`

**Step 1: Add ReasoningMsg and ReasoningEndMsg types**

In `pkg/buckley/ui/tui/messages.go`, add after `ThinkingMsg`:

```go
// ReasoningMsg streams reasoning content to the display.
type ReasoningMsg struct {
	Text string // Incremental reasoning text to append
}

func (ReasoningMsg) isMessage() {}

// ReasoningEndMsg signals reasoning block is complete and should collapse.
type ReasoningEndMsg struct {
	Preview string // First ~40 chars for collapsed view
	Full    string // Full reasoning content
}

func (ReasoningEndMsg) isMessage() {}
```

**Step 2: Verify compilation**

Run: `go build ./pkg/buckley/ui/tui/...`
Expected: SUCCESS

---

## Task 6: Add ChatView Reasoning Methods

**Files:**
- Modify: `pkg/buckley/ui/widgets/chatview.go`
- Modify: `pkg/buckley/ui/scrollback/buffer.go`

**Step 1: Add reasoning line tracking to scrollback Buffer**

In `pkg/buckley/ui/scrollback/buffer.go`, add fields and methods:

```go
// Add to Buffer struct:
type Buffer struct {
	// ... existing fields ...

	// Reasoning block tracking
	reasoningStart    int  // Line index where current reasoning block starts
	reasoningEnd      int  // Line index where current reasoning block ends
	hasReasoningBlock bool
	reasoningExpanded bool
	reasoningPreview  string
	reasoningFull     string
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

	// Remove all reasoning lines
	if b.reasoningStart < len(b.lines) {
		b.lines = b.lines[:b.reasoningStart]
	}

	// Add collapsed preview line
	b.reasoningPreview = preview
	b.reasoningFull = full
	b.reasoningExpanded = false

	collapsedText := "▶ \"" + preview + "...\" (click to expand)"
	line := Line{
		Content:   collapsedText,
		Style:     collapsedStyle,
		Timestamp: time.Now(),
		Source:    "reasoning-collapsed",
	}
	b.appendLineLocked(line, false)
	b.reasoningEnd = len(b.lines)
}

// ToggleReasoningBlock expands or collapses the reasoning block.
func (b *Buffer) ToggleReasoningBlock(expandedStyle, collapsedStyle LineStyle) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.hasReasoningBlock || b.reasoningFull == "" {
		return
	}

	// Remove current reasoning display
	if b.reasoningStart < len(b.lines) {
		b.lines = b.lines[:b.reasoningStart]
	}

	b.reasoningExpanded = !b.reasoningExpanded

	if b.reasoningExpanded {
		// Show expanded with gutter
		headerLine := Line{
			Content:   "▼ \"" + b.reasoningPreview + "...\"",
			Style:     expandedStyle,
			Timestamp: time.Now(),
			Source:    "reasoning-header",
		}
		b.appendLineLocked(headerLine, false)

		// Add full content with gutter prefix
		for _, contentLine := range strings.Split(b.reasoningFull, "\n") {
			line := Line{
				Content:   "│ " + contentLine,
				Style:     expandedStyle,
				Timestamp: time.Now(),
				Source:    "reasoning-content",
			}
			b.appendLineLocked(line, false)
		}
	} else {
		// Show collapsed
		collapsedText := "▶ \"" + b.reasoningPreview + "...\" (click to expand)"
		line := Line{
			Content:   collapsedText,
			Style:     collapsedStyle,
			Timestamp: time.Now(),
			Source:    "reasoning-collapsed",
		}
		b.appendLineLocked(line, false)
	}
	b.reasoningEnd = len(b.lines)
}

// ClearReasoningBlock clears reasoning block tracking (call when new message starts).
func (b *Buffer) ClearReasoningBlock() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hasReasoningBlock = false
	b.reasoningExpanded = false
	b.reasoningPreview = ""
	b.reasoningFull = ""
}
```

**Step 2: Add ChatView reasoning methods**

In `pkg/buckley/ui/widgets/chatview.go`:

```go
// AppendReasoning appends streaming reasoning text (dimmed).
func (c *ChatView) AppendReasoning(text string) {
	c.buffer.AppendReasoningLine(text, scrollback.LineStyle{
		FG:     extractFG(c.thinkingStyle),
		Italic: true,
		Dim:    true,
	})
}

// CollapseReasoning collapses reasoning block to preview.
func (c *ChatView) CollapseReasoning(preview, full string) {
	c.buffer.ReplaceReasoningBlock(preview, full, scrollback.LineStyle{
		FG:     extractFG(c.thinkingStyle),
		Italic: true,
		Dim:    true,
	})
}

// ToggleReasoning expands or collapses the reasoning block.
func (c *ChatView) ToggleReasoning() {
	style := scrollback.LineStyle{
		FG:     extractFG(c.thinkingStyle),
		Italic: true,
		Dim:    true,
	}
	c.buffer.ToggleReasoningBlock(style, style)
}

// ClearReasoningBlock clears reasoning state for new message.
func (c *ChatView) ClearReasoningBlock() {
	c.buffer.ClearReasoningBlock()
}
```

**Step 3: Verify compilation**

Run: `go build ./pkg/buckley/ui/...`
Expected: SUCCESS

---

## Task 7: Wire Up TUI Controller and App

**Files:**
- Modify: `pkg/buckley/ui/tui/controller.go`
- Modify: `pkg/buckley/ui/tui/app_widget.go`

**Step 1: Update tuiStreamHandler**

In `pkg/buckley/ui/tui/controller.go`, update `tuiStreamHandler`:

```go
type tuiStreamHandler struct {
	ctrl         *Controller
	app          *WidgetApp
	reasoning    strings.Builder
	hasReasoning bool
}

func (h *tuiStreamHandler) OnText(text string) {
	// Existing behavior - text handled elsewhere
}

func (h *tuiStreamHandler) OnReasoning(reasoning string) {
	h.reasoning.WriteString(reasoning)
	h.hasReasoning = true
	h.app.AppendReasoning(reasoning)
}

func (h *tuiStreamHandler) OnReasoningEnd() {
	if h.hasReasoning {
		full := h.reasoning.String()
		preview := full
		if len(preview) > 40 {
			preview = preview[:40]
		}
		h.app.CollapseReasoning(preview, full)
	}
	h.reasoning.Reset()
	h.hasReasoning = false
}
```

**Step 2: Add WidgetApp methods**

In `pkg/buckley/ui/tui/app_widget.go`:

```go
// AppendReasoning appends reasoning text to display. Thread-safe.
func (a *WidgetApp) AppendReasoning(text string) {
	a.Post(ReasoningMsg{Text: text})
}

// CollapseReasoning collapses reasoning to preview. Thread-safe.
func (a *WidgetApp) CollapseReasoning(preview, full string) {
	a.Post(ReasoningEndMsg{Preview: preview, Full: full})
}
```

**Step 3: Handle messages in Update loop**

In `pkg/buckley/ui/tui/app_widget.go`, add to the message handling switch:

```go
case ReasoningMsg:
	a.chatView.AppendReasoning(m.Text)
	a.statusBar.SetStatus("Thinking...")

case ReasoningEndMsg:
	a.chatView.CollapseReasoning(m.Preview, m.Full)
```

**Step 4: Verify compilation and test**

Run: `go build ./...`
Manual test: `./buckley -m moonshotai/kimi-k2-thinking`

---

## Summary

After completing all tasks:

1. **Task 1**: ReasoningDetail type added to model package
2. **Task 2**: OnReasoningEnd callback added to all StreamHandler implementations
3. **Task 3**: ThinkTagParser created with tests
4. **Task 4**: Runner integrated with parser and reasoning_details
5. **Task 5**: TUI message types added (ReasoningMsg, ReasoningEndMsg)
6. **Task 6**: ChatView and scrollback Buffer reasoning methods added
7. **Task 7**: TUI controller and app wired up

Test end-to-end with:
```bash
./buckley -m moonshotai/kimi-k2-thinking
```

Ask a question and verify:
- Reasoning appears dimmed while streaming
- Reasoning collapses to preview when response starts
- Clicking preview expands full reasoning
- No error when model returns only reasoning
