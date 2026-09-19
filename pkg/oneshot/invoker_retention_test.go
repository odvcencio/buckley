package oneshot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

type retentionInvokerResponse struct {
	resp *model.ChatResponse
	err  error
}

type retentionInvokerClient struct {
	responses []retentionInvokerResponse
	calls     int
}

func (c *retentionInvokerClient) ChatCompletion(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
	if c.calls >= len(c.responses) {
		return nil, errors.New("unexpected model call")
	}
	next := c.responses[c.calls]
	c.calls++
	return next.resp, next.err
}

func TestInvokeWithRetryAggregatesNoToolThenToolCallTrace(t *testing.T) {
	firstIdentity := &model.ExecutionIdentity{RequestedModel: "gpt-request", SelectedModel: "gpt-selected", ProviderID: "openai", ResponseModel: "gpt-first", ResponseID: "resp-first"}
	secondIdentity := &model.ExecutionIdentity{RequestedModel: "gpt-request", SelectedModel: "gpt-selected", ProviderID: "openai", ResponseModel: "gpt-second", ResponseID: "resp-second", Conflicted: true}
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{
		{resp: retentionChatResponse("resp-first", firstIdentity, model.Message{Role: "assistant", Content: "plain text instead of tool", Reasoning: "PRIVATE_ONESHOT_REASONING_1"}, "stop", 10, 1)},
		{resp: retentionChatResponse("resp-second", secondIdentity, model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{
			ID: "call_2", Type: "function", Function: model.FunctionCall{Name: "retain_tool", Arguments: `{"ok":true}`},
		}}}, "tool_calls", 20, 2)},
	}}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "gpt-request", Provider: "openai"})

	result, trace, err := invoker.InvokeWithRetry(context.Background(), "system", "user", retentionToolDef(), transparency.NewContextAudit())
	if err != nil {
		t.Fatalf("InvokeWithRetry: %v", err)
	}
	if result == nil || result.ToolCall == nil || result.ToolCall.Name != "retain_tool" {
		t.Fatalf("result = %#v, want final tool call", result)
	}
	if result.Trace != trace {
		t.Fatal("result.Trace does not point at returned aggregate trace")
	}
	if trace == nil || len(trace.Attempts) != 2 {
		t.Fatalf("trace attempts = %#v, want two attempts", trace)
	}
	if got := trace.Attempts[0].ValidationError; !strings.Contains(got, "model did not call") {
		t.Fatalf("first validation error = %q, want missing-tool rejection", got)
	}
	if trace.Tokens.Input != 30 || trace.Tokens.Output != 3 {
		t.Fatalf("aggregate tokens = %+v, want 30/3 exactly once", trace.Tokens)
	}
	if got := trace.ModelExecutions; len(got) != 2 || got[0].ResponseID != "resp-first" || got[1].ResponseID != "resp-second" || !got[1].Conflicted {
		t.Fatalf("aggregate identities = %+v, want first then conflicted second", got)
	}
	firstIdentity.ResponseID = "mutated"
	if trace.Attempts[0].Trace.ModelExecutions[0].ResponseID != "resp-first" || trace.ModelExecutions[0].ResponseID != "resp-first" {
		t.Fatalf("trace identities aliased source identity: attempts=%+v aggregate=%+v", trace.Attempts[0].Trace.ModelExecutions, trace.ModelExecutions)
	}
	if strings.Contains(result.TextContent, "PRIVATE_ONESHOT_REASONING") || strings.Contains(trace.Content, "PRIVATE_ONESHOT_REASONING") {
		t.Fatalf("private reasoning leaked into public text: result=%q trace=%q", result.TextContent, trace.Content)
	}
}

