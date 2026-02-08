package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/odvcencio/buckley/pkg/model"
)

// ChatCompletionClient is the minimal interface for model inference.
// toolrunner.ModelClient and model.Client both satisfy this.
type ChatCompletionClient interface {
	ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error)
}

// ModelClientAdapter adapts a ChatCompletionClient to the ModelCaller interface.
// It maintains conversation history and translates between machine and model types.
type ModelClientAdapter struct {
	client ChatCompletionClient
	model  string
	tools  []map[string]any

	mu       sync.Mutex
	messages []model.Message
}

// NewModelClientAdapter creates a new adapter for the given model client.
func NewModelClientAdapter(client ChatCompletionClient, modelName string, tools []map[string]any) *ModelClientAdapter {
	return &ModelClientAdapter{
		client: client,
		model:  modelName,
		tools:  tools,
	}
}

// AddMessages appends messages to the conversation history.
func (a *ModelClientAdapter) AddMessages(msgs ...model.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.messages = append(a.messages, msgs...)
}

// AddToolResults appends tool result messages to the conversation history.
func (a *ModelClientAdapter) AddToolResults(results []ToolCallResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range results {
		content := r.Result
		if !r.Success && r.Err != nil {
			content = fmt.Sprintf("Error: %s", r.Err.Error())
		}
		a.messages = append(a.messages, model.Message{
			Role:       "tool",
			Content:    content,
			ToolCallID: r.ID,
			Name:       r.Name,
		})
	}
}

// Messages returns a copy of the current conversation history.
func (a *ModelClientAdapter) Messages() []model.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	msgs := make([]model.Message, len(a.messages))
	copy(msgs, a.messages)
	return msgs
}

// Call implements ModelCaller by building a ChatRequest from the current
// conversation history and the CallModel action.
func (a *ModelClientAdapter) Call(ctx context.Context, action CallModel) (Event, error) {
	a.mu.Lock()
	messages := make([]model.Message, len(a.messages))
	copy(messages, a.messages)
	a.mu.Unlock()

	if action.Steering != "" {
		messages = append(messages, model.Message{
			Role:    "system",
			Content: action.Steering,
		})
	}

	req := model.ChatRequest{
		Model:    a.model,
		Messages: messages,
		Tools:    a.tools,
	}
	if action.EnableReasoning {
		req.Reasoning = &model.ReasoningConfig{Effort: "high"}
	}

	resp, err := a.client.ChatCompletion(ctx, req)
	if err != nil {
		return ModelFailed{Err: err, Retryable: true}, nil
	}

	if len(resp.Choices) == 0 {
		return ModelFailed{Err: fmt.Errorf("empty response from model"), Retryable: false}, nil
	}

	choice := resp.Choices[0]

	// Append assistant response to history
	a.mu.Lock()
	a.messages = append(a.messages, choice.Message)
	a.mu.Unlock()

	var toolCalls []ToolCallRequest
	for _, tc := range choice.Message.ToolCalls {
		var params map[string]any
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &params); err != nil {
				return ModelFailed{
					Err:       fmt.Errorf("invalid tool call arguments for %s: %w", tc.Function.Name, err),
					Retryable: true,
				}, nil
			}
		}
		toolCalls = append(toolCalls, ToolCallRequest{
			ID:     tc.ID,
			Name:   tc.Function.Name,
			Params: params,
		})
	}

	return ModelCompleted{
		Content:      messageContentString(choice.Message),
		FinishReason: choice.FinishReason,
		ToolCalls:    toolCalls,
		TokensUsed:   resp.Usage.TotalTokens,
		Reasoning:    choice.Message.Reasoning,
	}, nil
}

func messageContentString(msg model.Message) string {
	switch c := msg.Content.(type) {
	case string:
		return c
	case nil:
		return ""
	default:
		data, _ := json.Marshal(c)
		return string(data)
	}
}
