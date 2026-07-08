package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// These fixtures mirror real payloads observed from reasoning models
// (GLM-5.2, Qwen agentic checkpoints) routed through OpenRouter/vLLM, where
// the tool call arrives as text inside the assistant message content
// instead of the OpenAI-standard structured `tool_calls` field.
const (
	qwenHermesToolCallPayload = "<tool_call>\n" +
		`{"name": "get_weather", "arguments": {"location": "San Francisco", "unit": "celsius"}}` +
		"\n</tool_call>"

	glmNativeArgTagPayload = "<tool_call>read_file\n" +
		"<arg_key>path</arg_key>\n" +
		"<arg_value>pkg/rlm/subagent.go</arg_value>\n" +
		"</tool_call>"

	glmJSONFencePayload = "I'll write the file now.\n\n" +
		"```json\n" +
		`{"name": "write_file", "arguments": {"path": "notes.md", "content": "hello world"}}` +
		"\n```\n"

	// glmNumericQuirkPayload reproduces the exact live bug: GLM-5.2 tool-call
	// argument JSON with a stray space injected right after a leading '-' in
	// a numeric literal, which is the one code path in encoding/json's
	// scanner that emits the literal reported error text "invalid character
	// ' ' in numeric literal" (as opposed to the "after decimal point" / "in
	// exponent" variants -- see pkg/jsonrepair for the full scanner-state
	// analysis).
	glmNumericQuirkPayload = "<tool_call>\n" +
		`{"name": "generate_pull_request", "arguments": {"title": "fix bug", "confidence": - 5}}` +
		"\n</tool_call>"
)

func TestParseTextToolCalls_QwenHermesJSONTag(t *testing.T) {
	got := ParseTextToolCalls(qwenHermesToolCallPayload)

	if !got.Detected {
		t.Fatalf("expected Detected=true for Hermes/Qwen <tool_call> JSON payload")
	}
	if len(got.Calls) != 1 {
		t.Fatalf("expected 1 parsed call, got %d (reason=%q)", len(got.Calls), got.Reason)
	}

	call := got.Calls[0]
	if call.Type != "function" {
		t.Errorf("Type = %q, want %q", call.Type, "function")
	}
	if call.Function.Name != "get_weather" {
		t.Errorf("Function.Name = %q, want %q", call.Function.Name, "get_weather")
	}
	if call.ID == "" {
		t.Errorf("expected a synthesized non-empty ID")
	}

	var args struct {
		Location string `json:"location"`
		Unit     string `json:"unit"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v (arguments=%q)", err, call.Function.Arguments)
	}
	if args.Location != "San Francisco" || args.Unit != "celsius" {
		t.Errorf("unexpected arguments: %+v", args)
	}
}

func TestParseTextToolCalls_GLMNativeArgKeyValueTag(t *testing.T) {
	got := ParseTextToolCalls(glmNativeArgTagPayload)

	if !got.Detected {
		t.Fatalf("expected Detected=true for GLM native <arg_key>/<arg_value> payload")
	}
	if len(got.Calls) != 1 {
		t.Fatalf("expected 1 parsed call, got %d (reason=%q)", len(got.Calls), got.Reason)
	}

	call := got.Calls[0]
	if call.Function.Name != "read_file" {
		t.Errorf("Function.Name = %q, want %q", call.Function.Name, "read_file")
	}

	var args map[string]string
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v (arguments=%q)", err, call.Function.Arguments)
	}
	if args["path"] != "pkg/rlm/subagent.go" {
		t.Errorf("unexpected arguments: %+v", args)
	}
}

func TestParseTextToolCalls_JSONFencedPayload(t *testing.T) {
	got := ParseTextToolCalls(glmJSONFencePayload)

	if !got.Detected {
		t.Fatalf("expected Detected=true for ```json fenced tool-call payload")
	}
	if len(got.Calls) != 1 {
		t.Fatalf("expected 1 parsed call, got %d (reason=%q)", len(got.Calls), got.Reason)
	}

	call := got.Calls[0]
	if call.Function.Name != "write_file" {
		t.Errorf("Function.Name = %q, want %q", call.Function.Name, "write_file")
	}

	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v (arguments=%q)", err, call.Function.Arguments)
	}
	if args.Path != "notes.md" || args.Content != "hello world" {
		t.Errorf("unexpected arguments: %+v", args)
	}
}