func TestInvokeWithRetryRetainsPartialResultWhenRetryFails(t *testing.T) {
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{
		{resp: retentionChatResponse("resp-first", nil, model.Message{Role: "assistant", Content: "plain text"}, "stop", 10, 1)},
		{resp: retentionChatResponse("resp-partial", &model.ExecutionIdentity{ResponseID: "resp-partial"}, model.Message{Role: "assistant", Content: "public partial", Reasoning: "PRIVATE_ONESHOT_REASONING_2"}, "stop", 7, 3), err: errors.New("transport dropped after body")},
	}}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "gpt-request", Provider: "openai"})

	result, trace, err := invoker.InvokeWithRetry(context.Background(), "system", "user", retentionToolDef(), nil)
	if err == nil || !strings.Contains(err.Error(), "partial response") {
		t.Fatalf("InvokeWithRetry error = %v, want partial-response error", err)
	}
	if result == nil || result.TextContent != "public partial" {
		t.Fatalf("result = %#v, want retained public partial", result)
	}
	if result.Trace != trace {
		t.Fatal("result.Trace does not retain aggregate trace")
	}
	if trace == nil || trace.Error == "" || len(trace.Attempts) != 2 {
		t.Fatalf("trace = %#v, want aggregate failed trace", trace)
	}
	if trace.Tokens.Input != 17 || trace.Tokens.Output != 4 {
		t.Fatalf("aggregate tokens = %+v, want first+partial exactly once", trace.Tokens)
	}
	if got := trace.ModelExecutions; len(got) != 1 || got[0].ResponseID != "resp-partial" {
		t.Fatalf("identities = %+v, want absent first identity skipped and partial retained", got)
	}
	if strings.Contains(result.TextContent, "PRIVATE_ONESHOT_REASONING") || strings.Contains(trace.Content, "PRIVATE_ONESHOT_REASONING") {
		t.Fatalf("private reasoning leaked into public text: result=%q trace=%q", result.TextContent, trace.Content)
	}
}

func TestInvokerMethodsAttachIdentityToTrace(t *testing.T) {
	identity := &model.ExecutionIdentity{RequestedModel: "req", SelectedModel: "sel", ProviderID: "prov", ResponseModel: "wire", ResponseID: "resp-text"}
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{
		{resp: retentionChatResponse("resp-text", identity, model.Message{Role: "assistant", Content: "hello"}, "stop", 3, 4)},
	}}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "req", Provider: "prov"})

	content, trace, err := invoker.InvokeText(context.Background(), "system", "user", nil)
	if err != nil || content != "hello" {
		t.Fatalf("InvokeText = %q, %v", content, err)
	}
	if got := trace.ModelExecutions; len(got) != 1 || got[0].ResponseID != "resp-text" || got[0].SelectedModel != "sel" {
		t.Fatalf("InvokeText trace identities = %+v, want response identity", got)
	}
	identity.ResponseID = "mutated"
	if trace.ModelExecutions[0].ResponseID != "resp-text" {
		t.Fatalf("InvokeText trace identity aliased source: %+v", trace.ModelExecutions)
	}
}

func TestInvokePartialErrorAttachesIdentityToTrace(t *testing.T) {
	identity := &model.ExecutionIdentity{RequestedModel: "req", SelectedModel: "sel", ProviderID: "prov", ResponseModel: "wire", ResponseID: "resp-partial", Conflicted: true}
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{
		{resp: retentionChatResponse("resp-partial", identity, model.Message{Role: "assistant", Content: "public partial", Reasoning: "PRIVATE_INVOKE_REASONING"}, "stop", 4, 5), err: errors.New("transport failed after body")},
	}}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "req", Provider: "prov"})

	result, trace, err := invoker.Invoke(context.Background(), "system", "user", retentionToolDef(), nil)
	if err == nil || !strings.Contains(err.Error(), "partial response") {
		t.Fatalf("Invoke error = %v, want partial-response failure", err)
	}
	if result == nil || result.TextContent != "public partial" || result.Trace != trace {
		t.Fatalf("result = %#v trace=%#v, want retained public partial and trace", result, trace)
	}
	if trace == nil || trace.Error == "" || trace.Tokens.Input != 4 || trace.Tokens.Output != 5 {
		t.Fatalf("trace = %#v, want failed trace with usage", trace)
	}
	if got := trace.ModelExecutions; len(got) != 1 || got[0].ResponseID != "resp-partial" || !got[0].Conflicted {
		t.Fatalf("Invoke identities = %+v, want partial response identity", got)
	}
	identity.ResponseID = "mutated"
	if trace.ModelExecutions[0].ResponseID != "resp-partial" {
		t.Fatalf("Invoke identity aliased source identity: %+v", trace.ModelExecutions)
	}
	if strings.Contains(result.TextContent, "PRIVATE_INVOKE_REASONING") || strings.Contains(trace.Content, "PRIVATE_INVOKE_REASONING") {
		t.Fatalf("private reasoning leaked into public text: result=%q trace=%q", result.TextContent, trace.Content)
	}
}

