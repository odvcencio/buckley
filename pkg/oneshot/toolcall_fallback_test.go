package oneshot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/draco/buckley/pkg/model"
	"github.com/draco/buckley/pkg/tools"
)

// These fixtures mirror real payloads observed from reasoning models
// (GLM-4.6, Qwen agentic checkpoints) routed through OpenRouter/vLLM, where
// the tool call arrives as text inside the assistant message content instead
// of the OpenAI-standard structured `tool_calls` field. Without
// parseTextToolCalls/resolveTextToolCalls, Invoke and InvokeWithTools treat
// this raw text as the final answer.
const (
	qwenHermesToolCallPayload = "<tool_call>\n" +
		`{"name": "get_weather", "arguments": {"location": "San Francisco", "unit": "celsius"}}` +
		"\n</tool_call>"

	glmNativeArgTagPayload = "<tool_call>read_file\n" +
		"<arg_key>path</arg_key>\n" +
		"<arg_value>pkg/oneshot/invoker.go</arg_value>\n" +
		"</tool_call>"

	glmJSONFencePayload = "I'll write the file now.\n\n" +
		"```json\n" +
		`{"name": "write_file", "arguments": {"path": "notes.md", "content": "hello world"}}` +
		"\n```\n"
)

func TestParseTextToolCalls_QwenHermesJSONTag(t *testing.T) {
	got := parseTextToolCalls(qwenHermesToolCallPayload)

	if !got.Detected || len(got.Calls) != 1 {
		t.Fatalf("expected 1 detected call, got Detected=%v Calls=%d Reason=%q", got.Detected, len(got.Calls), got.Reason)
	}
	call := got.Calls[0]
	if call.Function.Name != "get_weather" {
		t.Errorf("Function.Name = %q, want %q", call.Function.Name, "get_weather")
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
	got := parseTextToolCalls(glmNativeArgTagPayload)

	if !got.Detected || len(got.Calls) != 1 {
		t.Fatalf("expected 1 detected call, got Detected=%v Calls=%d Reason=%q", got.Detected, len(got.Calls), got.Reason)
	}
	call := got.Calls[0]
	if call.Function.Name != "read_file" {
		t.Errorf("Function.Name = %q, want %q", call.Function.Name, "read_file")
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		t.Fatalf("arguments not valid JSON: %v (arguments=%q)", err, call.Function.Arguments)
	}
	if args["path"] != "pkg/oneshot/invoker.go" {
		t.Errorf("unexpected arguments: %+v", args)
	}
}

func TestParseTextToolCalls_JSONFencedPayload(t *testing.T) {
	got := parseTextToolCalls(glmJSONFencePayload)

	if !got.Detected || len(got.Calls) != 1 {
		t.Fatalf("expected 1 detected call, got Detected=%v Calls=%d Reason=%q", got.Detected, len(got.Calls), got.Reason)
	}
	call := got.Calls[0]
	if call.Function.Name != "write_file" {
		t.Errorf("Function.Name = %q, want %q", call.Function.Name, "write_file")
	}
}

func TestParseTextToolCalls_DetectedButUnparsable(t *testing.T) {
	content := `<tool_call>{"name": "foo", "arguments": {not valid json}}</tool_call>`

	got := parseTextToolCalls(content)
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

func TestParseTextToolCalls_NoFalsePositiveOnPlainText(t *testing.T) {
	tests := []string{
		"",
		"Here is my analysis of the code. Everything looks correct.",
		"Example:\n```json\n{\"foo\": \"bar\"}\n```\nThat's not a tool call.",
	}
	for _, content := range tests {
		got := parseTextToolCalls(content)
		if got.Detected {
			t.Errorf("expected Detected=false for %q, got true (Reason=%q)", content, got.Reason)
		}
	}
}

// sequencedMockClient implements ModelClient, returning canned responses in
// order and counting how many times ChatCompletion was called. It
// deliberately does NOT implement streamingModelClient/reasoningAwareModelClient
// so chatCompletion() exercises the same blocking fallback path as before
// this change -- these tests are about the tool-call-parsing wiring, not
// streaming.
type sequencedMockClient struct {
	responses []*model.ChatResponse
	calls     int
}

func (m *sequencedMockClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	idx := m.calls
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}
	m.calls++
	return m.responses[idx], nil
}

func toolCallTextResponse(text string) *model.ChatResponse {
	return &model.ChatResponse{
		Choices: []model.Choice{{
			Message: model.Message{Role: "assistant", Content: text},
		}},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20},
	}
}

func testTool() tools.Definition {
	return tools.Definition{
		Name:        "test_tool",
		Description: "A test tool",
		Parameters:  tools.ObjectSchema(map[string]tools.Property{}, ""),
	}
}