// TestParseTextToolCalls_GLMNumericLiteralQuirk proves the parser recovers a
// dispatchable call AND produces valid, unmarshalable JSON arguments even
// when GLM's tool-call JSON has the stray-whitespace-in-numeric-literal
// quirk that otherwise fails downstream with "unmarshal tool call: invalid
// character ' ' in numeric literal" (the live bug report).
func TestParseTextToolCalls_GLMNumericLiteralQuirk(t *testing.T) {
	got := ParseTextToolCalls(glmNumericQuirkPayload)

	if !got.Detected || len(got.Calls) != 1 {
		t.Fatalf("expected 1 detected call, got Detected=%v Calls=%d Reason=%q", got.Detected, len(got.Calls), got.Reason)
	}

	call := got.Calls[0]
	if call.Function.Name != "generate_pull_request" {
		t.Errorf("Function.Name = %q, want %q", call.Function.Name, "generate_pull_request")
	}

	// The critical assertion: Function.Arguments must be *valid* JSON that a
	// plain json.Unmarshal can decode, proving the numeric literal was
	// repaired rather than passed through broken.
	var args struct {
		Title      string  `json:"title"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("recovered arguments are not valid JSON after repair: %v (arguments=%q)", err, call.Function.Arguments)
	}
	if args.Title != "fix bug" {
		t.Errorf("Title = %q, want %q", args.Title, "fix bug")
	}
	if args.Confidence != -5 {
		t.Errorf("Confidence = %v, want -5", args.Confidence)
	}
}

func TestParseTextToolCalls_MultipleParallelCalls(t *testing.T) {
	content := "<tool_call>\n" +
		`{"name": "read_file", "arguments": {"path": "a.go"}}` +
		"\n</tool_call>\n<tool_call>\n" +
		`{"name": "read_file", "arguments": {"path": "b.go"}}` +
		"\n</tool_call>"

	got := ParseTextToolCalls(content)
	if !got.Detected {
		t.Fatalf("expected Detected=true")
	}
	if len(got.Calls) != 2 {
		t.Fatalf("expected 2 parsed calls, got %d (reason=%q)", len(got.Calls), got.Reason)
	}
	if got.Calls[0].ID == got.Calls[1].ID {
		t.Errorf("expected distinct synthesized IDs for parallel calls, both were %q", got.Calls[0].ID)
	}
	if got.Calls[0].Function.Arguments == got.Calls[1].Function.Arguments {
		t.Errorf("expected distinct arguments for the two calls")
	}
}

func TestParseTextToolCalls_ArgumentsAsJSONEncodedString(t *testing.T) {
	// Some providers emit "arguments" as a JSON string containing JSON
	// (rather than a nested object), e.g. "arguments": "{\"query\":\"foo\"}".
	content := `<tool_call>{"name": "search", "arguments": "{\"query\": \"foo\"}"}</tool_call>`

	got := ParseTextToolCalls(content)
	if !got.Detected || len(got.Calls) != 1 {
		t.Fatalf("expected 1 detected call, got Detected=%v Calls=%d Reason=%q", got.Detected, len(got.Calls), got.Reason)
	}
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(got.Calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v (arguments=%q)", err, got.Calls[0].Function.Arguments)
	}
	if args.Query != "foo" {
		t.Errorf("unexpected arguments: %+v", args)
	}
}

func TestParseTextToolCalls_DetectedButUnparsable(t *testing.T) {
	// Malformed JSON inside <tool_call> tags that jsonrepair.Repair cannot
	// fix (a bare identifier, not the numeric-literal-spacing quirk): must
	// be reported as Detected=true with no calls, NOT silently ignored
	// (silently ignoring this is exactly the bug -- the raw text would leak
	// out as the final answer).
	content := `<tool_call>{"name": "foo", "arguments": {not valid json}}</tool_call>`

	got := ParseTextToolCalls(content)
	if !got.Detected {
		t.Fatalf("expected Detected=true for a tagged-but-malformed payload")
	}
	if len(got.Calls) != 0 {
		t.Fatalf("expected no successfully parsed calls, got %d", len(got.Calls))
	}
	if got.Reason == "" {
		t.Errorf("expected a non-empty Reason explaining the parse failure")
	}
}

func TestParseTextToolCalls_MissingNameField(t *testing.T) {
	content := `<tool_call>{"arguments": {"path": "x"}}</tool_call>`

	got := ParseTextToolCalls(content)
	if !got.Detected {
		t.Fatalf("expected Detected=true (tagged payload with no usable name)")
	}
	if len(got.Calls) != 0 {
		t.Fatalf("expected no successfully parsed calls, got %d", len(got.Calls))
	}
	if got.Reason == "" {
		t.Errorf("expected a non-empty Reason")
	}
}

func TestParseTextToolCalls_NoFalsePositiveOnPlainText(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"plain prose", "I read the file and everything looks correct. No changes needed."},
		{
			"json fence without a tool-call shape",
			"Here's an example config:\n```json\n{\"foo\": \"bar\"}\n```\nLet me know if that works.",
		},
		{
			"prose mentioning tool_call as a word, not a tag",
			"I considered using tool_call semantics but decided plain text was clearer.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseTextToolCalls(tt.content)
			if got.Detected {
				t.Errorf("expected Detected=false for %q, got Detected=true (Reason=%q, Calls=%d)", tt.content, got.Reason, len(got.Calls))
			}
			if len(got.Calls) != 0 {
				t.Errorf("expected no calls for %q, got %d", tt.content, len(got.Calls))
			}
		})
	}
}

