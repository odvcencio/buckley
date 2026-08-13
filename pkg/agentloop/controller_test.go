package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

// recordingHistory captures every message Controller appends, in order.
type recordingHistory struct {
	messages []model.Message
}

func (h *recordingHistory) Append(msg model.Message) {
	h.messages = append(h.messages, msg)
}

func textResponse(content string, usage model.Usage) *model.ChatResponse {
	return &model.ChatResponse{
		Choices: []model.Choice{{Message: model.Message{Role: "assistant", Content: content}}},
		Usage:   usage,
	}
}

func toolCallResponse(callID, toolName, args string, usage model.Usage) *model.ChatResponse {
	return &model.ChatResponse{
		Choices: []model.Choice{{
			Message: model.Message{
				Role: "assistant",
				ToolCalls: []model.ToolCall{{
					ID:       callID,
					Type:     "function",
					Function: model.FunctionCall{Name: toolName, Arguments: args},
				}},
			},
		}},
		Usage: usage,
	}
}

func TestController_NormalCompletionNoToolCalls(t *testing.T) {
	history := &recordingHistory{}
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			return textResponse("hello there", model.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}), nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinishReason != "" {
		t.Fatalf("FinishReason = %q, want normal completion", result.FinishReason)
	}
	if got, ok := result.Message.Content.(string); !ok || got != "hello there" {
		t.Fatalf("Message.Content = %#v, want %q", result.Message.Content, "hello there")
	}
	if result.Usage.TotalTokens != 5 {
		t.Fatalf("Usage.TotalTokens = %d, want 5", result.Usage.TotalTokens)
	}
	if result.Rounds != 1 {
		t.Fatalf("Rounds = %d, want 1", result.Rounds)
	}
	if len(history.messages) != 1 || history.messages[0].Role != "assistant" {
		t.Fatalf("expected exactly the final assistant message appended, got %+v", history.messages)
	}
}

