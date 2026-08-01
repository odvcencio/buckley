package agentloop

import (
	"context"
	"errors"
	"testing"

	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/runledger"
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
