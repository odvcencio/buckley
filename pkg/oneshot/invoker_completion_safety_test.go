package oneshot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tools"
	"m31labs.dev/buckley/pkg/transparency"
)

const invokerPrivateReasoningSentinel = "private-reasoning-sentinel"

type completionSafetyClient struct {
	response *model.ChatResponse
	err      error
}

func (c completionSafetyClient) ChatCompletion(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
	return c.response, c.err
}

type completionSafetyStreamClient struct {
	chunks <-chan model.StreamChunk
	errs   <-chan error
}

func (c completionSafetyStreamClient) ChatCompletion(context.Context, model.ChatRequest) (*model.ChatResponse, error) {
	return nil, errors.New("unexpected non-stream call")
}

func (c completionSafetyStreamClient) ChatCompletionStream(context.Context, model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	return c.chunks, c.errs
}

func completionSafetyTool() tools.Definition {
	return tools.Definition{
		Name:        "test_tool",
		Description: "test tool",
		Parameters:  tools.ObjectSchema(map[string]tools.Property{}, ""),
	}
}

func truncatedToolResponse(reason string) *model.ChatResponse {
	return &model.ChatResponse{
		ID:    "resp-" + reason,
		Model: "test-model",
		Choices: []model.Choice{{
			Message: model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{
				ID:       "call_" + strings.ReplaceAll(reason, "_", "-"),
				Type:     "function",
				Function: model.FunctionCall{Name: "test_tool", Arguments: `{"value":42}`},
			}}},
			FinishReason: reason,
		}},
		Usage: model.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}
}

func truncatedTextResponse(reason string) *model.ChatResponse {
	return &model.ChatResponse{
		ID:    "resp-text-" + reason,
		Model: "test-model",
		Choices: []model.Choice{{
			Message: model.Message{
				Role:             "assistant",
				Content:          "public partial text",
				Reasoning:        invokerPrivateReasoningSentinel,
				ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", Text: invokerPrivateReasoningSentinel}},
			},
			FinishReason: reason,
		}},
		Usage: model.Usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7},
	}
}

func TestInvokerRejectsTruncatedToolResponses(t *testing.T) {
	for _, reason := range []string{"length", "max_tokens", "max_output_tokens"} {
		t.Run(reason, func(t *testing.T) {
			invoker := NewInvoker(InvokerConfig{Client: completionSafetyClient{response: truncatedToolResponse(reason)}, Model: "test-model"})
			result, trace, err := invoker.Invoke(context.Background(), "system", "user", completionSafetyTool(), nil)
			if err == nil {
				t.Fatalf("Invoke error = nil, result=%+v trace=%+v", result, trace)
			}
			if result == nil {
				t.Fatal("result = nil, want partial result with retained trace only")
			}
			if result.ToolCall != nil {
				t.Fatalf("truncated tool call exposed as actionable result: %+v", result.ToolCall)
			}
			if trace == nil || trace.Tokens.Input != 10 || trace.Tokens.Output != 20 {
				t.Fatalf("trace = %+v, want retained usage", trace)
			}
			if trace.Response == nil || trace.Response.FinishReason != reason {
				t.Fatalf("trace response = %+v, want finish %q", trace.Response, reason)
			}
			if len(trace.ToolCalls) != 1 || trace.ToolCalls[0].Name != "test_tool" {
				t.Fatalf("trace tool calls = %+v, want retained evidence", trace.ToolCalls)
			}
		})
	}
}

func TestInvokerRejectsTruncatedTextResponses(t *testing.T) {
	for _, reason := range []string{"length", "max_tokens", "max_output_tokens"} {
		t.Run(reason, func(t *testing.T) {
			invoker := NewInvoker(InvokerConfig{Client: completionSafetyClient{response: truncatedTextResponse(reason)}, Model: "test-model"})
			text, trace, err := invoker.InvokeText(context.Background(), "system", "user", nil)
			if err == nil {
				t.Fatalf("InvokeText error = nil, text=%q trace=%+v", text, trace)
			}
			if text != "public partial text" {
				t.Fatalf("partial text = %q, want retained public partial text", text)
			}
			if strings.Contains(text, invokerPrivateReasoningSentinel) || (trace != nil && strings.Contains(trace.Content, invokerPrivateReasoningSentinel)) {
				t.Fatalf("private reasoning leaked through text/trace: text=%q trace=%+v", text, trace)
			}
			if trace == nil || trace.Tokens.Input != 3 || trace.Tokens.Output != 4 {
				t.Fatalf("trace = %+v, want retained usage", trace)
			}
			if trace.Response == nil || trace.Response.FinishReason != reason {
				t.Fatalf("trace response = %+v, want finish %q", trace.Response, reason)
			}
		})
	}
}