func TestController_EmptyOrTruncatedTerminalCandidateIsIncomplete(t *testing.T) {
	for _, tt := range []struct {
		name   string
		choice model.Choice
	}{
		{name: "empty", choice: model.Choice{Message: model.Message{Role: "assistant"}}},
		{name: "truncated", choice: model.Choice{Message: model.Message{Role: "assistant", Content: "partial"}, FinishReason: "length"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctrl, err := NewController(ControllerConfig{
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return model.ChatRequest{Model: "test-model"}, nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					return &model.ChatResponse{Choices: []model.Choice{tt.choice}}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := ctrl.Run(t.Context())
			if runErr != nil {
				t.Fatalf("Run: %v", runErr)
			}
			if result.CompletionStatus != CompletionIncomplete || result.FinishReason != FinishReasonInvalidCompletion || result.RequireConclusive() == nil {
				t.Fatalf("result = %+v, want explicit incomplete candidate", result)
			}
		})
	}
}

func TestController_TruncatedToolCallChoiceNeverDispatches(t *testing.T) {
	history := &recordingHistory{}
	dispatchCalls := 0
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			response := toolCallResponse("call-truncated", "write_file", `{}`, model.Usage{TotalTokens: 10})
			response.Choices[0].FinishReason = "max_tokens"
			return response, nil
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
	if dispatchCalls != 0 || len(history.messages) != 0 {
		t.Fatalf("dispatch_calls=%d history=%+v", dispatchCalls, history.messages)
	}
	if result.FinishReason != FinishReasonInvalidCompletion || result.CompletionStatus != CompletionIncomplete || !strings.Contains(result.Termination.Reason, "truncated") {
		t.Fatalf("result=%+v", result)
	}
}

func TestController_DispatchesToolsBackfillsIDsAndSumsUsage(t *testing.T) {
	history := &recordingHistory{}
	round := 0
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, r int) (model.ChatRequest, error) {
			round++
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			if round == 1 {
				// No ID: Controller must backfill it.
				return toolCallResponse("", "search_text", `{"query":"foo"}`, model.Usage{PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14}), nil
			}
			return textResponse("done", model.Usage{PromptTokens: 20, CompletionTokens: 6, TotalTokens: 26}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			if len(calls) != 1 || calls[0].ID == "" {
				t.Fatalf("expected one backfilled tool call, got %+v", calls)
			}
			return []ToolOutcome{{Content: "ok: foo", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinishReason != "" {
		t.Fatalf("FinishReason = %q, want normal completion", result.FinishReason)
	}
	if result.Rounds != 2 {
		t.Fatalf("Rounds = %d, want 2", result.Rounds)
	}
	if result.ToolCalls != 1 {
		t.Fatalf("ToolCalls = %d, want 1", result.ToolCalls)
	}
	if result.Usage.TotalTokens != 40 {
		t.Fatalf("Usage.TotalTokens = %d, want 40 (14 + 26)", result.Usage.TotalTokens)
	}
	// History: round 1 assistant tool-call message, round 1 tool result, round 2 final assistant message.
	if len(history.messages) != 3 {
		t.Fatalf("expected 3 appended messages, got %d: %+v", len(history.messages), history.messages)
	}
	if len(history.messages[0].ToolCalls) != 1 || history.messages[0].ToolCalls[0].ID == "" {
		t.Fatalf("expected the assistant tool-call message with a backfilled ID, got %+v", history.messages[0])
	}
	if history.messages[1].Role != "tool" || history.messages[1].Content != "ok: foo" {
		t.Fatalf("expected the tool result message, got %+v", history.messages[1])
	}
	if history.messages[1].ToolCallID != history.messages[0].ToolCalls[0].ID {
		t.Fatalf("tool result ToolCallID = %q, want %q", history.messages[1].ToolCallID, history.messages[0].ToolCalls[0].ID)
	}
}

func TestController_GovernorStopsOnExactRepeat(t *testing.T) {
	calls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor: New(Config{ExactRepeatLimit: 3, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 100}),
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			calls++
			return toolCallResponse("call-1", "search_text", `{"query":"foo"}`, model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "same result", Success: true}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinishReason != FinishReasonLoopGuard {
		t.Fatalf("FinishReason = %q, want %q", result.FinishReason, FinishReasonLoopGuard)
	}
	if result.GuardDecision.Kind != "exact_repeat" {
		t.Fatalf("GuardDecision.Kind = %q, want exact_repeat", result.GuardDecision.Kind)
	}
	if calls != 3 {
		t.Fatalf("model calls = %d, want exactly 3 (the exact-repeat limit)", calls)
	}
	if result.Content == "" {
		t.Fatalf("expected a caller-facing stop message")
	}
}

func TestController_StepCapStopsBeforeGovernorDefault(t *testing.T) {
	calls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor: New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 100}),
		StepCap:  1,
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			calls++
			return toolCallResponse("call-1", "search_text", `{"query":"foo"}`, model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "distinct-" + calls[0].Function.Arguments, Success: true}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinishReason != FinishReasonStepCap {
		t.Fatalf("FinishReason = %q, want %q", result.FinishReason, FinishReasonStepCap)
	}
	if calls != 1 {
		t.Fatalf("model calls = %d, want exactly 1 (StepCap stops before round 2's model call)", calls)
	}
}

func TestController_GuardFinalizesFromToolEvidenceWithToolsDisabled(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatchCalls := 0
	parallel := true
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 1}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			messages := []model.Message{{Role: "user", Content: "find the answer"}}
			messages = append(messages, history.messages...)
			return model.ChatRequest{
				Model:             "test-model",
				Messages:          messages,
				Tools:             []map[string]any{{"type": "function"}},
				ToolChoice:        "auto",
				ParallelToolCalls: &parallel,
			}, nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{"query":"answer"}`, model.Usage{TotalTokens: 5}), nil
			}
			if len(req.Tools) != 0 || req.ToolChoice != "none" || req.ParallelToolCalls != nil {
				t.Fatalf("finalization still exposed tools: %+v", req)
			}
			var sawToolEvidence, sawFinalizationPrompt bool
			for _, message := range req.Messages {
				if message.Role == "tool" && message.ToolCallID == "call-1" && strings.Contains(model.ExtractTextContentOrEmpty(message.Content), "grounded evidence") {
					sawToolEvidence = true
				}
			}
			if final := req.Messages[len(req.Messages)-1]; final.Role == "user" && strings.Contains(model.ExtractTextContentOrEmpty(final.Content), "Do not call tools") {
				sawFinalizationPrompt = true
			}
			if !sawToolEvidence || !sawFinalizationPrompt {
				t.Fatalf("finalization request missing evidence=%v or prompt=%v: %+v", sawToolEvidence, sawFinalizationPrompt, req.Messages)
			}
			return textResponse("grounded final answer", model.Usage{TotalTokens: 7}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "grounded evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CompletionStatus != CompletionConclusive || result.RequireConclusive() != nil {
		t.Fatalf("completion = %q, termination = %+v", result.CompletionStatus, result.Termination)
	}
	if result.FinishReason != FinishReasonLoopGuard || result.Termination.Kind != "tool_call_limit" || !result.Termination.FinalizationAttempted || result.Termination.FinalizationError != "" {
		t.Fatalf("unexpected termination: finish=%q termination=%+v", result.FinishReason, result.Termination)
	}
	if got := model.ExtractTextContentOrEmpty(result.Message.Content); got != "grounded final answer" {
		t.Fatalf("final message = %q", got)
	}
	if result.Usage.TotalTokens != 12 || modelCalls != 2 || dispatchCalls != 1 {
		t.Fatalf("usage=%d model_calls=%d dispatch_calls=%d", result.Usage.TotalTokens, modelCalls, dispatchCalls)
	}
}

func TestController_FinalizationFailureIsExplicitlyIncomplete(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 1}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{}), nil
			}
			return nil, errors.New("provider unavailable during synthesis")
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence survives", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Run error = %v, want IncompleteTurnError", err)
	}
	if result == nil || result.CompletionStatus != CompletionIncomplete || !result.Termination.FinalizationAttempted {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Termination.FinalizationError, "provider unavailable") || result.Message.Content != nil {
		t.Fatalf("termination = %+v message=%+v", result.Termination, result.Message)
	}
	if len(history.messages) != 2 || history.messages[1].Role != "tool" || !strings.Contains(model.ExtractTextContentOrEmpty(history.messages[1].Content), "evidence survives") {
		t.Fatalf("tool evidence was not preserved in history: %+v", history.messages)
	}
}

func TestController_EmptyChoicesAfterToolEvidenceUsesFinalization(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}, nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 2}), nil
			case 2:
				return &model.ChatResponse{Usage: model.Usage{TotalTokens: 1}}, nil
			default:
				if len(req.Tools) != 0 || req.ToolChoice != "none" || req.ParallelToolCalls != nil {
					t.Fatalf("finalization request retained tools: %+v", req)
				}
				return textResponse("answer from preserved evidence", model.Usage{TotalTokens: 3}), nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "grounded evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CompletionStatus != CompletionConclusive || !result.Termination.FinalizationAttempted || modelCalls != 3 {
		t.Fatalf("result=%+v model_calls=%d", result, modelCalls)
	}
	if got := model.ExtractTextContentOrEmpty(result.Message.Content); got != "answer from preserved evidence" {
		t.Fatalf("final answer = %q", got)
	}
}

func TestController_ExplicitCostCeilingDoesNotSpendAgainAfterExhaustion(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 50}),
		FinalizeOnStop: true,
		MaxCostUSD:     1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", MaxTokens: 100, Messages: append([]model.Message(nil), history.messages...)}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 1000}), nil
			}
			return textResponse("cost-bounded answer", model.Usage{TotalTokens: 2000}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	var incomplete *IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Run error = %v, want incomplete after cost exhaustion", err)
	}
	if result.CompletionStatus != CompletionIncomplete || result.Termination.Kind != "cost_limit" || result.CostUSD != 1 || modelCalls != 1 {
		t.Fatalf("result=%+v model_calls=%d", result, modelCalls)
	}
}

func TestController_CostCeilingPersistsAcrossRunContinuations(t *testing.T) {
	modelCalls := 0
	request := model.ChatRequest{
		Model:     "test-model",
		MaxTokens: 10,
		Messages:  []model.Message{{Role: "user", Content: strings.Repeat("prompt", 20)}},
	}
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return request, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse("first answer needs a nudge", model.Usage{TotalTokens: 600}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := ctrl.Run(t.Context())
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if first.CostUSD != 0.6 || first.ModelRequests != 1 || first.Usage.TotalTokens != 600 || modelCalls != 1 {
		t.Fatalf("first result=%+v model_calls=%d", first, modelCalls)
	}

	second, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("second Run error = %v, want incomplete cost stop", runErr)
	}
	if modelCalls != 1 {
		t.Fatalf("model calls = %d, second continuation dispatched past the all-in ceiling", modelCalls)
	}
	if second.CostUSD != 0.6 || second.ModelRequests != 1 || second.Usage.TotalTokens != 600 {
		t.Fatalf("second cumulative result=%+v", second)
	}
	if second.Termination.Kind != "cost_limit" {
		t.Fatalf("second termination=%+v", second.Termination)
	}
}

func TestController_ModelRequestLimitPersistsAcrossRunContinuations(t *testing.T) {
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		MaxModelRequests: 1,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse("first answer needs a nudge", model.Usage{TotalTokens: 7}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	first, err := ctrl.Run(t.Context())
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if first.ModelRequests != 1 || first.Usage.TotalTokens != 7 || modelCalls != 1 {
		t.Fatalf("first result=%+v model_calls=%d", first, modelCalls)
	}

	second, err := ctrl.Run(t.Context())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if modelCalls != 1 || second.ModelRequests != 1 || second.Usage.TotalTokens != 7 {
		t.Fatalf("second result=%+v model_calls=%d", second, modelCalls)
	}
	if second.Termination.Kind != "model_request_limit" || second.CompletionStatus != CompletionIncomplete {
		t.Fatalf("second termination=%+v completion=%q", second.Termination, second.CompletionStatus)
	}
}

func TestController_OverCeilingFirstResponseCannotDispatchTools(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatchCalls := 0
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", MaxTokens: 100}, nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			modelCalls++
			if req.MaxTokens != 100 {
				t.Fatalf("MaxTokens = %d, want existing affordable allowance 100", req.MaxTokens)
			}
			return toolCallResponse("call-1", "write_file", `{}`, model.Usage{TotalTokens: 2000}), nil
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
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want explicit incomplete", runErr)
	}
	if modelCalls != 1 || dispatchCalls != 0 || len(history.messages) != 0 {
		t.Fatalf("model_calls=%d dispatch_calls=%d history=%+v", modelCalls, dispatchCalls, history.messages)
	}
	if result.CompletionStatus != CompletionIncomplete || result.Termination.Kind != "cost_limit" || result.CostUSD != 2 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Termination.Reason, "content and tool calls were rejected") {
		t.Fatalf("termination reason = %q", result.Termination.Reason)
	}
}

func TestController_OverCeilingFinalizationResponseIsRejected(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatchCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 1, MaxToolCalls: 10}),
		FinalizeOnStop: true,
		MaxCostUSD:     1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 10_000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{
				Model:     "test-model",
				MaxTokens: 100,
				Messages:  append([]model.Message(nil), history.messages...),
			}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 4000}), nil
			}
			return textResponse("must not be accepted", model.Usage{TotalTokens: 7000}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "preserved evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want explicit incomplete", runErr)
	}
	if modelCalls != 2 || dispatchCalls != 1 {
		t.Fatalf("model_calls=%d dispatch_calls=%d", modelCalls, dispatchCalls)
	}
	if len(history.messages) != 2 {
		t.Fatalf("history has %d messages, want only assistant tool call and tool result: %+v", len(history.messages), history.messages)
	}
	if result.CompletionStatus != CompletionIncomplete || !result.Termination.FinalizationAttempted || result.CostUSD != 1.1 {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Termination.FinalizationError, "exceeding the explicit") {
		t.Fatalf("finalization error = %q", result.Termination.FinalizationError)
	}
}

func TestController_CostReservationBoundsUnknownAndExplicitOutputAllowances(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		request               model.ChatRequest
		maxCostUSD            float64
		wantMaxTokens         int
		wantCompletionClamped bool
	}{
		{
			name:          "provider default becomes explicit conservative allowance",
			request:       model.ChatRequest{Model: "test-model"},
			maxCostUSD:    10,
			wantMaxTokens: fallbackCostBoundedOutputTokens,
		},
		{
			name:                  "existing completion field is clamped without adding conflicting max tokens",
			request:               model.ChatRequest{Model: "test-model", MaxCompletionTokens: 2000},
			maxCostUSD:            1,
			wantCompletionClamped: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var captured model.ChatRequest
			ctrl, err := NewController(ControllerConfig{
				MaxCostUSD: tt.maxCostUSD,
				CostForUsage: func(usage model.Usage) (float64, error) {
					return float64(usage.TotalTokens) / 1000, nil
				},
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return tt.request, nil
				},
				CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
					captured = req
					return textResponse("done", model.Usage{TotalTokens: 1}), nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ctrl.Run(t.Context()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if tt.wantMaxTokens > 0 && captured.MaxTokens != tt.wantMaxTokens {
				t.Fatalf("MaxTokens = %d, want %d", captured.MaxTokens, tt.wantMaxTokens)
			}
			if tt.wantCompletionClamped {
				if captured.MaxTokens != 0 || captured.MaxCompletionTokens <= 0 || captured.MaxCompletionTokens >= tt.request.MaxCompletionTokens {
					t.Fatalf("captured request = %+v, want only clamped MaxCompletionTokens", captured)
				}
				envelope := model.EstimateRequestTokens(captured).Total + captured.MaxCompletionTokens
				if envelope > 1_000 {
					t.Fatalf("reserved envelope = %d tokens, exceeds 1,000-token ceiling", envelope)
				}
			}
		})
	}
}

func TestController_CostCeilingRejectsUnpriceableImageInputBeforeDispatch(t *testing.T) {
	modelCalls := 0
	priceCalls := 0
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(model.Usage) (float64, error) {
			priceCalls++
			return 0, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{
				Model: "vision-model",
				Messages: []model.Message{{
					Role: "user",
					Content: []model.ContentPart{
						{Type: "text", Text: "inspect this"},
						{Type: "image_url", ImageURL: &model.ImageURL{URL: "https://example.invalid/large.png"}},
					},
				}},
			}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse("must not run", model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error=%v, want incomplete", runErr)
	}
	if modelCalls != 0 || priceCalls != 0 {
		t.Fatalf("model_calls=%d price_calls=%d, want no dispatch or pricing", modelCalls, priceCalls)
	}
	if result.Termination.Kind != "cost_limit" || !strings.Contains(result.Termination.Reason, "image-token estimator") {
		t.Fatalf("result=%+v", result)
	}
}

func TestController_CostNormalizerObservesFinalAffordableAllowance(t *testing.T) {
	var normalized []int
	var captured model.ChatRequest
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		NormalizeCostBoundedRequest: func(req model.ChatRequest) (model.ChatRequest, error) {
			normalized = append(normalized, requestOutputAllowance(req, 0, 0))
			return req, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", MaxTokens: 2000}, nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			captured = req
			return textResponse("done", model.Usage{TotalTokens: 1}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(t.Context()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(normalized) < 4 || len(normalized)%2 != 0 || normalized[0] != 2000 {
		t.Fatalf("normalizer allowances = %v, want requested then lower affordable candidates", normalized)
	}
	sawDispatchedAllowance := false
	for i := 0; i < len(normalized); i += 2 {
		if normalized[i] != normalized[i+1] {
			t.Fatalf("normalizer idempotence pair %d = %d, %d", i/2, normalized[i], normalized[i+1])
		}
		if normalized[i] == captured.MaxTokens {
			sawDispatchedAllowance = true
		}
	}
	if captured.MaxTokens >= normalized[0] || !sawDispatchedAllowance {
		t.Fatalf("dispatched MaxTokens = %d, normalized candidates = %v", captured.MaxTokens, normalized)
	}
}

func TestNewController_RejectsNonFiniteMaxCostCeilings(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value float64
	}{
		{name: "NaN", value: math.NaN()},
		{name: "positive infinity", value: math.Inf(1)},
		{name: "negative infinity", value: math.Inf(-1)},
		{name: "negative", value: -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewController(ControllerConfig{
				MaxCostUSD: tt.value,
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return model.ChatRequest{}, nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					return textResponse("unused", model.Usage{}), nil
				}),
			})
			if err == nil || !strings.Contains(err.Error(), "finite and non-negative") {
				t.Fatalf("NewController error = %v", err)
			}
		})
	}
}

func TestController_CostCeilingRejectsUntypedAndRawImageContent(t *testing.T) {
	raw := json.RawMessage(`{"type":"input_image","image_url":"data:image/png;base64,AA=="}`)
	for _, tt := range []struct {
		name    string
		content any
	}{
		{
			name: "concrete map slice",
			content: []map[string]any{
				{"type": "text", "text": "inspect"},
				{"type": "image_url", "image_url": map[string]any{"url": "https://example.invalid/image.png"}},
			},
		},
		{name: "raw JSON pointer", content: &raw},
	} {
		t.Run(tt.name, func(t *testing.T) {
			modelCalls := 0
			priceCalls := 0
			ctrl, err := NewController(ControllerConfig{
				MaxCostUSD: 1,
				CostForUsage: func(model.Usage) (float64, error) {
					priceCalls++
					return 0, nil
				},
				BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
					return model.ChatRequest{Model: "vision-model", Messages: []model.Message{{Role: "user", Content: tt.content}}}, nil
				},
				CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
					modelCalls++
					return textResponse("must not run", model.Usage{}), nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := ctrl.Run(t.Context())
			var incomplete *IncompleteTurnError
			if !errors.As(runErr, &incomplete) {
				t.Fatalf("Run error = %v, want incomplete", runErr)
			}
			if modelCalls != 0 || priceCalls != 0 || result.Termination.Kind != "cost_limit" {
				t.Fatalf("result=%+v model_calls=%d price_calls=%d", result, modelCalls, priceCalls)
			}
		})
	}
}

func TestContentContainsUnpricedImage_CyclicNonImageContentIsSafe(t *testing.T) {
	content := map[string]any{"type": "text", "text": "plain"}
	content["self"] = content
	if contentContainsUnpricedImage(content) {
		t.Fatal("cyclic text-only content reported an image")
	}
}

func TestController_CostNormalizerBytesAreIncludedBeforeAdmission(t *testing.T) {
	base := model.ChatRequest{Model: "test-model", MaxTokens: 10, Messages: []model.Message{{Role: "user", Content: "small"}}}
	rawBound, err := conservativeRequestInputTokenBound(base)
	if err != nil {
		t.Fatal(err)
	}
	maxCost := float64(rawBound+base.MaxTokens+1) / 1000
	modelCalls := 0
	maxPricedPrompt := 0
	normalizeCalls := 0
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: maxCost,
		CostForUsage: func(usage model.Usage) (float64, error) {
			maxPricedPrompt = max(maxPricedPrompt, usage.PromptTokens)
			return float64(usage.TotalTokens) / 1000, nil
		},
		NormalizeCostBoundedRequest: func(req model.ChatRequest) (model.ChatRequest, error) {
			normalizeCalls++
			provider := make(map[string]any, len(req.Provider)+1)
			for key, value := range req.Provider {
				provider[key] = value
			}
			provider["wire_padding"] = strings.Repeat("x", 2048)
			req.Provider = provider
			return req, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return base, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse("must not run", model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want incomplete", runErr)
	}
	if modelCalls != 0 || normalizeCalls < 2 || normalizeCalls%2 != 0 {
		t.Fatalf("model_calls=%d normalize_calls=%d", modelCalls, normalizeCalls)
	}
	if maxPricedPrompt <= rawBound+1500 {
		t.Fatalf("priced prompt bound = %d, raw bound = %d; provider-added wire bytes were not reserved", maxPricedPrompt, rawBound)
	}
	if result.Termination.Kind != "cost_limit" {
		t.Fatalf("result=%+v", result)
	}
}

func TestController_CostNormalizerMustBeIdempotent(t *testing.T) {
	modelCalls := 0
	priceCalls := 0
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(model.Usage) (float64, error) {
			priceCalls++
			return 0, nil
		},
		NormalizeCostBoundedRequest: func(req model.ChatRequest) (model.ChatRequest, error) {
			req.Transforms = append(req.Transforms, "changes-every-pass")
			return req, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", MaxTokens: 10}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			return textResponse("must not run", model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want incomplete", runErr)
	}
	if modelCalls != 0 || priceCalls != 0 || !strings.Contains(result.Termination.Reason, "not idempotent") {
		t.Fatalf("result=%+v model_calls=%d price_calls=%d", result, modelCalls, priceCalls)
	}
}

func TestController_CostAdmissionUsesNormalizedModelContext(t *testing.T) {
	var contextModels []string
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1_000_000, nil
		},
		NormalizeCostBoundedRequest: func(req model.ChatRequest) (model.ChatRequest, error) {
			req.Model = "wire-model"
			return req, nil
		},
		ContextWindow: func(modelID string) int {
			contextModels = append(contextModels, modelID)
			if modelID == "wire-model" {
				return 4096
			}
			return 64
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			return textResponse("unused", model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	bounded, reservation, err := ctrl.reserveModelRequest(model.ChatRequest{Model: "alias-model"}, 0)
	if err != nil {
		t.Fatalf("reserveModelRequest: %v", err)
	}
	if bounded.Model != "wire-model" || reservation.outputTokens <= 0 || reservation.outputTokens >= fallbackCostBoundedOutputTokens {
		t.Fatalf("bounded=%+v reservation=%+v", bounded, reservation)
	}
	if len(contextModels) == 0 {
		t.Fatal("ContextWindow was not consulted")
	}
	for _, modelID := range contextModels {
		if modelID != "wire-model" {
			t.Fatalf("ContextWindow called with pre-normalized model %q: %v", modelID, contextModels)
		}
	}
}

func TestController_ConservativeInputBoundIsNotCappedByContextCatalog(t *testing.T) {
	payload := strings.Repeat("large-prompt-byte-", 512)
	maxPricedPrompt := 0
	ctrl, err := NewController(ControllerConfig{
		MaxCostUSD: 1,
		CostForUsage: func(usage model.Usage) (float64, error) {
			maxPricedPrompt = max(maxPricedPrompt, usage.PromptTokens)
			return float64(usage.TotalTokens) / 1_000_000, nil
		},
		ContextWindow: func(string) int { return 32 },
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			return textResponse("unused", model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, reservation, err := ctrl.reserveModelRequest(model.ChatRequest{
		Model:     "test-model",
		MaxTokens: 1,
		Messages:  []model.Message{{Role: "user", Content: payload}},
	}, 0)
	if err != nil {
		t.Fatalf("reserveModelRequest: %v", err)
	}
	if reservation.inputTokens <= 32 || maxPricedPrompt <= 32 {
		t.Fatalf("reservation=%+v max_priced_prompt=%d, want bound above stale 32-token context", reservation, maxPricedPrompt)
	}
}

func TestController_FinalizationEmptyChoicesStillAccountsUsageAndCost(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 1, MaxToolCalls: 10}),
		FinalizeOnStop: true,
		MaxCostUSD:     10,
		CostForUsage: func(usage model.Usage) (float64, error) {
			return float64(usage.TotalTokens) / 1000, nil
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", MaxTokens: 10, Messages: append([]model.Message(nil), history.messages...)}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{TotalTokens: 100}), nil
			}
			return &model.ChatResponse{Usage: model.Usage{TotalTokens: 200}}, nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, runErr := ctrl.Run(t.Context())
	var incomplete *IncompleteTurnError
	if !errors.As(runErr, &incomplete) {
		t.Fatalf("Run error = %v, want incomplete", runErr)
	}
	if result.Usage.TotalTokens != 300 || math.Abs(result.CostUSD-0.3) > 1e-12 || modelCalls != 2 {
		t.Fatalf("result=%+v model_calls=%d", result, modelCalls)
	}
	if !strings.Contains(result.Termination.FinalizationError, "no response choices") {
		t.Fatalf("finalization error = %q", result.Termination.FinalizationError)
	}
}

func TestController_ExplicitModelRequestLimitIncludesFinalSynthesis(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:         New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 50}),
		FinalizeOnStop:   true,
		MaxModelRequests: 2,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls == 1 {
				return toolCallResponse("call-1", "search_text", `{}`, model.Usage{}), nil
			}
			return textResponse("final within request ceiling", model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: "evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ctrl.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CompletionStatus != CompletionConclusive || result.ModelRequests != 2 || modelCalls != 2 || result.Termination.Kind != "model_request_limit" {
		t.Fatalf("result=%+v model_calls=%d", result, modelCalls)
	}
}

func TestController_ParallelBatchCannotOvershootToolCeiling(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatched := 0
	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 2}),
		FinalizeOnStop: true,
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model", Messages: append([]model.Message(nil), history.messages...)}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			modelCalls++
			if modelCalls > 1 {
				return textResponse("answer from two admitted calls", model.Usage{}), nil
			}
			calls := make([]model.ToolCall, 4)
			for i := range calls {
				calls[i] = model.ToolCall{
					ID:   fmt.Sprintf("call-%d", i+1),
					Type: "function",
					Function: model.FunctionCall{
						Name:      "read_file",
						Arguments: fmt.Sprintf(`{"path":"file-%d"}`, i+1),
					},
				}
			}
			return &model.ChatResponse{Choices: []model.Choice{{Message: model.Message{Role: "assistant", ToolCalls: calls}}}}, nil
		}),
		DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			dispatched += len(calls)
			outcomes := make([]ToolOutcome, len(calls))
			for i := range outcomes {
				outcomes[i] = ToolOutcome{Content: "evidence", Success: true}
			}
			return outcomes, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if dispatched != 2 || result.ToolCalls != 2 {
		t.Fatalf("dispatched=%d result.ToolCalls=%d, want exact ceiling 2", dispatched, result.ToolCalls)
	}
	if len(history.messages) == 0 || len(history.messages[0].ToolCalls) != 2 {
		t.Fatalf("assistant transcript advertised calls that did not run: %+v", history.messages)
	}
	if result.CompletionStatus != CompletionConclusive || result.Termination.Kind != "tool_call_limit" {
		t.Fatalf("result = %+v", result)
	}
}

func TestController_EmptyChoicesReportsFinishReasonWithoutError(t *testing.T) {
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			return &model.ChatResponse{}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, err := ctrl.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinishReason != FinishReasonEmptyChoices {
		t.Fatalf("FinishReason = %q, want %q", result.FinishReason, FinishReasonEmptyChoices)
	}
}

func TestController_CallModelErrorPropagates(t *testing.T) {
	wantErr := errors.New("boom")
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			return nil, wantErr
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	_, err = ctrl.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
}

func TestController_MissingToolDispatcherErrors(t *testing.T) {
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			return toolCallResponse("call-1", "search_text", `{}`, model.Usage{}), nil
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	if _, err := ctrl.Run(context.Background()); err == nil {
		t.Fatalf("expected an error when the model requests tools with no ToolDispatcher configured")
	}
}

func TestController_ToolDispatcherErrorAbortsTurn(t *testing.T) {
	wantErr := errors.New("approval wait cancelled")
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			return toolCallResponse("call-1", "run_shell", `{}`, model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			return nil, wantErr
		}),
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	_, err = ctrl.Run(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
}

func TestController_RecordsRunLedgerEvents(t *testing.T) {
	store, err := runledger.New(t.TempDir() + "/ledger.db")
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	run, err := store.StartRun(ctx, runledger.AgentRun{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(ctx context.Context, round int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			return textResponse("done", model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}), nil
		}),
		RunLedger: store,
		RunID:     run.RunID,
		SessionID: "sess-1",
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	if _, err := ctrl.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events, err := store.ListEvents(ctx, runledger.EventQuery{RunID: run.RunID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var sawStarted, sawCompleted bool
	for _, e := range events {
		switch e.Type {
		case runledger.EventModelRequestStarted:
			sawStarted = true
		case runledger.EventModelRequestCompleted:
			sawCompleted = true
		}
	}
	if !sawStarted || !sawCompleted {
		t.Fatalf("expected model.request_started and model.request_completed events, got %+v", events)
	}
}

func TestBackfillToolCallIDs(t *testing.T) {
	calls := []model.ToolCall{{ID: "kept"}, {ID: ""}, {ID: ""}}
	got := BackfillToolCallIDs(calls)
	if got[0].ID != "kept" {
		t.Fatalf("expected an existing ID to survive, got %q", got[0].ID)
	}
	if got[1].ID != "tool-2" || got[2].ID != "tool-3" {
		t.Fatalf("expected positional backfilled IDs, got %q, %q", got[1].ID, got[2].ID)
	}
}

func TestGuardStopMessageDefaultsWhenReasonEmpty(t *testing.T) {
	msg := GuardStopMessage("")
	if msg == "" {
		t.Fatalf("expected a non-empty default message")
	}
}

func TestProjectForContinuation_NilCoordinatorIsPassthroughPin(t *testing.T) {
	req := model.ChatRequest{
		Model:    "test-model",
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	}
	got := ProjectForContinuation(req, 0, nil, "", true)
	if len(got.Messages) != 1 || got.Messages[0].Content != "hi" {
		t.Fatalf("expected the message to survive an unpinned projection pass, got %+v", got.Messages)
	}
}
