package agentloop

import (
	"context"
	"errors"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestController_TruncatedTerminalCandidatePreservesPublicPrefix(t *testing.T) {
	for _, tt := range []struct {
		name      string
		choice    model.Choice
		wantText  string
		wantUsage model.Usage
	}{
		{
			name: "text",
			choice: model.Choice{
				Message: model.Message{
					Role:             "assistant",
					Content:          "  useful public prefix  ",
					Reasoning:        "PRIVATE_REASONING",
					ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", Text: "PRIVATE_DETAIL"}},
				},
				FinishReason: " MAX_TOKENS ",
			},
			wantText:  "useful public prefix",
			wantUsage: model.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8},
		},
		{
			name: "text with valid tool call",
			choice: model.Choice{
				Message: model.Message{
					Role:    "assistant",
					Content: "useful public prefix before tool",
					ToolCalls: []model.ToolCall{{
						ID:       "call-1",
						Type:     "function",
						Function: model.FunctionCall{Name: "write_file", Arguments: `{"path":"x","content":"y"}`},
					}},
					Reasoning: "PRIVATE_TOOL_REASONING",
				},
				FinishReason: "length",
			},
			wantText:  "useful public prefix before tool",
			wantUsage: model.Usage{PromptTokens: 2, CompletionTokens: 4, TotalTokens: 6},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			history := &recordingHistory{}
			modelCalls := 0
			dispatchCalls := 0
			ctrl, err := NewController(ControllerConfig{
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					modelCalls++
					return &model.ChatResponse{
						Choices: []model.Choice{tt.choice},
						Usage:   tt.wantUsage,
						ExecutionIdentity: &model.ExecutionIdentity{
							RequestedModel: "requested",
							SelectedModel:  "selected",
							ProviderID:     "provider",
							ResponseModel:  "reported",
							ResponseID:     "response-id",
						},
					}, nil
				}),
				DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
					dispatchCalls++
					return []ToolOutcome{{Content: "must not execute", Success: true}}, nil
				}),
				History: history,
			})
			if err != nil {
				t.Fatal(err)
			}

			result, runErr := ctrl.Run(t.Context())
			if runErr != nil {
				t.Fatalf("Run: %v", runErr)
			}
			if result.CompletionStatus != CompletionIncomplete || result.FinishReason != FinishReasonInvalidCompletion || result.RequireConclusive() == nil {
				t.Fatalf("result=%+v, want incomplete invalid completion", result)
			}
			if !result.Partial || result.Content != tt.wantText || model.ExtractTextContentOrEmpty(result.Message.Content) != tt.wantText {
				t.Fatalf("result content=%q message=%+v partial=%v, want preserved public prefix %q", result.Content, result.Message, result.Partial, tt.wantText)
			}
			if result.Message.Role != "assistant" || len(result.Message.ToolCalls) != 0 {
				t.Fatalf("result message=%+v, want sanitized assistant message without tool calls", result.Message)
			}
			if result.Message.Reasoning != "" || len(result.Message.ReasoningDetails) != 0 {
				t.Fatalf("result message retained private reasoning fields: %+v", result.Message)
			}
			if modelCalls != 1 || dispatchCalls != 0 || len(history.messages) != 0 {
				t.Fatalf("model_calls=%d dispatch_calls=%d history=%+v, want one request, no tools, no accepted history", modelCalls, dispatchCalls, history.messages)
			}
			if result.Usage != tt.wantUsage {
				t.Fatalf("usage=%+v, want %+v", result.Usage, tt.wantUsage)
			}
			if len(result.ModelExecutions) != 1 || result.ModelExecutions[0].ResponseID != "response-id" {
				t.Fatalf("model executions=%+v, want preserved response identity", result.ModelExecutions)
			}
			assertNoPrivateReasoningLeak(t, result, history, "PRIVATE")
		})
	}
}

func TestController_FinalizationTruncationPreservesDraftThroughExhaustion(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatchCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 1, MaxToolCalls: 10}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 10}), nil
			case 2:
				return &model.ChatResponse{
					Choices: []model.Choice{{Message: model.Message{
						Role:      "assistant",
						Content:   "truncated final draft",
						Reasoning: "PRIVATE_TRUNCATED_FINAL_REASONING",
					}, FinishReason: "length"}},
					Usage: model.Usage{CompletionTokens: 7, TotalTokens: 7},
				}, nil
			case 3:
				return &model.ChatResponse{
					Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: ""}}},
					Usage:   model.Usage{CompletionTokens: 1, TotalTokens: 1},
				}, nil
			default:
				return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{
					Role:             "assistant",
					Content:          "",
					ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", Text: "PRIVATE_EMPTY_FINAL_REASONING"}},
				}}}, Usage: model.Usage{CompletionTokens: 2, TotalTokens: 2}}, nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "confirmed evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want IncompleteTurnError", runErr)
	}
	if modelCalls != 4 {
		t.Fatalf("model_calls=%d, want tool round plus three finalization attempts", modelCalls)
	}
	if dispatchCalls != 1 {
		t.Fatalf("dispatch_calls=%d, want only the original tool round", dispatchCalls)
	}
	if result.CompletionStatus != CompletionIncomplete || !result.Partial || result.Content != "truncated final draft" {
		t.Fatalf("result=%+v, want incomplete preserved truncated draft", result)
	}
	if result.Message.Reasoning != "" || len(result.Message.ReasoningDetails) != 0 {
		t.Fatalf("result message retained private reasoning fields: %+v", result.Message)
	}
	assertNoPrivateReasoningLeak(t, result, history, "PRIVATE")
}

func TestController_FinalizationSuccessClearsStaleTruncatedPartial(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatchCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 1, MaxToolCalls: 10}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 10}), nil
			case 2:
				return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{
					Role:    "assistant",
					Content: "truncated final draft",
				}, FinishReason: "max_output_tokens"}}}, nil
			default:
				return textResponse("complete final synthesis", model.Usage{CompletionTokens: 3, TotalTokens: 3}), nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "confirmed evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if modelCalls != 3 {
		t.Fatalf("model_calls=%d, want tool round, truncated finalization, successful finalization", modelCalls)
	}
	if dispatchCalls != 1 {
		t.Fatalf("dispatch_calls=%d, want only the original tool round", dispatchCalls)
	}
	if result.CompletionStatus != CompletionConclusive || result.Partial || result.Content != "complete final synthesis" {
		t.Fatalf("result=%+v, want conclusive final synthesis without stale partial flag", result)
	}
	if result.Message.Role != "assistant" || model.ExtractTextContentOrEmpty(result.Message.Content) != "complete final synthesis" {
		t.Fatalf("message=%+v, want final synthesis", result.Message)
	}
}
