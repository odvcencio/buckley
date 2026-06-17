package chatcheck

import (
	"context"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

type fakeClient struct {
	responses []model.ChatResponse
	errs      []error
	requests  []model.ChatRequest
}

func (f *fakeClient) ChatCompletion(_ context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	f.requests = append(f.requests, req)
	idx := len(f.requests) - 1
	if idx < len(f.errs) && f.errs[idx] != nil {
		return nil, f.errs[idx]
	}
	if idx >= len(f.responses) {
		return nil, errors.New("unexpected request")
	}
	resp := f.responses[idx]
	return &resp, nil
}

func TestRunnerRunMultiTurn(t *testing.T) {
	client := &fakeClient{responses: []model.ChatResponse{
		response("test-model", "BUCKLEY_CHAT_CHECK_ONE"),
		response("test-model", "previous token was BUCKLEY_CHAT_CHECK_ONE; BUCKLEY_CHAT_CHECK_TWO"),
	}}
	runner := Runner{Client: client}

	result, err := runner.Run(context.Background(), DefaultScenario("test-model"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Turns) != 2 {
		t.Fatalf("turns=%d want 2", len(result.Turns))
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests=%d want 2", len(client.requests))
	}
	second := client.requests[1].Messages
	if len(second) < 4 {
		t.Fatalf("second request messages=%d want at least 4", len(second))
	}
	if second[1].Role != "user" || !strings.Contains(second[1].Content.(string), "BUCKLEY_CHAT_CHECK_ONE") {
		t.Fatalf("first user turn missing from second request: %+v", second)
	}
	if second[2].Role != "assistant" || second[2].Content != "BUCKLEY_CHAT_CHECK_ONE" {
		t.Fatalf("first assistant turn missing from second request: %+v", second)
	}
}

func TestRunnerRunNoChoices(t *testing.T) {
	client := &fakeClient{responses: []model.ChatResponse{{Model: "test-model"}}}
	runner := Runner{Client: client}

	result, err := runner.Run(context.Background(), Scenario{
		Model: "test-model",
		Turns: []Turn{{User: "hello"}},
	})
	if err == nil || !strings.Contains(err.Error(), "no response choices") {
		t.Fatalf("err=%v want no response choices", err)
	}
	if result == nil || len(result.Turns) != 1 || result.Turns[0].Err == "" {
		t.Fatalf("result did not capture failure: %+v", result)
	}
}

func TestRunnerRunMissingExpectedText(t *testing.T) {
	client := &fakeClient{responses: []model.ChatResponse{response("test-model", "different text")}}
	runner := Runner{Client: client}

	_, err := runner.Run(context.Background(), Scenario{
		Model: "test-model",
		Turns: []Turn{{
			User:         "hello",
			WantContains: []string{"expected token"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err=%v want missing expected text", err)
	}
}

func TestRunnerRunReasoningFallback(t *testing.T) {
	client := &fakeClient{responses: []model.ChatResponse{{
		Model: "test-model",
		Choices: []model.Choice{{
			Message: model.Message{Reasoning: "visible fallback"},
		}},
	}}}
	runner := Runner{Client: client}

	result, err := runner.Run(context.Background(), Scenario{
		Model: "test-model",
		Turns: []Turn{{User: "hello", WantContains: []string{"visible fallback"}}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Turns[0].Reasoning || result.Turns[0].Text != "visible fallback" {
		t.Fatalf("unexpected turn result: %+v", result.Turns[0])
	}
}

func response(modelID, text string) model.ChatResponse {
	return model.ChatResponse{
		Model: modelID,
		Choices: []model.Choice{{
			Message:      model.Message{Content: text},
			FinishReason: "stop",
		}},
		Usage: model.Usage{TotalTokens: 10},
	}
}
