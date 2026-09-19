package chatcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

type terminalEvidenceClient struct {
	responses []model.ChatResponse
	errs      []error
	requests  []model.ChatRequest
}

func (c *terminalEvidenceClient) ChatCompletion(_ context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	c.requests = append(c.requests, req)
	idx := len(c.requests) - 1
	var resp *model.ChatResponse
	if idx < len(c.responses) {
		next := c.responses[idx]
		resp = &next
	}
	if idx < len(c.errs) && c.errs[idx] != nil {
		return resp, c.errs[idx]
	}
	if resp == nil {
		return nil, errors.New("unexpected request")
	}
	return resp, nil
}

func TestRunnerRunRetainsPartialResponseEvidenceOnTerminalError(t *testing.T) {
	const private = "PRIVATE_CHATCHECK_TERMINAL_SENTINEL"
	const rawProviderSecret = "RAW_PROVIDER_SECRET"
	usage := chatcheckUsage(7, 8, 15)
	identity := model.ExecutionIdentity{
		RequestedModel: "requested/model",
		SelectedModel:  "selected/model",
		ProviderID:     "provider-a",
		ResponseModel:  "provider-model",
		ResponseID:     "response-partial",
		Conflicted:     true,
	}
	resp := chatcheckRetainedResponse("wire-model", "public partial draft", "stop", usage, identity)
	resp.Choices[0].Message.Reasoning = private
	resp.Choices[0].Message.ReasoningDetails = []model.ReasoningDetail{{Type: "reasoning.text", Text: private}}
	client := &terminalEvidenceClient{
		responses: []model.ChatResponse{resp},
		errs:      []error{fmt.Errorf("provider failed: %s", rawProviderSecret)},
	}

	result, err := (Runner{Client: client}).Run(context.Background(), Scenario{
		Model: "requested/model",
		Turns: []Turn{{User: "hello", WantContains: []string{"public"}}},
	})
	if err == nil || !strings.Contains(err.Error(), rawProviderSecret) {
		t.Fatalf("Run error = %v, want returned Go error to retain raw cause", err)
	}
	var incomplete *agentloop.IncompleteTurnError
	if !errors.As(err, &incomplete) {
		t.Fatalf("Run error = %T %[1]v, want incomplete turn", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d, want no retry after partial provider error", len(client.requests))
	}
	requireFailedTurnEvidence(t, result, "public partial draft", usage, identity)
	if result.Turns[0].Model != "wire-model" || result.Turns[0].Finish != "stop" {
		t.Fatalf("turn model/finish = %q/%q", result.Turns[0].Model, result.Turns[0].Finish)
	}
	if !result.Turns[0].Reasoning {
		t.Fatalf("turn should report that private reasoning was present")
	}
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatalf("marshal result: %v", marshalErr)
	}
	public := string(data)
	if strings.Contains(public, private) || strings.Contains(public, rawProviderSecret) {
		t.Fatalf("serialized failed result leaked private/raw provider text: %s", public)
	}
	if strings.Contains(result.Error, "provider error") || strings.Contains(result.Error, rawProviderSecret) {
		t.Fatalf("result error = %q, want sanitized incomplete error without raw provider text", result.Error)
	}
}

func TestRunnerRunRetainsTruncatedResponseEvidenceWithoutAccepting(t *testing.T) {
	for _, finish := range []string{"length", "max_tokens", "max_output_tokens"} {
		t.Run(finish, func(t *testing.T) {
			usage := chatcheckUsage(3, 4, 7)
			identity := model.ExecutionIdentity{ResponseID: "resp-" + finish, ResponseModel: "provider-model"}
			client := &terminalEvidenceClient{responses: []model.ChatResponse{
				chatcheckRetainedResponse("provider-model", "public truncated "+finish, finish, usage, identity),
			}}

			result, err := (Runner{Client: client}).Run(context.Background(), Scenario{
				Model: "requested/model",
				Turns: []Turn{{User: "hello", WantContains: []string{"public"}}},
			})
			if err == nil || !strings.Contains(err.Error(), "truncated") {
				t.Fatalf("Run error = %v, want truncation failure", err)
			}
			requireFailedTurnEvidence(t, result, "public truncated "+finish, usage, identity)
			if result.Turns[0].Finish != finish {
				t.Fatalf("finish = %q, want %q", result.Turns[0].Finish, finish)
			}
			if len(client.requests) != 1 {
				t.Fatalf("requests = %d, want no retry for retained truncated response", len(client.requests))
			}
		})
	}
}