func TestInvokeTextPartialErrorAttachesIdentityToTrace(t *testing.T) {
	identity := &model.ExecutionIdentity{RequestedModel: "req", SelectedModel: "sel", ProviderID: "prov", ResponseModel: "wire", ResponseID: "resp-text-partial"}
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{
		{resp: retentionChatResponse("resp-text-partial", identity, model.Message{Role: "assistant", Content: "public text partial", Reasoning: "PRIVATE_TEXT_REASONING"}, "stop", 6, 7), err: errors.New("text transport failed")},
	}}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "req", Provider: "prov"})

	content, trace, err := invoker.InvokeText(context.Background(), "system", "user", nil)
	if err == nil || !strings.Contains(err.Error(), "partial response") {
		t.Fatalf("InvokeText error = %v, want partial-response failure", err)
	}
	if content != "public text partial" {
		t.Fatalf("content = %q, want retained public partial", content)
	}
	if trace == nil || trace.Error == "" || trace.Tokens.Input != 6 || trace.Tokens.Output != 7 {
		t.Fatalf("trace = %#v, want failed trace with usage", trace)
	}
	if got := trace.ModelExecutions; len(got) != 1 || got[0].ResponseID != "resp-text-partial" {
		t.Fatalf("InvokeText identities = %+v, want partial response identity", got)
	}
	identity.ResponseID = "mutated"
	if trace.ModelExecutions[0].ResponseID != "resp-text-partial" {
		t.Fatalf("InvokeText identity aliased source identity: %+v", trace.ModelExecutions)
	}
	if strings.Contains(content, "PRIVATE_TEXT_REASONING") || strings.Contains(trace.Content, "PRIVATE_TEXT_REASONING") {
		t.Fatalf("private reasoning leaked into public text: content=%q trace=%q", content, trace.Content)
	}
}

func TestInvokeStreamAttachesIdentityToTrace(t *testing.T) {
	finish := "stop"
	identity := &model.ExecutionIdentity{RequestedModel: "req", SelectedModel: "sel", ProviderID: "prov", ResponseModel: "stream-wire", ResponseID: "resp-stream"}
	streamClient := retentionStreamClient{
		chunks: []model.StreamChunk{{
			ID:                "resp-stream",
			Model:             "stream-wire",
			ExecutionIdentity: identity,
			Choices: []model.StreamChoice{{
				Delta:        model.MessageDelta{Content: "streamed public text", Reasoning: "PRIVATE_STREAM_REASONING"},
				FinishReason: &finish,
			}},
			Usage: &model.Usage{PromptTokens: 5, CompletionTokens: 6, TotalTokens: 11},
		}},
	}
	invoker := NewInvoker(InvokerConfig{Client: streamClient, Model: "req", Provider: "prov"})

	result, trace, err := invoker.InvokeStream(context.Background(), "system", "user", retentionToolDef(), nil, nil)
	if err != nil {
		t.Fatalf("InvokeStream: %v", err)
	}
	if result == nil || result.TextContent != "streamed public text" || result.Trace != trace {
		t.Fatalf("result = %#v trace=%#v, want retained streamed result trace", result, trace)
	}
	if got := trace.ModelExecutions; len(got) != 1 || got[0].ResponseID != "resp-stream" || got[0].ResponseModel != "stream-wire" {
		t.Fatalf("stream identities = %+v, want accumulated stream identity", got)
	}
	identity.ResponseID = "mutated"
	if trace.ModelExecutions[0].ResponseID != "resp-stream" {
		t.Fatalf("stream identity aliased source identity: %+v", trace.ModelExecutions)
	}
	if strings.Contains(result.TextContent, "PRIVATE_STREAM_REASONING") || strings.Contains(trace.Content, "PRIVATE_STREAM_REASONING") {
		t.Fatalf("private stream reasoning leaked into public text: result=%q trace=%q", result.TextContent, trace.Content)
	}
}