// TestParseTextToolCalls_RegressionWithoutParser documents the exact bug
// this parser fixes, and PROVES it fails without the parser. ExtractTextContent
// is the same helper subagent.go's Execute loop (and oneshot's invoker.go)
// use to build the final answer once the structured ToolCalls slice is
// empty; before this fix that raw text was handed straight to the caller as
// the final answer/verdict. This test shows the "old" pre-fix behavior (the
// raw tagged text reaching what becomes the summary verbatim) and then shows
// ParseTextToolCalls recovering the structured call from that same raw text.
// If ParseTextToolCalls were reverted to a no-op (e.g. `return
// TextToolCallParse{}`), the second half of this test would fail while the
// first half (proving the raw text leaks) would still pass -- i.e. without
// the parser, the tool-call tag text really does leak into what becomes the
// final answer.
func TestParseTextToolCalls_RegressionWithoutParser(t *testing.T) {
	// This is exactly what subagent.go's Execute loop (and oneshot's
	// invoker.go) do today at the `len(choice.Message.ToolCalls) == 0`
	// branch to build the pre-fallback content string.
	oldPathSummary, err := ExtractTextContent(qwenHermesToolCallPayload)
	if err != nil {
		t.Fatalf("unexpected error extracting raw text: %v", err)
	}
	if !strings.Contains(oldPathSummary, "<tool_call>") {
		t.Fatalf("sanity check failed: expected the old code path to still contain the raw tag, got %q", oldPathSummary)
	}

	// New behavior: the fallback parser recognizes and extracts the call
	// instead of letting that raw text become the final answer.
	parsed := ParseTextToolCalls(oldPathSummary)
	if !parsed.Detected || len(parsed.Calls) != 1 {
		t.Fatalf("parser failed to recover the tool call from the raw text that would otherwise become the final summary: Detected=%v Calls=%d", parsed.Detected, len(parsed.Calls))
	}
	if parsed.Calls[0].Function.Name != "get_weather" {
		t.Fatalf("unexpected function name recovered: %q", parsed.Calls[0].Function.Name)
	}
}
