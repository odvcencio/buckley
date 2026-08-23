package oneshot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/draco/buckley/pkg/model"
)

// fakeStreamingClient implements streamingModelClient for tests, delivering
// canned chunks (with optional per-chunk delays, to exercise mid-stream
// context cancellation) followed by a single terminal error (nil on
// success), mirroring model.Manager.ChatCompletionStream's channel contract.
type fakeStreamingClient struct {
	chunks []model.StreamChunk
	delays []time.Duration // parallel to chunks; 0/unspecified means send immediately
	err    error
}

func (f *fakeStreamingClient) ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	chunkCh := make(chan model.StreamChunk)
	errCh := make(chan error, 1)

	go func() {
		defer close(chunkCh)
		defer close(errCh)

		for i, c := range f.chunks {
			var d time.Duration
			if i < len(f.delays) {
				d = f.delays[i]
			}
			if d > 0 {
				select {
				case <-time.After(d):
				case <-ctx.Done():
					errCh <- ctx.Err()
					return
				}
			}
			select {
			case chunkCh <- c:
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			}
		}
		errCh <- f.err
	}()

	return chunkCh, errCh
}

func strPtr(s string) *string { return &s }

func TestStreamChatCompletion_AssemblesContentAndUsage(t *testing.T) {
	client := &fakeStreamingClient{chunks: []model.StreamChunk{
		{ID: "resp-1", Model: "glm-4.6", Choices: []model.StreamChoice{{Delta: model.MessageDelta{Role: "assistant"}}}},
		{Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: "Hello, "}}}},
		{Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: "world."}, FinishReason: strPtr("stop")}}},
		{Usage: &model.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}},
	}}

	resp, err := streamChatCompletion(context.Background(), client, model.ChatRequest{Model: "glm-4.6"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}

	content, _ := resp.Choices[0].Message.Content.(string)
	if content != "Hello, world." {
		t.Errorf("content = %q, want %q", content, "Hello, world.")
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("finish reason = %q, want %q", resp.Choices[0].FinishReason, "stop")
	}
	if resp.Usage.TotalTokens != 8 {
		t.Errorf("usage.TotalTokens = %d, want 8", resp.Usage.TotalTokens)
	}
	if resp.ID != "resp-1" || resp.Model != "glm-4.6" {
		t.Errorf("unexpected ID/Model: %q/%q", resp.ID, resp.Model)
	}
}

func TestStreamChatCompletion_AssemblesFragmentedToolCall(t *testing.T) {
	client := &fakeStreamingClient{chunks: []model.StreamChunk{
		{Choices: []model.StreamChoice{{Delta: model.MessageDelta{Role: "assistant", ToolCalls: []model.ToolCallDelta{
			{Index: 0, ID: "call_1", Type: "function", Function: &model.FunctionCallDelta{Name: "get_wea"}},
		}}}}},
		{Choices: []model.StreamChoice{{Delta: model.MessageDelta{ToolCalls: []model.ToolCallDelta{
			{Index: 0, Function: &model.FunctionCallDelta{Name: "ther", Arguments: `{"locat`}},
		}}}}},
		{Choices: []model.StreamChoice{{Delta: model.MessageDelta{ToolCalls: []model.ToolCallDelta{
			{Index: 0, Function: &model.FunctionCallDelta{Arguments: `ion":"NYC"}`}},
		}}}}},
	}}

	resp, err := streamChatCompletion(context.Background(), client, model.ChatRequest{Model: "glm-4.6"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 assembled tool call, got %d", len(resp.Choices[0].Message.ToolCalls))
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "get_weather" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
	if tc.Function.Arguments != `{"location":"NYC"}` {
		t.Errorf("arguments = %q, want %q", tc.Function.Arguments, `{"location":"NYC"}`)
	}
}

func TestStreamChatCompletion_PartialSurvivesContextCancellation(t *testing.T) {
	client := &fakeStreamingClient{
		chunks: []model.StreamChunk{
			{Choices: []model.StreamChoice{{Delta: model.MessageDelta{Role: "assistant", Content: "partial answer"}}}},
			{Choices: []model.StreamChoice{{Delta: model.MessageDelta{Content: " more that should not arrive"}}}},
		},
		delays: []time.Duration{0, 500 * time.Millisecond},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	resp, err := streamChatCompletion(ctx, client, model.ChatRequest{Model: "glm-4.6"})
	if err != nil {
		t.Fatalf("expected a partial response with nil error when ctx expires mid-stream, got err=%v", err)
	}
	content, _ := resp.Choices[0].Message.Content.(string)
	if content != "partial answer" {
		t.Errorf("content = %q, want the partial content that arrived before the deadline (%q)", content, "partial answer")
	}
	if strings.Contains(content, "should not arrive") {
		t.Errorf("content leaked data that should have arrived after the deadline: %q", content)
	}
}

func TestStreamChatCompletion_ErrorWithNoContentPropagates(t *testing.T) {
	client := &fakeStreamingClient{err: fmt.Errorf("boom")}

	_, err := streamChatCompletion(context.Background(), client, model.ChatRequest{Model: "glm-4.6"})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected the underlying stream error to propagate when nothing was received, got %v", err)
	}
}

// reasoningAwareMockClient implements ModelClient + reasoningAwareModelClient
// (but not streamingModelClient), letting tests assert on the exact request
// chatCompletion() sent while still exercising the blocking fallback path.
type reasoningAwareMockClient struct {
	response *model.ChatResponse
	supports bool
	lastReq  model.ChatRequest
}

func (m *reasoningAwareMockClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	m.lastReq = req
	return m.response, nil
}

func (m *reasoningAwareMockClient) SupportsReasoning(modelID string) bool {
	return m.supports
}

func TestChatCompletion_SetsReasoningEffortWhenModelSupportsIt(t *testing.T) {
	client := &reasoningAwareMockClient{response: toolCallTextResponse("plain text answer"), supports: true}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "glm-4.6-reasoning"})

	if _, _, err := invoker.InvokeText(context.Background(), "system", "user", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.lastReq.Reasoning == nil {
		t.Fatalf("expected req.Reasoning to be set for a reasoning-capable model")
	}
	if client.lastReq.Reasoning.Effort != defaultReasoningEffort {
		t.Errorf("Effort = %q, want %q", client.lastReq.Reasoning.Effort, defaultReasoningEffort)
	}
}

func TestChatCompletion_LeavesReasoningUnsetWhenModelDoesNotSupportIt(t *testing.T) {
	client := &reasoningAwareMockClient{response: toolCallTextResponse("plain text answer"), supports: false}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "gpt-5-nano"})

	if _, _, err := invoker.InvokeText(context.Background(), "system", "user", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.lastReq.Reasoning != nil {
		t.Errorf("expected req.Reasoning to stay nil for a non-reasoning model, got %+v", client.lastReq.Reasoning)
	}
}

// TestChatCompletion_PlainMockClientStillWorks proves the optional-interface
// design (reasoningAwareModelClient / streamingModelClient) degrades safely:
// mockClient (invoker_test.go) implements neither, so chatCompletion must
// fall back to the original blocking ChatCompletion call without a type
// assertion panic or behavior change.
func TestChatCompletion_PlainMockClientStillWorks(t *testing.T) {
	client := &mockClient{response: toolCallTextResponse("plain text answer")}
	invoker := NewInvoker(InvokerConfig{Client: client, Model: "glm-4.6"})

	content, _, err := invoker.InvokeText(context.Background(), "system", "user", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "plain text answer" {
		t.Errorf("content = %q, want %q", content, "plain text answer")
	}
}
