package agentloop

import (
	"context"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestController_FinalizationRetryCorrectionIsProjectedWithLatestToolEvidence(t *testing.T) {
	history := &recordingHistory{}
	modelCalls := 0
	dispatchCalls := 0
	const contextWindow = 2500
	var retryReq model.ChatRequest
	var retryUseContinuation bool

	ctrl, err := NewController(ControllerConfig{
		Governor:       New(Config{ExactRepeatLimit: 100, OutcomeRepeatLimit: 100, MaxRounds: 50, MaxToolCalls: 1}),
		FinalizeOnStop: true,
		ContextWindow: func(string) int {
			return contextWindow
		},
		Continuation: model.NewContinuationCoordinator(nil, nil, "session-finalization-context"),
		ContinuationEligible: func(string) bool {
			return true
		},
		ProviderID: func(string) string {
			return "provider-test"
		},
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			messages := []model.Message{
				{Role: "system", Content: "system instruction"},
				{Role: "user", Content: "start from the latest inspected evidence"},
			}
			for i := 0; i < 40; i++ {
				messages = append(messages,
					model.Message{Role: "assistant", Content: strings.Repeat("older assistant context ", 20)},
					model.Message{Role: "user", Content: strings.Repeat("older user context ", 20)},
				)
			}
			messages = append(messages, history.messages...)
			return model.ChatRequest{
				Model:     "test-model",
				Messages:  messages,
				MaxTokens: 1,
				Tools:     []map[string]any{{"type": "function", "function": map[string]any{"name": "search_text"}}},
			}, nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, useContinuation bool) (*model.ChatResponse, error) {
			modelCalls++
			switch modelCalls {
			case 1:
				return toolCallResponse("call-1", "search_text", `{"query":"answer"}`, model.Usage{TotalTokens: 1}), nil
			case 2:
				if useContinuation {
					t.Fatalf("first finalization used opaque continuation")
				}
				return toolCallResponse("call-disabled", "search_text", `{"query":"must not dispatch"}`, model.Usage{CompletionTokens: 1, TotalTokens: 1}), nil
			case 3:
				retryReq = req
				retryUseContinuation = useContinuation
				return textResponse("final answer from projected evidence", model.Usage{CompletionTokens: 1, TotalTokens: 1}), nil
			default:
				t.Fatalf("unexpected model call %d", modelCalls)
				return nil, nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "latest grounded evidence", Success: true}}, nil
		}),
		History: history,
	})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	result, runErr := ctrl.Run(t.Context())
	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if result.CompletionStatus != CompletionConclusive || result.Content != "final answer from projected evidence" {
		t.Fatalf("result=%+v", result)
	}
	if modelCalls != 3 || dispatchCalls != 1 {
		t.Fatalf("model_calls=%d dispatch_calls=%d, want 3 and 1", modelCalls, dispatchCalls)
	}
	if retryUseContinuation {
		t.Fatalf("retry finalization used opaque continuation")
	}
	if len(retryReq.Tools) != 0 || retryReq.ToolChoice != "none" || retryReq.ParallelToolCalls != nil {
		t.Fatalf("retry finalization exposed tools: %+v", retryReq)
	}
	if got := model.EstimateRequestTokens(retryReq).Total; got > contextWindow {
		t.Fatalf("retry finalization request estimate=%d, want <= context window %d", got, contextWindow)
	}

	var correctionMessages, finalizationMessages int
	for _, msg := range retryReq.Messages {
		text := model.ExtractTextContentOrEmpty(msg.Content)
		if strings.Contains(text, "Your previous reply was not a usable final answer") {
			correctionMessages++
			if msg.Role != "user" {
				t.Fatalf("correction message role=%q, want user", msg.Role)
			}
			if !strings.Contains(text, "Buckley stopped further tool execution") {
				t.Fatalf("correction was appended outside the projected finalization prompt: %q", text)
			}
		}
		if strings.Contains(text, "Buckley stopped further tool execution") {
			finalizationMessages++
		}
	}
	if correctionMessages != 1 || finalizationMessages != 1 {
		t.Fatalf("correction_messages=%d finalization_messages=%d in %+v, want one projected finalization prompt containing the correction", correctionMessages, finalizationMessages, retryReq.Messages)
	}
	if !requestContainsToolPair(retryReq, "call-1", "latest grounded evidence") {
		t.Fatalf("retry finalization request lost latest tool pair: %+v", retryReq.Messages)
	}
	for _, msg := range history.messages {
		if strings.Contains(model.ExtractTextContentOrEmpty(msg.Content), "Your previous reply was not a usable final answer") {
			t.Fatalf("durable history was mutated with retry correction: %+v", history.messages)
		}
	}
}

func requestContainsToolPair(req model.ChatRequest, callID, toolText string) bool {
	var sawAssistantCall, sawToolResult bool
	for _, msg := range req.Messages {
		if msg.Role == "assistant" {
			for _, call := range msg.ToolCalls {
				if call.ID == callID {
					sawAssistantCall = true
				}
			}
		}
		if msg.Role == "tool" && msg.ToolCallID == callID && strings.Contains(model.ExtractTextContentOrEmpty(msg.Content), toolText) {
			sawToolResult = true
		}
	}
	return sawAssistantCall && sawToolResult
}