func TestInvokeStreamTerminalErrorAttachesIdentityToPartialTrace(t *testing.T) {
	identity := &model.ExecutionIdentity{RequestedModel: "req", SelectedModel: "sel", ProviderID: "prov", ResponseModel: "stream-wire", ResponseID: "resp-stream-error"}
	streamClient := retentionStreamClient{
		chunks: []model.StreamChunk{{
			ID:                "resp-stream-error",
			Model:             "stream-wire",
			ExecutionIdentity: identity,
			Choices: []model.StreamChoice{{
				Delta: model.MessageDelta{Content: "stream partial", Reasoning: "PRIVATE_STREAM_ERROR_REASONING"},
			}},
			Usage: &model.Usage{PromptTokens: 8, CompletionTokens: 9, TotalTokens: 17},
		}},
		err: errors.New("stream transport failed"),
	}
	invoker := NewInvoker(InvokerConfig{Client: streamClient, Model: "req", Provider: "prov"})

	result, trace, err := invoker.InvokeStream(context.Background(), "system", "user", retentionToolDef(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "partial response") {
		t.Fatalf("InvokeStream error = %v, want partial-response failure", err)
	}
	if result == nil || result.TextContent != "stream partial" || result.Trace != trace {
		t.Fatalf("result = %#v trace=%#v, want retained stream partial", result, trace)
	}
	if trace == nil || trace.Error == "" || trace.Tokens.Input != 8 || trace.Tokens.Output != 9 {
		t.Fatalf("trace = %#v, want failed stream trace with usage", trace)
	}
	if got := trace.ModelExecutions; len(got) != 1 || got[0].ResponseID != "resp-stream-error" {
		t.Fatalf("stream identities = %+v, want terminal-error identity", got)
	}
	identity.ResponseID = "mutated"
	if trace.ModelExecutions[0].ResponseID != "resp-stream-error" {
		t.Fatalf("stream identity aliased source identity: %+v", trace.ModelExecutions)
	}
	if strings.Contains(result.TextContent, "PRIVATE_STREAM_ERROR_REASONING") || strings.Contains(trace.Content, "PRIVATE_STREAM_ERROR_REASONING") {
		t.Fatalf("private stream reasoning leaked into public text: result=%q trace=%q", result.TextContent, trace.Content)
	}
}

func TestInvokeStreamCancelAfterChunkAttachesIdentityToPartialTrace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	identity := &model.ExecutionIdentity{RequestedModel: "req", SelectedModel: "sel", ProviderID: "prov", ResponseModel: "stream-wire", ResponseID: "resp-stream-cancel"}
	streamClient := retentionBlockingStreamClient{chunk: model.StreamChunk{
		ID:                "resp-stream-cancel",
		Model:             "stream-wire",
		ExecutionIdentity: identity,
		Choices: []model.StreamChoice{{
			Delta: model.MessageDelta{Content: "cancel partial"},
		}},
		Usage: &model.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
	}}
	invoker := NewInvoker(InvokerConfig{Client: streamClient, Model: "req", Provider: "prov"})

	result, trace, err := invoker.InvokeStream(ctx, "system", "user", retentionToolDef(), nil, func(_, _ string) {
		cancel()
	})
	if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("InvokeStream error = %v, want context cancellation", err)
	}
	if result == nil || result.TextContent != "cancel partial" || result.Trace != trace {
		t.Fatalf("result = %#v trace=%#v, want retained cancel partial", result, trace)
	}
	if trace == nil || trace.Error == "" || trace.Tokens.Input != 2 || trace.Tokens.Output != 3 {
		t.Fatalf("trace = %#v, want canceled trace with usage", trace)
	}
	if got := trace.ModelExecutions; len(got) != 1 || got[0].ResponseID != "resp-stream-cancel" {
		t.Fatalf("stream cancel identities = %+v, want accumulated identity", got)
	}
	identity.ResponseID = "mutated"
	if trace.ModelExecutions[0].ResponseID != "resp-stream-cancel" {
		t.Fatalf("stream cancel identity aliased source identity: %+v", trace.ModelExecutions)
	}
}

func TestInvokeWithToolsAttachesControllerIdentitiesToTrace(t *testing.T) {
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{
		{resp: retentionChatResponse("resp-tool", &model.ExecutionIdentity{ResponseID: "resp-tool"}, model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{
			ID: "call_read", Type: "function", Function: model.FunctionCall{Name: "retain_tool", Arguments: `{"ok":true}`},
		}}}, "tool_calls", 10, 1)},
		{resp: retentionChatResponse("resp-final", &model.ExecutionIdentity{ResponseID: "resp-final"}, model.Message{Role: "assistant", Content: "tool-backed final"}, "stop", 20, 2)},
	}}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "req", Provider: "prov"})

	content, trace, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{retentionToolDef()}, retentionExecutor{}, 3)
	if err != nil {
		t.Fatalf("InvokeWithTools: %v", err)
	}
	if content != "tool-backed final" {
		t.Fatalf("content = %q, want final text", content)
	}
	if trace == nil || trace.Tokens.Input != 30 || trace.Tokens.Output != 3 {
		t.Fatalf("trace = %#v, want retained controller usage", trace)
	}
	if got := trace.ModelExecutions; len(got) != 2 || got[0].ResponseID != "resp-tool" || got[1].ResponseID != "resp-final" {
		t.Fatalf("controller identities = %+v, want both model rounds", got)
	}
}

