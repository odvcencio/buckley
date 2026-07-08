package oneshot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tools"
)

// These fixtures mirror real payloads observed from reasoning models
// (GLM-5.2, Qwen agentic checkpoints) routed through OpenRouter/vLLM, where
// the tool call arrives as text inside the assistant message content
// instead of the OpenAI-standard structured `tool_calls` field. Without
// resolveTextToolCalls, Invoke/InvokeStream/InvokeWithTools treat this raw
// text as the final answer -- which is the exact bug behind `buckley pr`
// producing a "no tool call" / unmarshal failure instead of a generated PR.
const (
	fallbackQwenHermesToolCallPayload = "<tool_call>\n" +
		`{"name": "get_weather", "arguments": {"location": "San Francisco", "unit": "celsius"}}` +
		"\n</tool_call>"

	fallbackGLMNativeArgTagPayload = "<tool_call>read_file\n" +
		"<arg_key>path</arg_key>\n" +
		"<arg_value>pkg/oneshot/invoker.go</arg_value>\n" +
		"</tool_call>"

	fallbackGLMJSONFencePayload = "I'll write the file now.\n\n" +
		"```json\n" +
		`{"name": "write_file", "arguments": {"path": "notes.md", "content": "hello world"}}` +
		"\n```\n"

	// fallbackGLMNumericQuirkPayload reproduces the exact live bug: GLM-5.2
	// tool-call argument JSON with a stray space injected right after a
	// leading '-' in a numeric literal -- the one encoding/json scanner
	// state that emits the literal reported error text "invalid character
	// ' ' in numeric literal" (see pkg/jsonrepair for the full analysis).
	fallbackGLMNumericQuirkPayload = "<tool_call>\n" +
		`{"name": "generate_pull_request", "arguments": {"title": "fix bug", "confidence": - 5}}` +
		"\n</tool_call>"
)

func fallbackTestTool() tools.Definition {
	return tools.Definition{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters:  tools.ObjectSchema(map[string]tools.Property{}, ""),
	}
}

func fallbackToolCallTextResponse(text string) *model.ChatResponse {
	return &model.ChatResponse{
		Choices: []model.Choice{{
			Message: model.Message{Role: "assistant", Content: text},
		}},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20},
	}
}

// -----------------------------------------------------------------------
// resolveTextToolCalls: pure decision-function tests.
// -----------------------------------------------------------------------

func TestResolveTextToolCalls_StructuredCallRepaired(t *testing.T) {
	// A structured tool call the API returned directly (never touched the
	// text-fallback parser) with GLM's numeric-literal-spacing quirk baked
	// into its Arguments. resolveTextToolCalls must repair this via
	// repairStructuredToolCallArguments so downstream Unmarshal succeeds.
	msg := model.Message{
		Role: "assistant",
		ToolCalls: []model.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: model.FunctionCall{Name: "generate_pull_request", Arguments: `{"confidence": - 5}`},
		}},
	}

	calls, unparsable, reason := resolveTextToolCalls(msg)
	if unparsable {
		t.Fatalf("expected unparsable=false, reason=%q", reason)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	var args struct {
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("structured call arguments not valid JSON after repair: %v (arguments=%q)", err, calls[0].Function.Arguments)
	}
	if args.Confidence != -5 {
		t.Errorf("Confidence = %v, want -5", args.Confidence)
	}
}

func TestResolveTextToolCalls_HermesTag(t *testing.T) {
	calls, unparsable, reason := resolveTextToolCalls(model.Message{Role: "assistant", Content: fallbackQwenHermesToolCallPayload})
	if unparsable {
		t.Fatalf("expected unparsable=false, reason=%q", reason)
	}
	if len(calls) != 1 || calls[0].Function.Name != "get_weather" {
		t.Fatalf("expected 1 recovered get_weather call, got %+v", calls)
	}
}

func TestResolveTextToolCalls_GLMNativeTag(t *testing.T) {
	calls, unparsable, reason := resolveTextToolCalls(model.Message{Role: "assistant", Content: fallbackGLMNativeArgTagPayload})
	if unparsable {
		t.Fatalf("expected unparsable=false, reason=%q", reason)
	}
	if len(calls) != 1 || calls[0].Function.Name != "read_file" {
		t.Fatalf("expected 1 recovered read_file call, got %+v", calls)
	}
}

