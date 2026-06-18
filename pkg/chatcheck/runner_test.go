package chatcheck

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

type fakeClient struct {
	responses    []model.ChatResponse
	errs         []error
	nilResponses map[int]bool
	requests     []model.ChatRequest
}

func (f *fakeClient) ChatCompletion(_ context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	f.requests = append(f.requests, req)
	idx := len(f.requests) - 1
	if idx < len(f.errs) && f.errs[idx] != nil {
		return nil, f.errs[idx]
	}
	if f.nilResponses != nil && f.nilResponses[idx] {
		return nil, nil
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
	if !result.Passed || result.Error != "" {
		t.Fatalf("result pass/error = %v/%q", result.Passed, result.Error)
	}
	if result.DurationMillis < 0 || result.CompletedAt.Before(result.StartedAt) {
		t.Fatalf("invalid timing in result: %+v", result)
	}
	if result.Usage.TotalTokens != 20 {
		t.Fatalf("total tokens = %d, want 20", result.Usage.TotalTokens)
	}
	if !result.Turns[0].Passed || len(result.Turns[0].Checks) == 0 || result.Turns[0].LatencyMillis < 0 {
		t.Fatalf("first turn report missing pass/check/timing data: %+v", result.Turns[0])
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

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(data), "Latency") {
		t.Fatalf("json result should expose latency_ms, not Go duration field: %s", data)
	}
}

func TestRunnerRunNoChoices(t *testing.T) {
	client := &fakeClient{responses: []model.ChatResponse{{Model: "test-model"}}}
	runner := Runner{Client: client}

	result, err := runner.Run(context.Background(), Scenario{
		Model: "test-model",
		Turns: []Turn{{User: "hello"}},
	})
	if err == nil || !strings.Contains(err.Error(), "no response choices") || !strings.Contains(err.Error(), "messages=1") {
		t.Fatalf("err=%v want no response choices with request shape", err)
	}
	if result == nil || len(result.Turns) != 1 || result.Turns[0].Err == "" {
		t.Fatalf("result did not capture failure: %+v", result)
	}
	if result.Passed || result.Error == "" || result.Turns[0].Passed {
		t.Fatalf("failure result should be marked failed: %+v", result)
	}
}

func TestRunnerRunNilResponse(t *testing.T) {
	client := &fakeClient{nilResponses: map[int]bool{0: true}}
	runner := Runner{Client: client}

	result, err := runner.Run(context.Background(), Scenario{
		Model: "test-model",
		Turns: []Turn{{User: "hello"}},
	})
	if err == nil || !strings.Contains(err.Error(), "nil chat response") || !strings.Contains(err.Error(), "messages=1") {
		t.Fatalf("err=%v want nil response with request shape", err)
	}
	if result == nil || len(result.Turns) != 1 || result.Turns[0].Err == "" {
		t.Fatalf("result did not capture failure: %+v", result)
	}
}

func TestRunnerRunMissingExpectedText(t *testing.T) {
	client := &fakeClient{responses: []model.ChatResponse{response("test-model", "different text")}}
	runner := Runner{Client: client}

	result, err := runner.Run(context.Background(), Scenario{
		Model: "test-model",
		Turns: []Turn{{
			User:         "hello",
			WantContains: []string{"expected token"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err=%v want missing expected text", err)
	}
	if result == nil || len(result.Turns) != 1 || len(result.Turns[0].Checks) == 0 {
		t.Fatalf("missing text result should include failed checks: %+v", result)
	}
	lastCheck := result.Turns[0].Checks[len(result.Turns[0].Checks)-1]
	if lastCheck.Passed || !strings.Contains(lastCheck.Message, "expected token") {
		t.Fatalf("unexpected failed check: %+v", lastCheck)
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

func TestRunnerRunProviderErrorCapturesFailedTurn(t *testing.T) {
	client := &fakeClient{errs: []error{errors.New("provider unavailable")}}
	runner := Runner{Client: client}

	result, err := runner.Run(context.Background(), Scenario{
		Model: "test-model",
		Turns: []Turn{{User: "hello"}},
	})
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("err=%v want provider error", err)
	}
	if result == nil || result.Passed || result.Error == "" {
		t.Fatalf("result should be failed with error: %+v", result)
	}
	if len(result.Turns) != 1 || result.Turns[0].Err == "" || result.Turns[0].Passed {
		t.Fatalf("failed turn not captured: %+v", result.Turns)
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
