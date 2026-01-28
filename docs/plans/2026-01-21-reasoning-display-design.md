# Reasoning Display Design

Date: 2026-01-21

## Problem

Reasoning models like `kimi-k2-thinking` output thinking content that isn't displayed to users. Two issues:

1. OpenRouter sends `reasoning_details` array, but code only parses non-existent `reasoning` string field
2. `<think>` tags in content are extracted only at stream end, not displayed during streaming
3. When model returns only reasoning without response content, an error occurs instead of showing the reasoning

## Solution

Support both reasoning formats with progressive reveal UI: stream reasoning as dimmed text, collapse to clickable preview when complete.

## Data Flow & Parsing

### Two Reasoning Sources

**`reasoning_details` array (OpenRouter format):**

Parsed at JSON level when deserializing `StreamChunk`. Extract `.text` or `.summary` from each detail and call `OnReasoning()`.

**`<think>` tags in content (Kimi K2 format):**

Parsed during streaming with state machine. Route content through `ThinkTagParser`:
- Detects `<think>` → reasoning mode → `OnReasoning()`
- Detects `</think>` → response mode → `OnText()`
- Buffers partial tags (max ~10 chars) to avoid false switches

### State Machine

```
Normal → (sees "<think>") → Reasoning → (sees "</think>") → Normal
              ↓                              ↓
         [buffer "<t.." until               [buffer "</.." until
          confirmed or rejected]             confirmed or rejected]
```

### Stream Handler Changes

```go
type StreamHandler interface {
    OnText(text string)
    OnReasoning(reasoning string)
    OnReasoningEnd()  // NEW: signals reasoning block complete
    OnToolStart(name string, arguments string)
    OnToolEnd(name string, result string, err error)
    OnComplete(result *Result)
}
```

## TUI Display

### ReasoningBlock Widget

Three states:

**Streaming state:**
- Renders incoming reasoning in dimmed/muted color
- Appends text as `OnReasoning()` is called

**Collapsed state (after `OnReasoningEnd()`):**
```
▶ "Let me analyze this step by step..." (click to expand)
```
First ~40 chars, truncated with ellipsis.

**Expanded state (user clicks):**
```
▼ "Let me analyze this step by step..."
│ Let me analyze this step by step. First, I need to understand
│ what the user is asking for. They want to display reasoning
│ content from AI models...
```
Gutter line `│` in dim color. Click again to collapse.

### Message Flow

- Response content renders below reasoning block in normal style
- Multiple reasoning blocks possible per response
- Reasoning blocks preserved in scroll-back

## Type Changes

### pkg/model/types.go

```go
// ReasoningDetail represents a single reasoning block from OpenRouter
type ReasoningDetail struct {
    Type    string `json:"type"`              // "reasoning.text", "reasoning.summary", "reasoning.encrypted"
    ID      string `json:"id"`
    Text    string `json:"text,omitempty"`    // For reasoning.text
    Summary string `json:"summary,omitempty"` // For reasoning.summary
}

// MessageDelta - add field:
type MessageDelta struct {
    Role             string            `json:"role,omitempty"`
    Content          string            `json:"content,omitempty"`
    Reasoning        string            `json:"reasoning,omitempty"`         // Legacy field
    ReasoningDetails []ReasoningDetail `json:"reasoning_details,omitempty"` // OpenRouter format
    ToolCalls        []ToolCallDelta   `json:"tool_calls,omitempty"`
}
```

## Edge Cases

### Reasoning-only responses

- Display reasoning (dimmed, then collapsed)
- Show system message: "Model provided reasoning but no response. Try rephrasing your question."
- No error thrown

### Malformed tags

- Unclosed `<think>` at stream end → treat remaining as reasoning, call `OnReasoningEnd()`
- `</think>` without opening → ignore, treat as literal text
- Nested `<think>` tags → ignore inner tags, treat as reasoning content

### Mixed formats

If model sends both `reasoning_details` AND `<think>` tags:
- `reasoning_details` takes precedence
- `<think>` tag parsing disabled when `reasoning_details` present

### Empty reasoning

- `<think></think>` or empty `reasoning_details` → no reasoning block shown
- Whitespace-only → no reasoning block shown

## Files to Modify

- `pkg/model/types.go` - Add `ReasoningDetail`, update `MessageDelta`
- `pkg/toolrunner/runner.go` - Integrate think parser, handle `reasoning_details`
- `pkg/toolrunner/think_parser.go` - New state machine (new file)
- `pkg/execution/strategy.go` - Update `StreamHandler` interface
- `pkg/execution/toolrunner_adapter.go` - Add `OnReasoningEnd()` passthrough
- `pkg/buckley/ui/tui/reasoning_block.go` - New widget (new file)
- `pkg/buckley/ui/tui/controller.go` - Update `tuiStreamHandler`