// TestInvoke_FallbackParsesTextToolCall proves the fallback parser is wired
// into Invoke end-to-end: a mock client returns a ChatResponse with EMPTY
// structured ToolCalls but a GLM/Qwen-style <tool_call> payload as Content,
// and Invoke must recover a dispatchable tools.ToolCall from it instead of
// treating the raw text as the answer.
func TestInvoke_FallbackParsesTextToolCall(t *testing.T) {
	raw := "<tool_call>\n" + `{"name": "test_tool", "arguments": {"value": 42}}` + "\n</tool_call>"
	client := &sequencedMockClient{responses: []*model.ChatResponse{toolCallTextResponse(raw)}}

	invoker := NewInvoker(InvokerConfig{Client: client, Model: "glm-4.6"})

	result, _, err := invoker.Invoke(context.Background(), "system", "user", testTool(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasToolCall() {
		t.Fatalf("expected a tool call recovered from text payload, got TextContent=%q", result.TextContent)
	}
	if result.ToolCall.Name != "test_tool" {
		t.Errorf("Name = %q, want %q", result.ToolCall.Name, "test_tool")
	}

	var args struct {
		Value int `json:"value"`
	}
	if err := result.ToolCall.Unmarshal(&args); err != nil {
		t.Fatalf("failed to unmarshal recovered arguments: %v", err)
	}
	if args.Value != 42 {
		t.Errorf("Value = %d, want 42", args.Value)
	}
	if client.calls != 1 {
		t.Errorf("expected exactly 1 model call (well-formed payload needs no retry), got %d", client.calls)
	}
}

// TestInvoke_UnparsableTextToolCall_RetriesOnceThenErrors proves that when
// the text looks like a tool call but can't be parsed, Invoke never lets the
// raw payload leak out as a "text response": it retries once with a
// corrective nudge and, if still unparsable, returns a clear error.
func TestInvoke_UnparsableTextToolCall_RetriesOnceThenErrors(t *testing.T) {
	malformed := `<tool_call>{"name": "test_tool", "arguments": {not valid json}}</tool_call>`
	client := &sequencedMockClient{responses: []*model.ChatResponse{
		toolCallTextResponse(malformed),
		toolCallTextResponse(malformed),
	}}

	invoker := NewInvoker(InvokerConfig{Client: client, Model: "glm-4.6"})

	result, trace, err := invoker.Invoke(context.Background(), "system", "user", testTool(), nil)
	if err == nil {
		t.Fatalf("expected an error after exhausting the single retry, got result=%+v", result)
	}
	if result != nil {
		t.Errorf("expected nil result on final failure, got %+v", result)
	}
	if trace == nil {
		t.Fatalf("expected a trace even on failure")
	}
	if client.calls != 2 {
		t.Errorf("expected exactly 2 model calls (1 initial + 1 corrective retry), got %d", client.calls)
	}
	if !strings.Contains(err.Error(), "could not be parsed") {
		t.Errorf("expected a clear 'could not be parsed' error, got: %v", err)
	}
}

// TestInvokeWithTools_FallbackParsesTextToolCall proves the same wiring for
// the multi-turn InvokeWithTools loop used by pkg/oneshot/review/pr.go's
// legacy fallback path.
func TestInvokeWithTools_FallbackParsesTextToolCall(t *testing.T) {
	raw := "<tool_call>\n" + `{"name": "test_tool", "arguments": {"value": 7}}` + "\n</tool_call>"
	finalResp := &model.ChatResponse{
		Choices: []model.Choice{{
			Message: model.Message{Role: "assistant", Content: "Done: value was 7."},
		}},
	}
	client := &sequencedMockClient{responses: []*model.ChatResponse{
		toolCallTextResponse(raw),
		finalResp,
	}}

	invoker := NewInvoker(InvokerConfig{Client: client, Model: "glm-4.6"})

	executor := &fakeToolExecutor{result: "ok"}
	content, _, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{testTool()}, executor, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "Done: value was 7." {
		t.Errorf("content = %q, want %q", content, "Done: value was 7.")
	}
	if executor.calls != 1 {
		t.Fatalf("expected the recovered tool call to be dispatched exactly once, got %d", executor.calls)
	}
	if executor.lastName != "test_tool" {
		t.Errorf("executor lastName = %q, want %q", executor.lastName, "test_tool")
	}
}

type fakeToolExecutor struct {
	result   string
	calls    int
	lastName string
}

func (f *fakeToolExecutor) Execute(name string, args json.RawMessage) (string, error) {
	f.calls++
	f.lastName = name
	return f.result, nil
}
