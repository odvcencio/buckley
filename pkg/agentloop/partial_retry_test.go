package agentloop

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestController_PartialRetryKeepsCompletedWorkAndReplays(t *testing.T) {
	withoutTransportBackoff(t)
	ledger, ev, runID := newDurableControllerStores(t)
	history := &recordingHistory{}
	providerCalls, writes, notices := 0, 0, 0
	config := ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model", Messages: append([]model.Message{{Role: "user", Content: "write once then report"}}, history.messages...)}), nil
		},
		CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
			providerCalls++
			switch providerCalls {
			case 1:
				return toolCallResponse("completed-write", "write_file", `{"path":"done.txt"}`, model.Usage{TotalTokens: 1}), nil
			case 2:
				return toolCallResponse("incomplete-write", "write_file", `{"path":"unsafe`, model.Usage{TotalTokens: 2}), io.ErrUnexpectedEOF
			case 3:
				if !strings.Contains(fmt.Sprint(req.Messages), "write completed") || strings.Contains(fmt.Sprint(req.Messages), "incomplete-write") {
					t.Fatalf("retry history=%+v", req.Messages)
				}
				return textResponse("completed safely", model.Usage{TotalTokens: 3}), nil
			default:
				t.Fatalf("unexpected provider replay %d", providerCalls)
				return nil, nil
			}
		}),
		DispatchTools: ToolDispatcherFunc(func(_ context.Context, calls []model.ToolCall) ([]ToolOutcome, error) {
			writes++
			if len(calls) != 1 || calls[0].ID != "completed-write" {
				t.Fatalf("dispatched partial calls: %+v", calls)
			}
			return []ToolOutcome{{Content: "write completed", Success: true}}, nil
		}),
		OnModelRetry: func(notice string) {
			notices++
			if !strings.Contains(notice, "unexpected EOF") || !strings.Contains(notice, "1 incomplete tool calls were not executed") {
				t.Fatal(notice)
			}
		},
		RunLedger: ledger, Evidence: ev, StepJournal: ledger, RunID: runID, SessionID: "durable-test", TaskID: "partial-retry", TurnID: "partial-retry-turn",
	}
	for _, replay := range []bool{false, true} {
		history = &recordingHistory{}
		config.History = history
		controller, err := NewController(config)
		if err != nil {
			t.Fatal(err)
		}
		result, err := controller.Run(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if result.Partial || result.CompletionStatus != CompletionConclusive || result.Content != "completed safely" || result.Usage.TotalTokens != 6 {
			t.Fatalf("replay=%v result=%+v", replay, result)
		}
		if providerCalls != 3 || writes != 1 || len(history.messages) != 2 {
			t.Fatalf("replay=%v calls=%d writes=%d history=%+v", replay, providerCalls, writes, history.messages)
		}
	}
	if notices != 2 {
		t.Fatalf("recovery notices=%d", notices)
	}
}

func TestController_PartialRetryIsBoundedAndRedacted(t *testing.T) {
	withoutTransportBackoff(t)
	secret := "sk-or-v1-" + strings.Repeat("a", 64)
	err := &model.APIError{StatusCode: 503, Message: "upstream overloaded; credential=" + secret, Retryable: true}
	calls, notices, dispatches := 0, 0, 0
	controller, buildErr := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return testToolRequest(model.ChatRequest{Model: "test-model"}), nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			calls++
			return toolCallResponse("never-run", "write_file", `{}`, model.Usage{TotalTokens: 1}), err
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) { dispatches++; return nil, nil }),
		OnModelRetry: func(notice string) {
			notices++
			if strings.Contains(notice, secret) || !strings.Contains(notice, "overloaded") {
				t.Fatal(notice)
			}
		},
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	result, runErr := controller.Run(t.Context())
	if runErr == nil || !result.Partial || calls != maxTransportRetries+1 || notices != maxTransportRetries || dispatches != 0 {
		t.Fatalf("result=%+v error=%v calls=%d notices=%d dispatches=%d", result, runErr, calls, notices, dispatches)
	}
	notice := PresentIncompleteResult(runErr)
	if !strings.Contains(notice.Message, "Provider error:") || !strings.Contains(notice.Message, "HTTP 503") || strings.Contains(notice.Message, secret) {
		t.Fatal(notice.Message)
	}
}

func TestController_PartialCancellationDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	controller, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			calls++
			cancel()
			return textResponse("partial", model.Usage{}), context.Canceled
		}),
		OnModelRetry: func(string) { t.Error("cancelled turn retried") },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Run(ctx)
	if !errors.Is(err, context.Canceled) || calls != 1 || !result.Partial {
		t.Fatalf("result=%+v error=%v calls=%d", result, err, calls)
	}
}

func TestTransientPartialResponseError_Classification(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		err   error
		retry bool
	}{
		{"stream EOF", io.ErrUnexpectedEOF, true},
		{"wrapped EOF", fmt.Errorf("read stream: %w", io.EOF), true},
		{"request timeout", context.DeadlineExceeded, true},
		{"cancellation", context.Canceled, false},
		{"unauthorized", &model.APIError{StatusCode: 401}, false},
		{"transient upstream", &model.APIError{StatusCode: 503, Retryable: true}, true},
		{"response cap", model.ErrProviderStreamTooLarge, false},
		{"unknown permanent error", errors.New("invalid provider data"), false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if got := transientPartialResponseError(scenario.err); got != scenario.retry {
				t.Fatalf("retry=%v want %v", got, scenario.retry)
			}
		})
	}
}
