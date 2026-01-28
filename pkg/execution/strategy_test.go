package execution

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

// MockModelClient implements ModelClient for testing.
type MockModelClient struct {
	Responses []model.ChatResponse
	CallCount int
	Requests  []model.ChatRequest
}

func (m *MockModelClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	m.Requests = append(m.Requests, req)

	if m.CallCount >= len(m.Responses) {
		return &model.ChatResponse{
			Choices: []model.Choice{
				{
					Message: model.Message{
						Content: "Done!",
					},
				},
			},
		}, nil
	}

	resp := m.Responses[m.CallCount]
	m.CallCount++
	return &resp, nil
}

func (m *MockModelClient) GetExecutionModel() string {
	return "test-model"
}

func (m *MockModelClient) ChatCompletionStream(ctx context.Context, req model.ChatRequest) (<-chan model.StreamChunk, <-chan error) {
	// For tests, convert non-streaming response to streaming format
	chunkChan := make(chan model.StreamChunk, 1)
	errChan := make(chan error, 1)

	go func() {
		defer close(chunkChan)
		defer close(errChan)

		resp, err := m.ChatCompletion(ctx, req)
		if err != nil {
			errChan <- err
			return
		}

		if len(resp.Choices) > 0 {
			msg := resp.Choices[0].Message
			chunk := model.StreamChunk{
				ID:    resp.ID,
				Model: resp.Model,
				Choices: []model.StreamChoice{{
					Index: 0,
					Delta: model.MessageDelta{
						Role:      msg.Role,
						Content:   model.ExtractTextContentOrEmpty(msg.Content),
						Reasoning: msg.Reasoning,
					},
				}},
				Usage: &resp.Usage,
			}
			// Convert tool calls to deltas
			for i, tc := range msg.ToolCalls {
				chunk.Choices[0].Delta.ToolCalls = append(chunk.Choices[0].Delta.ToolCalls, model.ToolCallDelta{
					Index: i,
					ID:    tc.ID,
					Type:  tc.Type,
					Function: &model.FunctionCallDelta{
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				})
			}
			chunkChan <- chunk
		}
	}()

	return chunkChan, errChan
}

// emptyRegistry creates a registry with no built-in tools for testing.
func emptyRegistry() *tool.Registry {
	return tool.NewRegistry(tool.WithBuiltinFilter(func(t tool.Tool) bool {
		return false
	}))
}

func TestClassicStrategy_Execute_NoToolCalls(t *testing.T) {
	mock := &MockModelClient{
		Responses: []model.ChatResponse{
			{
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content: "Simple response",
						},
					},
				},
			},
		},
	}

	registry := emptyRegistry()

	strategy, err := NewClassicStrategy(StrategyConfig{
		Models:   mock,
		Registry: registry,
	})
	if err != nil {
		t.Fatalf("failed to create strategy: %v", err)
	}

	result, err := strategy.Execute(context.Background(), ExecutionRequest{
		Prompt: "Hello",
	})
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Content != "Simple response" {
		t.Errorf("unexpected content: %s", result.Content)
	}
}

func TestClassicStrategy_Execute_WithThinkingTags(t *testing.T) {
	mock := &MockModelClient{
		Responses: []model.ChatResponse{
			{
				Choices: []model.Choice{
					{
						Message: model.Message{
							Content: "<think>Hmm, let me think about this...</think>Here's my answer.",
						},
					},
				},
			},
		},
	}

	registry := emptyRegistry()

	strategy, err := NewClassicStrategy(StrategyConfig{
		Models:          mock,
		Registry:        registry,
		EnableReasoning: true,
	})
	if err != nil {
		t.Fatalf("failed to create strategy: %v", err)
	}

	result, err := strategy.Execute(context.Background(), ExecutionRequest{
		Prompt: "Think about this",
	})
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Content != "Here's my answer." {
		t.Errorf("unexpected content: %q", result.Content)
	}

	if result.Reasoning != "Hmm, let me think about this..." {
		t.Errorf("unexpected reasoning: %q", result.Reasoning)
	}
}

func TestExtractThinkingContent_ViaModel(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantThinking string
		wantContent  string
	}{
		{
			name:         "no_thinking_tags",
			input:        "Hello, world!",
			wantThinking: "",
			wantContent:  "Hello, world!",
		},
		{
			name:         "single_thinking_block",
			input:        "<think>Let me consider this...</think>Here is my answer.",
			wantThinking: "Let me consider this...",
			wantContent:  "Here is my answer.",
		},
		{
			name:         "multiline_thinking",
			input:        "<think>\nStep 1: Do this\nStep 2: Do that\n</think>\nThe result is 42.",
			wantThinking: "Step 1: Do this\nStep 2: Do that",
			wantContent:  "The result is 42.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotThinking, gotContent := model.ExtractThinkingContent(tt.input)

			if gotThinking != tt.wantThinking {
				t.Errorf("thinking = %q, want %q", gotThinking, tt.wantThinking)
			}

			if gotContent != tt.wantContent {
				t.Errorf("content = %q, want %q", gotContent, tt.wantContent)
			}
		})
	}
}

func TestExecutionResult_Fields(t *testing.T) {
	result := ExecutionResult{
		Content:    "Test result",
		Reasoning:  "I thought about this",
		Iterations: 3,
		Confidence: 0.85,
		Artifacts:  []string{"artifact1.txt"},
	}

	if result.Content != "Test result" {
		t.Errorf("Content = %s, want Test result", result.Content)
	}
	if result.Reasoning != "I thought about this" {
		t.Errorf("Reasoning = %s, want I thought about this", result.Reasoning)
	}
	if result.Iterations != 3 {
		t.Errorf("Iterations = %d, want 3", result.Iterations)
	}
	if result.Confidence != 0.85 {
		t.Errorf("Confidence = %f, want 0.85", result.Confidence)
	}
}

func TestStrategyNames(t *testing.T) {
	classic := &ClassicStrategy{}
	if classic.Name() != "classic" {
		t.Errorf("ClassicStrategy.Name() = %s, want classic", classic.Name())
	}

	rlm := &RLMStrategy{}
	if rlm.Name() != "rlm" {
		t.Errorf("RLMStrategy.Name() = %s, want rlm", rlm.Name())
	}
}

func TestStrategyStreaming(t *testing.T) {
	classic := &ClassicStrategy{}
	if !classic.SupportsStreaming() {
		t.Error("ClassicStrategy should support streaming")
	}

	rlm := &RLMStrategy{}
	if rlm.SupportsStreaming() {
		t.Error("RLMStrategy should not support streaming")
	}
}
