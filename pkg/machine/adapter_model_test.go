package machine

import (
	"context"
	"fmt"
	"testing"

	"github.com/odvcencio/buckley/pkg/model"
)

type mockChatClient struct {
	responses []*model.ChatResponse
	errors    []error
	calls     int
	lastReq   model.ChatRequest
}

func (m *mockChatClient) ChatCompletion(_ context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	idx := m.calls
	m.calls++
	m.lastReq = req
	if idx < len(m.errors) && m.errors[idx] != nil {
		return nil, m.errors[idx]
	}
	if idx < len(m.responses) {
		return m.responses[idx], nil
	}
	return &model.ChatResponse{
		Choices: []model.Choice{
			{Message: model.Message{Role: "assistant", Content: "default"}, FinishReason: "end_turn"},
		},
	}, nil
}

func TestModelClientAdapter_SimpleConversation(t *testing.T) {
	client := &mockChatClient{
		responses: []*model.ChatResponse{
			{
				Choices: []model.Choice{
					{
						Message:      model.Message{Role: "assistant", Content: "Hello!"},
						FinishReason: "end_turn",
					},
				},
				Usage: model.Usage{TotalTokens: 50},
			},
		},
	}

	adapter := NewModelClientAdapter(client, "gpt-4", nil)
	adapter.AddMessages(model.Message{Role: "user", Content: "hi"})

	event, err := adapter.Call(context.Background(), CallModel{})
	if err != nil {
		t.Fatal(err)
	}

	mc, ok := event.(ModelCompleted)
	if !ok {
		t.Fatalf("event type = %T, want ModelCompleted", event)
	}
	if mc.Content != "Hello!" {
		t.Errorf("content = %q, want 'Hello!'", mc.Content)
	}
	if mc.FinishReason != "end_turn" {
		t.Errorf("finish_reason = %q, want 'end_turn'", mc.FinishReason)
	}
	if mc.TokensUsed != 50 {
		t.Errorf("tokens = %d, want 50", mc.TokensUsed)
	}

	msgs := adapter.Messages()
	if len(msgs) != 2 {
		t.Fatalf("history len = %d, want 2", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("msg[0].Role = %s, want user", msgs[0].Role)
	}
	if msgs[1].Role != "assistant" {
		t.Errorf("msg[1].Role = %s, want assistant", msgs[1].Role)
	}
}

func TestModelClientAdapter_ToolUseResponse(t *testing.T) {
	client := &mockChatClient{
		responses: []*model.ChatResponse{
			{
				Choices: []model.Choice{
					{
						Message: model.Message{
							Role: "assistant",
							ToolCalls: []model.ToolCall{
								{
									ID:   "tc1",
									Type: "function",
									Function: model.FunctionCall{
										Name:      "read_file",
										Arguments: `{"path":"foo.go"}`,
									},
								},
							},
						},
						FinishReason: "tool_use",
					},
				},
			},
		},
	}

	adapter := NewModelClientAdapter(client, "gpt-4", nil)
	adapter.AddMessages(model.Message{Role: "user", Content: "read foo.go"})

	event, err := adapter.Call(context.Background(), CallModel{})
	if err != nil {
		t.Fatal(err)
	}

	mc, ok := event.(ModelCompleted)
	if !ok {
		t.Fatalf("event type = %T, want ModelCompleted", event)
	}
	if mc.FinishReason != "tool_use" {
		t.Errorf("finish_reason = %q, want 'tool_use'", mc.FinishReason)
	}
	if len(mc.ToolCalls) != 1 {
		t.Fatalf("tool_calls len = %d, want 1", len(mc.ToolCalls))
	}
	if mc.ToolCalls[0].Name != "read_file" {
		t.Errorf("tool name = %s, want read_file", mc.ToolCalls[0].Name)
	}
	if mc.ToolCalls[0].Params["path"] != "foo.go" {
		t.Errorf("params.path = %v, want foo.go", mc.ToolCalls[0].Params["path"])
	}
}

func TestModelClientAdapter_SteeringInjection(t *testing.T) {
	client := &mockChatClient{
		responses: []*model.ChatResponse{
			{
				Choices: []model.Choice{
					{Message: model.Message{Role: "assistant", Content: "ok"}, FinishReason: "end_turn"},
				},
			},
		},
	}

	adapter := NewModelClientAdapter(client, "gpt-4", nil)
	adapter.AddMessages(model.Message{Role: "user", Content: "do stuff"})

	_, err := adapter.Call(context.Background(), CallModel{Steering: "use JWT"})
	if err != nil {
		t.Fatal(err)
	}

	if len(client.lastReq.Messages) != 2 {
		t.Fatalf("request messages = %d, want 2 (user + system steering)", len(client.lastReq.Messages))
	}
	last := client.lastReq.Messages[len(client.lastReq.Messages)-1]
	if last.Role != "system" {
		t.Errorf("steering msg role = %s, want system", last.Role)
	}
	if last.Content != "use JWT" {
		t.Errorf("steering content = %v, want 'use JWT'", last.Content)
	}
}

func TestModelClientAdapter_ModelError(t *testing.T) {
	client := &mockChatClient{
		errors: []error{fmt.Errorf("rate limited")},
	}

	adapter := NewModelClientAdapter(client, "gpt-4", nil)
	adapter.AddMessages(model.Message{Role: "user", Content: "hi"})

	event, err := adapter.Call(context.Background(), CallModel{})
	if err != nil {
		t.Fatal("expected event, not error")
	}

	mf, ok := event.(ModelFailed)
	if !ok {
		t.Fatalf("event type = %T, want ModelFailed", event)
	}
	if mf.Err == nil {
		t.Error("expected error in ModelFailed")
	}
	if !mf.Retryable {
		t.Error("expected retryable=true for API error")
	}
}

func TestModelClientAdapter_EmptyResponse(t *testing.T) {
	client := &mockChatClient{
		responses: []*model.ChatResponse{
			{Choices: nil},
		},
	}

	adapter := NewModelClientAdapter(client, "gpt-4", nil)
	adapter.AddMessages(model.Message{Role: "user", Content: "hi"})

	event, err := adapter.Call(context.Background(), CallModel{})
	if err != nil {
		t.Fatal(err)
	}

	mf, ok := event.(ModelFailed)
	if !ok {
		t.Fatalf("event type = %T, want ModelFailed", event)
	}
	if mf.Retryable {
		t.Error("empty response should not be retryable")
	}
}

func TestModelClientAdapter_AddToolResults(t *testing.T) {
	adapter := NewModelClientAdapter(&mockChatClient{}, "gpt-4", nil)
	adapter.AddMessages(model.Message{Role: "user", Content: "edit foo"})

	adapter.AddToolResults([]ToolCallResult{
		{ID: "tc1", Name: "edit_file", Result: "edited", Success: true},
		{ID: "tc2", Name: "broken", Err: fmt.Errorf("failed"), Success: false},
	})

	msgs := adapter.Messages()
	if len(msgs) != 3 {
		t.Fatalf("history len = %d, want 3", len(msgs))
	}
	if msgs[1].Role != "tool" || msgs[1].ToolCallID != "tc1" {
		t.Errorf("msg[1] = role=%s id=%s, want tool/tc1", msgs[1].Role, msgs[1].ToolCallID)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "tc2" {
		t.Errorf("msg[2] = role=%s id=%s, want tool/tc2", msgs[2].Role, msgs[2].ToolCallID)
	}
}

func TestModelClientAdapter_ReasoningMode(t *testing.T) {
	client := &mockChatClient{
		responses: []*model.ChatResponse{
			{
				Choices: []model.Choice{
					{Message: model.Message{Role: "assistant", Content: "thought about it"}, FinishReason: "end_turn"},
				},
			},
		},
	}

	adapter := NewModelClientAdapter(client, "o1", nil)
	adapter.AddMessages(model.Message{Role: "user", Content: "think hard"})

	_, err := adapter.Call(context.Background(), CallModel{EnableReasoning: true})
	if err != nil {
		t.Fatal(err)
	}

	if client.lastReq.Reasoning == nil {
		t.Fatal("expected reasoning config to be set")
	}
	if client.lastReq.Reasoning.Effort != "high" {
		t.Errorf("reasoning effort = %q, want 'high'", client.lastReq.Reasoning.Effort)
	}
}