func TestRunnerRunAggregatesCompletedAndPartialUsageOnce(t *testing.T) {
	firstUsage := chatcheckUsage(5, 10, 15)
	partialUsage := chatcheckUsage(6, 10, 16)
	firstIdentity := model.ExecutionIdentity{ResponseID: "resp-complete", ResponseModel: "complete-model"}
	partialIdentity := model.ExecutionIdentity{ResponseID: "resp-partial", ResponseModel: "partial-model"}
	client := &terminalEvidenceClient{responses: []model.ChatResponse{
		chatcheckRetainedResponse("complete-model", "FIRST", "stop", firstUsage, firstIdentity),
		chatcheckRetainedResponse("partial-model", "SECOND partial", "length", partialUsage, partialIdentity),
	}}

	result, err := (Runner{Client: client}).Run(context.Background(), Scenario{
		Model: "requested/model",
		Turns: []Turn{
			{User: "one", WantContains: []string{"FIRST"}},
			{User: "two", WantContains: []string{"SECOND"}},
		},
	})
	if err == nil {
		t.Fatal("Run succeeded, want second truncated turn to fail")
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want one completed request plus one partial request", len(client.requests))
	}
	if len(result.Turns) != 2 || !result.Turns[0].Passed || result.Turns[1].Passed {
		t.Fatalf("turn pass state = %+v", result.Turns)
	}
	wantUsage := chatcheckCombinedUsage(firstUsage, partialUsage)
	if !reflect.DeepEqual(result.Usage, wantUsage) {
		t.Fatalf("result usage = %+v, want %+v", result.Usage, wantUsage)
	}
	if got := turnModelExecutions(t, result.Turns[0]); len(got) != 1 || got[0].ResponseID != "resp-complete" {
		t.Fatalf("first turn model executions = %+v", got)
	}
	if got := turnModelExecutions(t, result.Turns[1]); len(got) != 1 || got[0].ResponseID != "resp-partial" {
		t.Fatalf("partial turn model executions = %+v", got)
	}
}

func TestRunnerRunReasoningOnlyPartialRetainsUsageWithoutLeak(t *testing.T) {
	const private = "PRIVATE_REASONING_ONLY_CHATCHECK"
	usage := chatcheckUsage(2, 3, 5)
	identity := model.ExecutionIdentity{ResponseID: "resp-reasoning-only", ResponseModel: "reasoning-model"}
	resp := chatcheckRetainedResponse("reasoning-model", "", "stop", usage, identity)
	resp.Choices[0].Message.Content = nil
	resp.Choices[0].Message.Reasoning = private
	resp.Choices[0].Message.ReasoningDetails = []model.ReasoningDetail{{Type: "reasoning.text", Text: private}}
	client := &terminalEvidenceClient{
		responses: []model.ChatResponse{resp},
		errs:      []error{errors.New("provider failed after reasoning only")},
	}

	result, err := (Runner{Client: client}).Run(context.Background(), Scenario{
		Model: "requested/model",
		Turns: []Turn{{User: "hello"}},
	})
	if err == nil {
		t.Fatal("Run succeeded, want provider partial failure")
	}
	requireFailedTurnEvidence(t, result, "", usage, identity)
	if !result.Turns[0].Reasoning {
		t.Fatalf("turn should report reasoning presence")
	}
	data, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatalf("marshal result: %v", marshalErr)
	}
	if strings.Contains(string(data), private) || strings.Contains(err.Error(), private) {
		t.Fatalf("private reasoning leaked: result=%s err=%v", data, err)
	}
}