func TestResolveTextToolCalls_JSONFence(t *testing.T) {
	calls, unparsable, reason := resolveTextToolCalls(model.Message{Role: "assistant", Content: fallbackGLMJSONFencePayload})
	if unparsable {
		t.Fatalf("expected unparsable=false, reason=%q", reason)
	}
	if len(calls) != 1 || calls[0].Function.Name != "write_file" {
		t.Fatalf("expected 1 recovered write_file call, got %+v", calls)
	}
}

func TestResolveTextToolCalls_NumericLiteralQuirk(t *testing.T) {
	calls, unparsable, reason := resolveTextToolCalls(model.Message{Role: "assistant", Content: fallbackGLMNumericQuirkPayload})
	if unparsable {
		t.Fatalf("expected unparsable=false, reason=%q", reason)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 recovered call, got %d", len(calls))
	}
	var args struct {
		Title      string  `json:"title"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("recovered arguments are not valid JSON after repair: %v (arguments=%q)", err, calls[0].Function.Arguments)
	}
	if args.Confidence != -5 {
		t.Errorf("Confidence = %v, want -5", args.Confidence)
	}
}

func TestResolveTextToolCalls_DetectedButUnparsable(t *testing.T) {
	msg := model.Message{Content: `<tool_call>{"name": "foo", "arguments": {not valid json}}</tool_call>`}
	calls, unparsable, reason := resolveTextToolCalls(msg)
	if !unparsable {
		t.Fatalf("expected unparsable=true for a tagged-but-malformed payload")
	}
	if len(calls) != 0 {
		t.Fatalf("expected no calls, got %d", len(calls))
	}
	if reason == "" {
		t.Errorf("expected a non-empty reason")
	}
}

func TestResolveTextToolCalls_NoFalsePositiveOnPlainText(t *testing.T) {
	tests := []string{
		"",
		"Here is my analysis of the code. Everything looks correct.",
		"Example:\n```json\n{\"foo\": \"bar\"}\n```\nThat's not a tool call.",
	}
	for _, content := range tests {
		calls, unparsable, reason := resolveTextToolCalls(model.Message{Content: content})
		if unparsable {
			t.Errorf("expected unparsable=false for %q, got true (reason=%q)", content, reason)
		}
		if len(calls) != 0 {
			t.Errorf("expected no calls for %q, got %d", content, len(calls))
		}
	}
}

// -----------------------------------------------------------------------
// Invoke: end-to-end wiring tests.
// -----------------------------------------------------------------------

// TestInvoke_FallbackParsesTextToolCall proves the fallback parser is wired
// into Invoke end-to-end: a mock client returns a ChatResponse with EMPTY
// structured ToolCalls but a GLM/Qwen-style <tool_call> payload as Content,
// and Invoke must recover a dispatchable tools.ToolCall from it instead of
// treating the raw text as the answer.
func TestInvoke_FallbackParsesTextToolCall(t *testing.T) {
	client := &multiResponseClient{responses: []*model.ChatResponse{fallbackToolCallTextResponse(fallbackQwenHermesToolCallPayload)}}

	invoker := NewInvoker(InvokerConfig{Client: client, Model: "z-ai/glm-5.2"})

	result, _, err := invoker.Invoke(context.Background(), "system", "user", fallbackTestTool(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasToolCall() {
		t.Fatalf("expected a tool call recovered from text payload, got TextContent=%q", result.TextContent)
	}
	if result.ToolCall.Name != "get_weather" {
		t.Errorf("Name = %q, want %q", result.ToolCall.Name, "get_weather")
	}
	if client.callCount != 1 {
		t.Errorf("expected exactly 1 model call (well-formed payload needs no retry), got %d", client.callCount)
	}
}

// TestInvoke_StructuredToolCallWithNumericQuirk_RepairedEndToEnd proves that
// a *structured* tool call (the API populated Message.ToolCalls directly,
// never touching the text-fallback parser) still gets its Arguments
// repaired end-to-end through Invoke, so callers unmarshaling
// result.ToolCall.Arguments never see GLM's numeric-literal-spacing quirk.
func TestInvoke_StructuredToolCallWithNumericQuirk_RepairedEndToEnd(t *testing.T) {
	client := &multiResponseClient{responses: []*model.ChatResponse{{
		Choices: []model.Choice{{
			Message: model.Message{
				Role: "assistant",
				ToolCalls: []model.ToolCall{{
					ID:       "call_1",
					Type:     "function",
					Function: model.FunctionCall{Name: "test_tool", Arguments: `{"confidence": - 5}`},
				}},
			},
		}},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20},
	}}}

	invoker := NewInvoker(InvokerConfig{Client: client, Model: "z-ai/glm-5.2"})

	result, _, err := invoker.Invoke(context.Background(), "system", "user", fallbackTestTool(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasToolCall() {
		t.Fatalf("expected a tool call")
	}

	// This is exactly what pkg/oneshot/commands.PRDefinition.Validate/
	// Unmarshal and the legacy pkg/oneshot/pr.Runner do with
	// result.ToolCall.Arguments -- it must be valid, unmarshalable JSON.
	var args struct {
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal(result.ToolCall.Arguments, &args); err != nil {
		t.Fatalf("unmarshal tool call: %v (arguments=%q)", err, result.ToolCall.Arguments)
	}
	if args.Confidence != -5 {
		t.Errorf("Confidence = %v, want -5", args.Confidence)
	}
}

// TestInvoke_UnparsableTextToolCall_RetriesOnceThenErrors proves that when
// the text looks like a tool call but can't be parsed, Invoke never lets the
// raw payload leak out as a "text response": it retries once with a
// corrective nudge and, if still unparsable, returns a clear error.
func TestInvoke_UnparsableTextToolCall_RetriesOnceThenErrors(t *testing.T) {
	malformed := `<tool_call>{"name": "test_tool", "arguments": {not valid json}}</tool_call>`
	client := &multiResponseClient{responses: []*model.ChatResponse{
		fallbackToolCallTextResponse(malformed),
		fallbackToolCallTextResponse(malformed),
	}}

	invoker := NewInvoker(InvokerConfig{Client: client, Model: "z-ai/glm-5.2"})

	result, trace, err := invoker.Invoke(context.Background(), "system", "user", fallbackTestTool(), nil)
	if err == nil {
		t.Fatalf("expected an error after exhausting the single retry, got result=%+v", result)
	}
	if result != nil {
		t.Errorf("expected nil result on final failure, got %+v", result)
	}
	if trace == nil {
		t.Fatalf("expected a trace even on failure")
	}
	if client.callCount != 2 {
		t.Errorf("expected exactly 2 model calls (1 initial + 1 corrective retry), got %d", client.callCount)
	}
	if !strings.Contains(err.Error(), "could not be parsed") {
		t.Errorf("expected a clear 'could not be parsed' error, got: %v", err)
	}
}

// -----------------------------------------------------------------------
// InvokeStream: end-to-end wiring tests.
// -----------------------------------------------------------------------

// TestInvokeStream_FallbackParsesTextToolCall proves the same fallback
// wiring applies to the streaming path: acc.FinalizeWithTokenParsing()
// only understands Kimi K2's `<|tool_call...|>` token format, so a
// GLM/Hermes-style `<tool_call>` tag surviving into the finalized message's
// Content must still be recovered by resolveTextToolCalls.
func TestInvokeStream_FallbackParsesTextToolCall(t *testing.T) {
	client := &mockStreamClient{responses: []*model.ChatResponse{
		fallbackToolCallTextResponse(fallbackQwenHermesToolCallPayload),
	}}

	invoker := NewInvoker(InvokerConfig{Client: client, Model: "z-ai/glm-5.2"})

	result, _, err := invoker.InvokeStream(context.Background(), "system", "user", fallbackTestTool(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasToolCall() {
		t.Fatalf("expected a tool call recovered from streamed text payload, got TextContent=%q", result.TextContent)
	}
	if result.ToolCall.Name != "get_weather" {
		t.Errorf("Name = %q, want %q", result.ToolCall.Name, "get_weather")
	}
}

// TestInvokeStream_UnparsableTextToolCall_RetriesOnceThenErrors mirrors
// TestInvoke_UnparsableTextToolCall_RetriesOnceThenErrors for the streaming
// path.
func TestInvokeStream_UnparsableTextToolCall_RetriesOnceThenErrors(t *testing.T) {
	malformed := `<tool_call>{"name": "test_tool", "arguments": {not valid json}}</tool_call>`
	client := &mockStreamClient{responses: []*model.ChatResponse{
		fallbackToolCallTextResponse(malformed),
		fallbackToolCallTextResponse(malformed),
	}}

	invoker := NewInvoker(InvokerConfig{Client: client, Model: "z-ai/glm-5.2"})

	result, trace, err := invoker.InvokeStream(context.Background(), "system", "user", fallbackTestTool(), nil, nil)
	if err == nil {
		t.Fatalf("expected an error after exhausting the single retry, got result=%+v", result)
	}
	if trace == nil {
		t.Fatalf("expected a trace even on failure")
	}
	if client.callCount != 2 {
		t.Errorf("expected exactly 2 model calls (1 initial + 1 corrective retry), got %d", client.callCount)
	}
	if !strings.Contains(err.Error(), "could not be parsed") {
		t.Errorf("expected a clear 'could not be parsed' error, got: %v", err)
	}
}

// -----------------------------------------------------------------------
// InvokeWithTools: end-to-end wiring tests.
// -----------------------------------------------------------------------

// TestInvokeWithTools_FallbackParsesTextToolCall proves the same wiring for
// the multi-turn InvokeWithTools loop used by
// pkg/oneshot/review/pr.go's legacy fallback path.
func TestInvokeWithTools_FallbackParsesTextToolCall(t *testing.T) {
	finalResp := &model.ChatResponse{
		Choices: []model.Choice{{
			Message: model.Message{Role: "assistant", Content: "Done: value was 7."},
		}},
	}
	client := &multiResponseClient{responses: []*model.ChatResponse{
		fallbackToolCallTextResponse("<tool_call>\n" + `{"name": "test_tool", "arguments": {"value": 7}}` + "\n</tool_call>"),
		finalResp,
	}}

	invoker := NewInvoker(InvokerConfig{Client: client, Model: "z-ai/glm-5.2"})

	executor := &mockToolExecutor{results: map[string]string{"test_tool": "ok"}}
	content, _, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{fallbackTestTool()}, executor, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "Done: value was 7." {
		t.Errorf("content = %q, want %q", content, "Done: value was 7.")
	}
	if len(executor.calls) != 1 || executor.calls[0] != "test_tool" {
		t.Fatalf("expected the recovered tool call to be dispatched exactly once, got %v", executor.calls)
	}
}

// TestInvokeWithTools_UnparsableTextToolCall_RetriesOnceThenErrors mirrors
// the single-retry-then-error guard for the InvokeWithTools loop.
func TestInvokeWithTools_UnparsableTextToolCall_RetriesOnceThenErrors(t *testing.T) {
	malformed := `<tool_call>{"name": "test_tool", "arguments": {not valid json}}</tool_call>`
	client := &multiResponseClient{responses: []*model.ChatResponse{
		fallbackToolCallTextResponse(malformed),
		fallbackToolCallTextResponse(malformed),
	}}

	invoker := NewInvoker(InvokerConfig{Client: client, Model: "z-ai/glm-5.2"})

	executor := &mockToolExecutor{}
	content, _, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{fallbackTestTool()}, executor, 5)
	if err == nil {
		t.Fatalf("expected an error after exhausting the single retry, got content=%q", content)
	}
	if len(executor.calls) != 0 {
		t.Errorf("expected no tool dispatches for an unparsable payload, got %v", executor.calls)
	}
	if !strings.Contains(err.Error(), "could not be parsed") {
		t.Errorf("expected a clear 'could not be parsed' error, got: %v", err)
	}
}