func TestInvokeWithToolsIncompleteRetainsToolUsageAndIdentities(t *testing.T) {
	client := &retentionInvokerClient{responses: []retentionInvokerResponse{
		{resp: retentionChatResponse("resp-tool", &model.ExecutionIdentity{RequestedModel: "req", SelectedModel: "sel", ProviderID: "prov", ResponseModel: "wire-tool", ResponseID: "resp-tool"}, model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{
			ID: "call_read", Type: "function", Function: model.FunctionCall{Name: "retain_tool", Arguments: `{"ok":true}`},
		}}}, "tool_calls", 10, 1)},
		{resp: retentionChatResponse("resp-partial-final", &model.ExecutionIdentity{RequestedModel: "req", SelectedModel: "sel", ProviderID: "prov", ResponseModel: "wire-final", ResponseID: "resp-partial-final"}, model.Message{Role: "assistant", Content: "partial final from evidence", Reasoning: "PRIVATE_TOOL_FINAL_REASONING"}, "stop", 20, 2), err: errors.New("finalization transport failed")},
	}}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "req", Provider: "prov"})

	content, trace, err := invoker.InvokeWithTools(context.Background(), "system", "user", []tools.Definition{retentionToolDef()}, retentionExecutor{}, 1)
	if err == nil || !strings.Contains(err.Error(), "model request failed") {
		t.Fatalf("InvokeWithTools error = %v, want incomplete controller failure", err)
	}
	if content != "partial final from evidence" {
		t.Fatalf("content = %q, want retained public partial final", content)
	}
	if trace == nil || trace.Error == "" || trace.Tokens.Input != 30 || trace.Tokens.Output != 3 {
		t.Fatalf("trace = %#v, want retained controller usage and error", trace)
	}
	if got := trace.ToolCalls; len(got) != 1 || got[0].Name != "retain_tool" {
		t.Fatalf("tool calls = %+v, want completed tool evidence retained", got)
	}
	if got := trace.ModelExecutions; len(got) != 2 || got[0].ResponseID != "resp-tool" || got[1].ResponseID != "resp-partial-final" {
		t.Fatalf("controller identities = %+v, want tool round and failed finalization", got)
	}
	if strings.Contains(content, "PRIVATE_TOOL_FINAL_REASONING") || strings.Contains(trace.Content, "PRIVATE_TOOL_FINAL_REASONING") {
		t.Fatalf("private reasoning leaked into public text: content=%q trace=%q", content, trace.Content)
	}
}

func retentionChatResponse(id string, identity *model.ExecutionIdentity, msg model.Message, finish string, input, output int) *model.ChatResponse {
	return &model.ChatResponse{
		ID: id, Model: "wire-model",
		Choices: []model.Choice{{Message: msg, FinishReason: finish}},
		Usage: model.Usage{
			PromptTokens: input, CompletionTokens: output, TotalTokens: input + output,
		},
		UsagePresent:      true,
		ExecutionIdentity: identity,
	}
}

func retentionToolDef() tools.Definition {
	return tools.Definition{
		Name:        "retain_tool",
		Description: "retain",
		Parameters:  tools.ObjectSchema(map[string]tools.Property{"ok": {Type: "boolean"}}, ""),
	}
}

type retentionStreamClient struct {
	chunks []model.StreamChunk
	err    error
}

func (c retentionStreamClient) ChatCompletion(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
	return nil, errors.New("unexpected non-streaming request")
}

func (c retentionStreamClient) ChatCompletionStream(context.Context, model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	chunks := make(chan model.StreamChunk, len(c.chunks))
	errs := make(chan error, 1)
	for _, chunk := range c.chunks {
		chunks <- chunk
	}
	close(chunks)
	if c.err != nil {
		errs <- c.err
	}
	close(errs)
	return chunks, errs
}

type retentionBlockingStreamClient struct {
	chunk model.StreamChunk
}

func (c retentionBlockingStreamClient) ChatCompletion(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
	return nil, errors.New("unexpected non-streaming request")
}

func (c retentionBlockingStreamClient) ChatCompletionStream(context.Context, model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	chunks := make(chan model.StreamChunk, 1)
	errs := make(chan error)
	chunks <- c.chunk
	return chunks, errs
}

type retentionExecutor struct{}

func (retentionExecutor) Execute(string, json.RawMessage) (string, error) {
	return "tool output", nil
}