func TestInvokerNormalStopAndToolCallsRemainSuccessful(t *testing.T) {
	t.Run("text stop", func(t *testing.T) {
		invoker := NewInvoker(InvokerConfig{Client: completionSafetyClient{response: &model.ChatResponse{
			Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: "done"}, FinishReason: "stop"}},
			Usage:   model.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		}}, Model: "test-model"})
		text, trace, err := invoker.InvokeText(context.Background(), "system", "user", nil)
		if err != nil || text != "done" || trace == nil || trace.Response == nil || trace.Response.FinishReason != "stop" {
			t.Fatalf("InvokeText = %q, trace=%+v, err=%v; want successful stop", text, trace, err)
		}
	})
	t.Run("tool calls", func(t *testing.T) {
		invoker := NewInvoker(InvokerConfig{Client: completionSafetyClient{response: &model.ChatResponse{
			Choices: []model.Choice{{Message: model.Message{Role: "assistant", ToolCalls: []model.ToolCall{{
				ID: "call_ok", Type: "function", Function: model.FunctionCall{Name: "test_tool", Arguments: `{"value":42}`},
			}}}, FinishReason: "tool_calls"}},
			Usage: model.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		}}, Model: "test-model"})
		result, trace, err := invoker.Invoke(context.Background(), "system", "user", completionSafetyTool(), nil)
		if err != nil || result == nil || result.ToolCall == nil || result.ToolCall.Name != "test_tool" || trace == nil || trace.Response == nil || trace.Response.FinishReason != "tool_calls" {
			t.Fatalf("Invoke = result=%+v trace=%+v err=%v; want successful tool call", result, trace, err)
		}
	})
}

func TestInvokeStreamRejectsTruncatedFinishAndQueuedError(t *testing.T) {
	chunks := make(chan model.StreamChunk, 2)
	errs := make(chan error, 1)
	finish := "length"
	chunks <- model.StreamChunk{
		ID: "resp-stream", Model: "test-model",
		Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: "partial stream"}, FinishReason: &finish}},
	}
	chunks <- model.StreamChunk{ID: "resp-stream", Model: "test-model", Usage: &model.Usage{PromptTokens: 5, CompletionTokens: 6, TotalTokens: 11}}
	close(chunks)
	errs <- errors.New("terminal stream error")
	close(errs)

	invoker := NewInvoker(InvokerConfig{Client: completionSafetyStreamClient{chunks: chunks, errs: errs}, Model: "test-model"})
	result, trace, err := invoker.InvokeStream(context.Background(), "system", "user", completionSafetyTool(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "terminal stream error") {
		t.Fatalf("InvokeStream error = %v, want queued terminal stream error", err)
	}
	if result == nil || result.TextContent != "partial stream" || result.ToolCall != nil {
		t.Fatalf("result = %+v, want retained partial text and no actionable tool", result)
	}
	if trace == nil || trace.Tokens.Input != 5 || trace.Tokens.Output != 6 || trace.Response == nil || trace.Response.FinishReason != "length" {
		t.Fatalf("trace = %+v, want retained usage and length finish", trace)
	}
}

func TestInvokeStreamRejectsTruncatedFinishWithoutTransportError(t *testing.T) {
	for _, reason := range []string{"length", "max_tokens", "max_output_tokens"} {
		t.Run(reason, func(t *testing.T) {
			chunks := make(chan model.StreamChunk, 2)
			errs := make(chan error)
			finish := reason
			chunks <- model.StreamChunk{
				ID: "resp-stream-" + reason, Model: "test-model",
				Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: "partial stream"}, FinishReason: &finish}},
			}
			chunks <- model.StreamChunk{ID: "resp-stream-" + reason, Model: "test-model", Usage: &model.Usage{PromptTokens: 5, CompletionTokens: 6, TotalTokens: 11}}
			close(chunks)
			close(errs)

			invoker := NewInvoker(InvokerConfig{Client: completionSafetyStreamClient{chunks: chunks, errs: errs}, Model: "test-model"})
			result, trace, err := invoker.InvokeStream(context.Background(), "system", "user", completionSafetyTool(), nil, nil)
			if err == nil {
				t.Fatalf("InvokeStream error = nil, result=%+v trace=%+v", result, trace)
			}
			if result == nil || result.TextContent != "partial stream" || result.ToolCall != nil {
				t.Fatalf("result = %+v, want retained partial text and no actionable tool", result)
			}
			if trace == nil || trace.Tokens.Input != 5 || trace.Tokens.Output != 6 || trace.Response == nil || trace.Response.FinishReason != reason {
				t.Fatalf("trace = %+v, want retained usage and finish %q", trace, reason)
			}
		})
	}
}

