package chatcheck

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestLoadScenarioFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenario.json")
	data := `{
  "description": "Checks the chat path.",
  "name": "repo-smoke",
  "tags": ["smoke", "OpenRouter"],
  "model": "file-model",
  "system_prompt": "Be terse.",
  "timeout": "2s",
  "max_tokens": 128,
  "session_id": "session-from-file",
  "turns": [
    {
      "user": "say ALPHA",
      "want_contains": ["ALPHA"],
      "min_chars": 5
    }
  ]
}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	scenario, err := LoadScenarioFile(path)
	if err != nil {
		t.Fatalf("LoadScenarioFile: %v", err)
	}
	if scenario.Description != "Checks the chat path." || scenario.Name != "repo-smoke" || scenario.Model != "file-model" {
		t.Fatalf("unexpected scenario identity: %+v", scenario)
	}
	if len(scenario.Tags) != 2 || scenario.Tags[0] != "smoke" || scenario.Tags[1] != "OpenRouter" {
		t.Fatalf("unexpected tags: %+v", scenario.Tags)
	}
	if scenario.Timeout != 2*time.Second {
		t.Fatalf("timeout=%s want 2s", scenario.Timeout)
	}
	if scenario.MaxTokens != 128 || scenario.SessionID != "session-from-file" {
		t.Fatalf("unexpected scenario config: %+v", scenario)
	}
	if len(scenario.Turns) != 1 || scenario.Turns[0].User != "say ALPHA" || scenario.Turns[0].WantContains[0] != "ALPHA" {
		t.Fatalf("unexpected turns: %+v", scenario.Turns)
	}
}

func TestLoadScenarioFileRejectsInvalidShape(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown field",
			body: `{"turns":[{"user":"hello"}],"extra":true}`,
			want: "unknown field",
		},
		{
			name: "no turns",
			body: `{"name":"empty"}`,
			want: "at least one turn",
		},
		{
			name: "conflicting timeout fields",
			body: `{"timeout":"1s","timeout_ms":1000,"turns":[{"user":"hello"}]}`,
			want: "both timeout and timeout_ms",
		},
		{
			name: "blank prompt",
			body: `{"turns":[{"user":"  "}]}`,
			want: "user prompt is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "scenario.json")
			if err := os.WriteFile(path, []byte(tt.body), 0o644); err != nil {
				t.Fatalf("write scenario: %v", err)
			}
			_, err := LoadScenarioFile(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v want %q", err, tt.want)
			}
		})
	}
}

func TestLoadScenariosDirectory(t *testing.T) {
	dir := t.TempDir()
	writeScenario := func(name string, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeScenario("b.json", `{"name":"second","turns":[{"user":"say B"}]}`)
	writeScenario("a.json", `{"name":"first","turns":[{"user":"say A"}]}`)
	writeScenario("notes.txt", `ignored`)

	scenarios, err := LoadScenarios(dir)
	if err != nil {
		t.Fatalf("LoadScenarios: %v", err)
	}
	if len(scenarios) != 2 {
		t.Fatalf("scenarios=%d want 2", len(scenarios))
	}
	if scenarios[0].Name != "first" || scenarios[1].Name != "second" {
		t.Fatalf("scenarios not sorted by filename: %+v", scenarios)
	}
}

func TestLoadScenariosDirectoryRejectsEmpty(t *testing.T) {
	_, err := LoadScenarios(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "contains no JSON scenarios") {
		t.Fatalf("err=%v want no JSON scenarios", err)
	}
}

func TestNormalizeScenarioTags(t *testing.T) {
	scenario := NormalizeScenario(Scenario{
		Name: "tagged",
		Tags: []string{" Smoke ", "chat", "smoke", "", "CHAT"},
		Turns: []Turn{{
			User: "hello",
		}},
	})
	if got, want := strings.Join(scenario.Tags, ","), "chat,smoke"; got != want {
		t.Fatalf("tags=%q want %q", got, want)
	}
}

func TestFilterScenariosByTagAndName(t *testing.T) {
	scenarios := []Scenario{
		NormalizeScenario(Scenario{Name: "smoke chat", Description: "fast provider check", Tags: []string{"smoke", "chat"}, Turns: []Turn{{User: "hello"}}}),
		NormalizeScenario(Scenario{Name: "regression tools", Description: "tool loop", Tags: []string{"regression", "tools"}, Turns: []Turn{{User: "hello"}}}),
		NormalizeScenario(Scenario{Name: "reasoning", Description: "premium model", Tags: []string{"reasoning"}, Turns: []Turn{{User: "hello"}}}),
	}

	got := FilterScenarios(scenarios, ScenarioSelector{Tags: []string{"SMOKE"}})
	if len(got) != 1 || got[0].Name != "smoke chat" {
		t.Fatalf("tag filter result: %+v", got)
	}

	got = FilterScenarios(scenarios, ScenarioSelector{NameContains: []string{"tool"}})
	if len(got) != 1 || got[0].Name != "regression tools" {
		t.Fatalf("name filter result: %+v", got)
	}

	got = FilterScenarios(scenarios, ScenarioSelector{Tags: []string{"chat"}, NameContains: []string{"provider"}})
	if len(got) != 1 || got[0].Name != "smoke chat" {
		t.Fatalf("combined filter result: %+v", got)
	}
}

func TestRunnerRunSuiteAggregatesFailures(t *testing.T) {
	client := &fakeClient{responses: []model.ChatResponse{
		response("test-model", "ONE"),
		response("test-model", "wrong"),
		response("test-model", "THREE"),
	}}
	runner := Runner{Client: client}

	suite, err := runner.RunSuite(context.Background(), "suite", []Scenario{
		{Name: "one", Model: "test-model", Turns: []Turn{{User: "say one", WantContains: []string{"ONE"}}}},
		{Name: "two", Model: "test-model", Turns: []Turn{{User: "say two", WantContains: []string{"TWO"}}}},
		{Name: "three", Model: "test-model", Turns: []Turn{{User: "say three", WantContains: []string{"THREE"}}}},
	})
	if err == nil || !strings.Contains(err.Error(), "two") {
		t.Fatalf("err=%v want suite failure naming failed scenario", err)
	}
	if suite == nil || suite.Passed || suite.PassedScenarios != 2 || suite.FailedScenarios != 1 {
		t.Fatalf("unexpected suite result: %+v", suite)
	}
	if len(suite.Results) != 3 {
		t.Fatalf("results=%d want 3", len(suite.Results))
	}
	if suite.Usage.TotalTokens != 30 {
		t.Fatalf("total tokens=%d want 30", suite.Usage.TotalTokens)
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
