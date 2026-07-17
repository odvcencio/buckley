// Package turntrace emits a structured, machine-readable record of each chat
// turn so a chat loop can be diagnosed headlessly — without a human reproducing
// the issue in an interactive session. Unlike the raw network log (wire bytes
// only, truncated, no streaming bodies), a turn record also captures buckley's
// own decisions: whether tools were offered and why, whether the model's output
// contained structured tool_calls vs. leaked inline tool-call markup, which
// branch the loop took, and the reasoning effort actually sent.
//
// The Tracer is nil-safe: every method on a nil *Tracer is a no-op, so callers
// can write `tracer := turntrace.Open(...)` and unconditionally call Record
// without guarding on whether tracing is enabled.
package turntrace

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
)

// ToolCallPreview is a compact view of a tool call the model requested.
type ToolCallPreview struct {
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
	Args string `json:"args,omitempty"`
}

// TurnRecord is one iteration of a chat loop: the request buckley built, the
// model's decoded response, and the branch the loop took.
type TurnRecord struct {
	Iteration int    `json:"iteration"`
	Model     string `json:"model"`

	// Request shape + the tool-gating decision (the common "why no tools" cause).
	NumMessages         int      `json:"num_messages"`
	Roles               []string `json:"roles,omitempty"`
	UseTools            bool     `json:"use_tools"`
	SupportsTools       bool     `json:"supports_tools"`
	SupportedParameters []string `json:"supported_parameters,omitempty"`
	ToolsOffered        int      `json:"tools_offered"`
	ToolNames           []string `json:"tool_names,omitempty"`
	ToolChoice          string   `json:"tool_choice,omitempty"`
	ReasoningEffort     string   `json:"reasoning_effort,omitempty"`

	// Decoded response.
	FinishReason         string            `json:"finish_reason,omitempty"`
	ContentPreview       string            `json:"content_preview,omitempty"`
	ReasoningChars       int               `json:"reasoning_chars,omitempty"`
	StructuredToolCalls  []ToolCallPreview `json:"structured_tool_calls,omitempty"`
	InlineMarkupDetected bool              `json:"inline_markup_detected"`

	// Loop decision: "finalize" | "nudge" | "tool_calls" | "error".
	Branch string `json:"branch"`
	Error  string `json:"error,omitempty"`
}

// Tracer appends TurnRecords as JSON Lines. A nil *Tracer is a valid no-op.
type Tracer struct {
	mu sync.Mutex
	f  *os.File
}

// Open opens (creating/appending) a JSONL trace file at path. A blank path
// disables tracing and returns (nil, nil) so callers get a no-op Tracer.
func Open(path string) (*Tracer, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Tracer{f: f}, nil
}

// Record appends one turn record. No-op on a nil Tracer or marshal failure.
func (t *Tracer) Record(rec TurnRecord) {
	if t == nil {
		return
	}
	rec.ContentPreview = truncate(rec.ContentPreview, 2000)
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.f.Write(append(data, '\n'))
}

// Close closes the trace file. No-op on a nil Tracer.
func (t *Tracer) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.f.Close()
}

// inlineMarkers are the inline tool-call token formats models emit inside the
// content field instead of a structured tool_calls array. When any of these
// appear in content, a non-parsing loop mistakes a tool call for a final answer.
var inlineMarkers = []string{
	"<|tool_call_begin|>",
	"<|tool_calls_section_begin|>",
	"<|tool_call_argument_begin|>",
	"<tool_call>",
	"<tool_call ",
}

// InlineToolMarkupDetected reports whether content carries inline tool-call
// markup that a structured-only decoder would leak as plain text.
func InlineToolMarkupDetected(content string) bool {
	for _, m := range inlineMarkers {
		if strings.Contains(content, m) {
			return true
		}
	}
	return false
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…[truncated]"
}