func TestInvokeStreamSucceedsWhenBothChannelsClose(t *testing.T) {
	chunks := make(chan model.StreamChunk, 1)
	errs := make(chan error, 1)
	chunks <- model.StreamChunk{Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: "done"}}}}
	close(chunks)
	close(errs)

	invoker := NewInvoker(InvokerConfig{Client: completionSafetyStreamClient{chunks: chunks, errs: errs}, Model: "test-model"})
	result, trace, err := invoker.InvokeStream(context.Background(), "system", "user", completionSafetyTool(), nil, nil)
	if err != nil || result == nil || result.TextContent != "done" || trace == nil {
		t.Fatalf("InvokeStream = result=%+v trace=%+v err=%v, want success after both channels close", result, trace, err)
	}
}

func TestInvokeStreamWaitsForErrorChannelAfterChunksClose(t *testing.T) {
	chunks := make(chan model.StreamChunk, 1)
	errs := make(chan error, 1)
	chunkSeen := make(chan struct{})
	done := make(chan struct {
		result *Result
		trace  *transparency.Trace
		err    error
	}, 1)

	chunks <- model.StreamChunk{Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: "partial before delayed error"}}}}

	invoker := NewInvoker(InvokerConfig{Client: completionSafetyStreamClient{chunks: chunks, errs: errs}, Model: "test-model"})
	go func() {
		result, trace, err := invoker.InvokeStream(context.Background(), "system", "user", completionSafetyTool(), nil, func(_, content string) {
			if content != "" {
				select {
				case <-chunkSeen:
				default:
					close(chunkSeen)
				}
			}
		})
		done <- struct {
			result *Result
			trace  *transparency.Trace
			err    error
		}{result: result, trace: trace, err: err}
	}()

	select {
	case <-chunkSeen:
	case <-time.After(time.Second):
		t.Fatal("stream chunk was not consumed")
	}
	close(chunks)
	errs <- errors.New("delayed terminal error")
	close(errs)

	select {
	case got := <-done:
		if got.err == nil || !strings.Contains(got.err.Error(), "delayed terminal error") {
			t.Fatalf("InvokeStream error = %v, want delayed terminal error", got.err)
		}
		if got.result == nil || got.result.TextContent != "partial before delayed error" || got.trace == nil {
			t.Fatalf("InvokeStream = result=%+v trace=%+v err=%v, want retained partial error", got.result, got.trace, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("InvokeStream did not return after delayed terminal error")
	}
}

func TestDrainReadyStreamChunksDoesNotChaseCallbackRefills(t *testing.T) {
	chunks := make(chan model.StreamChunk, 2)
	finish := "length"
	chunks <- model.StreamChunk{Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: "first"}, FinishReason: &finish}}}

	acc := model.NewStreamAccumulator()
	observed := false
	remaining := drainReadyStreamChunks(acc, func(_, content string) {
		if content == "first" {
			chunks <- model.StreamChunk{Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: "refilled"}}}}
		}
	}, chunks, &finish, &observed)
	if remaining == nil {
		t.Fatal("remaining channel = nil, want open channel with callback-refilled chunk")
	}
	if !observed {
		t.Fatal("observed = false, want drained frame to mark observed stream material")
	}
	if got := model.ExtractTextContentOrEmpty(acc.FinalizeWithTokenParsing().Content); got != "first" {
		t.Fatalf("drained content = %q, want only initially buffered chunk", got)
	}
	select {
	case leftover := <-chunks:
		if got := leftover.Choices[0].Delta.Content; got != "refilled" {
			t.Fatalf("leftover content = %q, want callback-refilled chunk", got)
		}
	default:
		t.Fatal("callback-refilled chunk was drained; want bounded snapshot drain")
	}
}

func TestInvokeStreamCancellationReturnsAccumulatedMaterial(t *testing.T) {
	chunks := make(chan model.StreamChunk, 1)
	errs := make(chan error)
	chunks <- model.StreamChunk{
		Choices: []model.StreamChoice{{Delta: model.MessageDelta{
			Content:          "partial before cancel",
			Reasoning:        invokerPrivateReasoningSentinel,
			ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", Text: invokerPrivateReasoningSentinel}},
		}}},
		Usage: &model.Usage{PromptTokens: 8, CompletionTokens: 9, TotalTokens: 17},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	invoker := NewInvoker(InvokerConfig{Client: completionSafetyStreamClient{chunks: chunks, errs: errs}, Model: "test-model"})
	result, trace, err := invoker.InvokeStream(ctx, "system", "user", completionSafetyTool(), nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("InvokeStream error = %v, want context canceled", err)
	}
	if result == nil || result.TextContent != "partial before cancel" {
		t.Fatalf("result = %+v, want accumulated partial text", result)
	}
	if trace == nil || trace.Tokens.Input != 8 || trace.Tokens.Output != 9 {
		t.Fatalf("trace = %+v, want retained usage", trace)
	}
	if strings.Contains(result.TextContent, invokerPrivateReasoningSentinel) || strings.Contains(trace.Content, invokerPrivateReasoningSentinel) {
		t.Fatalf("private reasoning leaked through public text: result=%+v trace=%+v", result, trace)
	}
}