func TestRunnerRunNilCancelDoesNotInventRetainedWork(t *testing.T) {
	client := &terminalEvidenceClient{errs: []error{context.Canceled}}

	result, err := (Runner{Client: client}).Run(context.Background(), Scenario{
		Model: "requested/model",
		Turns: []Turn{{User: "hello"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	if result == nil || result.Passed || len(result.Turns) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	turn := result.Turns[0]
	if turn.Text != "" || turn.Usage != (model.Usage{}) || len(turnModelExecutions(t, turn)) != 0 || turn.ToolCalls != 0 {
		t.Fatalf("nil cancel invented retained work: %+v", turn)
	}
}

func TestRunnerRunSuiteDoesNotLeakPartialProviderError(t *testing.T) {
	const rawProviderSecret = "RAW_CHATCHECK_SUITE_PROVIDER_SECRET"
	resp := chatcheckRetainedResponse(
		"wire-model",
		"public partial",
		"stop",
		chatcheckUsage(2, 4, 6),
		model.ExecutionIdentity{ResponseID: "resp-suite-partial", ResponseModel: "wire-model"},
	)
	client := &terminalEvidenceClient{
		responses: []model.ChatResponse{resp},
		errs:      []error{fmt.Errorf("provider exploded: %s", rawProviderSecret)},
	}

	suite, err := (Runner{Client: client}).RunSuite(context.Background(), "suite", []Scenario{{
		Name:  "partial",
		Model: "requested/model",
		Turns: []Turn{{User: "hello"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "chat check suite failed") {
		t.Fatalf("RunSuite error = %v, want suite failure", err)
	}
	if suite == nil || suite.Passed || suite.Error == "" || len(suite.Results) != 1 {
		t.Fatalf("unexpected suite result: %+v", suite)
	}
	data, marshalErr := json.Marshal(suite)
	if marshalErr != nil {
		t.Fatalf("marshal suite: %v", marshalErr)
	}
	if strings.Contains(string(data), rawProviderSecret) || strings.Contains(suite.Error, rawProviderSecret) {
		t.Fatalf("suite leaked raw provider error: result=%s err=%q", data, suite.Error)
	}
	requireFailedTurnEvidence(t, &suite.Results[0], "public partial", chatcheckUsage(2, 4, 6), model.ExecutionIdentity{ResponseID: "resp-suite-partial", ResponseModel: "wire-model"})
}

func TestRunnerRunSuiteAggregatesExtendedUsageAndRetainsPartialIdentity(t *testing.T) {
	firstUsage := chatcheckUsage(10, 20, 30)
	partialUsage := chatcheckUsage(11, 21, 32)
	firstUsage.Estimated = true
	partialUsage.Estimated = true
	firstIdentity := model.ExecutionIdentity{ResponseID: "resp-suite-complete", ResponseModel: "complete-model"}
	partialIdentity := model.ExecutionIdentity{ResponseID: "resp-suite-partial", ResponseModel: "partial-model"}
	client := &terminalEvidenceClient{responses: []model.ChatResponse{
		chatcheckRetainedResponse("complete-model", "FIRST suite", "stop", firstUsage, firstIdentity),
		chatcheckRetainedResponse("partial-model", "SECOND suite partial", "length", partialUsage, partialIdentity),
	}}

	suite, err := (Runner{Client: client}).RunSuite(context.Background(), "suite", []Scenario{
		{
			Name:  "complete",
			Model: "requested/model",
			Turns: []Turn{{User: "one", WantContains: []string{"FIRST"}}},
		},
		{
			Name:  "partial",
			Model: "requested/model",
			Turns: []Turn{{User: "two", WantContains: []string{"SECOND"}}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "chat check suite failed") {
		t.Fatalf("RunSuite error = %v, want suite failure", err)
	}
	if suite == nil || suite.Passed || suite.PassedScenarios != 1 || suite.FailedScenarios != 1 || len(suite.Results) != 2 {
		t.Fatalf("unexpected suite status: %+v", suite)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want one request per scenario with no retry", len(client.requests))
	}
	wantUsage := chatcheckCombinedUsage(firstUsage, partialUsage)
	if !reflect.DeepEqual(suite.Usage, wantUsage) {
		t.Fatalf("suite usage = %+v, want %+v", suite.Usage, wantUsage)
	}
	if !suite.Results[0].Passed || suite.Results[1].Passed {
		t.Fatalf("scenario pass states = %v/%v, want complete pass and partial fail", suite.Results[0].Passed, suite.Results[1].Passed)
	}
	requireTurnEvidence(t, suite.Results[0].Turns[0], "FIRST suite", firstUsage, firstIdentity)
	requireTurnEvidence(t, suite.Results[1].Turns[0], "SECOND suite partial", partialUsage, partialIdentity)
	if got := turnModelExecutions(t, suite.Results[0].Turns[0]); len(got) != 1 || got[0].ResponseID != "resp-suite-complete" {
		t.Fatalf("first scenario identity = %+v", got)
	}
	if got := turnModelExecutions(t, suite.Results[1].Turns[0]); len(got) != 1 || got[0].ResponseID != "resp-suite-partial" {
		t.Fatalf("partial scenario identity = %+v", got)
	}
}

func TestRunnerRunSuccessStillAcceptsConclusiveResponse(t *testing.T) {
	usage := chatcheckUsage(1, 2, 3)
	identity := model.ExecutionIdentity{ResponseID: "resp-success", ResponseModel: "success-model"}
	client := &terminalEvidenceClient{responses: []model.ChatResponse{
		chatcheckRetainedResponse("success-model", "ALL GOOD", "stop", usage, identity),
	}}

	result, err := (Runner{Client: client}).Run(context.Background(), Scenario{
		Model: "requested/model",
		Turns: []Turn{{User: "hello", WantContains: []string{"GOOD"}}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil || !result.Passed || len(result.Turns) != 1 || !result.Turns[0].Passed {
		t.Fatalf("unexpected success result: %+v", result)
	}
	requireTurnEvidence(t, result.Turns[0], "ALL GOOD", usage, identity)
}

func TestRunnerRunObservesToolCallsOnRetainedPartialWithoutDispatch(t *testing.T) {
	usage := chatcheckUsage(4, 5, 9)
	identity := model.ExecutionIdentity{ResponseID: "resp-tool-partial", ResponseModel: "tool-model"}
	resp := chatcheckRetainedResponse("tool-model", "public tool partial", "tool_calls", usage, identity)
	resp.Choices[0].Message.ToolCalls = []model.ToolCall{{
		ID:       "call-1",
		Function: model.FunctionCall{Name: "never_dispatch_me", Arguments: `{"ok":true}`},
	}}
	client := &terminalEvidenceClient{
		responses: []model.ChatResponse{resp},
		errs:      []error{errors.New("provider failed after tool call")},
	}

	result, err := (Runner{Client: client}).Run(context.Background(), Scenario{
		Model: "requested/model",
		Turns: []Turn{{User: "hello"}},
	})
	if err == nil {
		t.Fatal("Run succeeded, want partial tool-call failure")
	}
	requireFailedTurnEvidence(t, result, "public tool partial", usage, identity)
	if result.Turns[0].ToolCalls != 1 {
		t.Fatalf("tool calls = %d, want raw tool call observed but not dispatched", result.Turns[0].ToolCalls)
	}
	if len(client.requests) != 1 || len(client.requests[0].Tools) != 0 {
		t.Fatalf("requests/tools = %d/%d, want one no-tools request", len(client.requests), len(client.requests[0].Tools))
	}
}

func requireFailedTurnEvidence(t *testing.T, result *Result, text string, usage model.Usage, identity model.ExecutionIdentity) {
	t.Helper()
	if result == nil || result.Passed || result.Error == "" || len(result.Turns) != 1 || result.Turns[0].Passed || result.Turns[0].Err == "" {
		t.Fatalf("unexpected failed result: %+v", result)
	}
	requireTurnEvidence(t, result.Turns[0], text, usage, identity)
	if !reflect.DeepEqual(result.Usage, usage) {
		t.Fatalf("result usage = %+v, want %+v", result.Usage, usage)
	}
}

func requireTurnEvidence(t *testing.T, turn TurnResult, text string, usage model.Usage, identity model.ExecutionIdentity) {
	t.Helper()
	if turn.Text != text || turn.CharLength != len(text) {
		t.Fatalf("turn text/length = %q/%d, want %q/%d", turn.Text, turn.CharLength, text, len(text))
	}
	if !reflect.DeepEqual(turn.Usage, usage) {
		t.Fatalf("turn usage = %+v, want %+v", turn.Usage, usage)
	}
	executions := turnModelExecutions(t, turn)
	if len(executions) != 1 {
		t.Fatalf("model executions = %+v, want one", executions)
	}
	got := executions[0]
	if got.RequestedModel != identity.RequestedModel ||
		got.SelectedModel != identity.SelectedModel ||
		got.ProviderID != identity.ProviderID ||
		got.ResponseModel != identity.ResponseModel ||
		got.ResponseID != identity.ResponseID ||
		got.Conflicted != identity.Conflicted {
		t.Fatalf("model execution = %+v, want %+v", got, identity)
	}
}

func turnModelExecutions(t *testing.T, turn TurnResult) []transparency.ExecutionIdentityTrace {
	t.Helper()
	value := reflect.ValueOf(turn)
	field := value.FieldByName("ModelExecutions")
	if !field.IsValid() {
		return nil
	}
	executions, ok := field.Interface().([]transparency.ExecutionIdentityTrace)
	if !ok {
		t.Fatalf("ModelExecutions field has unexpected type %T", field.Interface())
	}
	return executions
}

func chatcheckRetainedResponse(modelID, text, finish string, usage model.Usage, identity model.ExecutionIdentity) model.ChatResponse {
	return model.ChatResponse{
		ID:    identity.ResponseID,
		Model: modelID,
		Choices: []model.Choice{{
			Message:      model.Message{Content: text},
			FinishReason: finish,
		}},
		Usage:             usage,
		UsagePresent:      true,
		ExecutionIdentity: &identity,
	}
}

func chatcheckUsage(prompt, completion, total int) model.Usage {
	return model.Usage{
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		PromptTokensDetails: &model.PromptTokensDetails{
			CachedTokens: prompt - 1,
		},
		CompletionTokenDetails: &model.CompletionTokenDetails{
			ReasoningTokens: completion - 1,
		},
		CacheWriteTokens: 1,
	}
}

func chatcheckCombinedUsage(parts ...model.Usage) model.Usage {
	var total model.Usage
	for _, part := range parts {
		total = model.AddUsage(total, part)
	}
	return total
}
