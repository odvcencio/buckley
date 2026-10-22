package agentloop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestJSONDigest_MatchesLegacyEncoding(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
	}{
		{name: "nil"},
		{name: "tool_call", value: model.ToolCall{ID: "call-1", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"source.go"}`}}},
		{name: "map", value: map[string]any{"second": 2, "first": "<source>"}},
		{name: "request_nil_messages", value: model.ChatRequest{Model: "test-model"}},
		{name: "request_empty_messages", value: model.ChatRequest{Model: "test-model", Messages: []model.Message{}}},
		{name: "request_pointer", value: &model.ChatRequest{Model: "test-model", Messages: []model.Message{{Role: "user", Content: "hello"}}}},
		{name: "request_history", value: model.ChatRequest{Model: "test-model", Models: []string{"fallback"}, Stream: true, SessionID: "test-session", Metadata: map[string]string{"task": "inspect"}, Messages: []model.Message{
			{Role: "user", Content: "<source> & \"quoted\"\n\u2028\xff"},
			{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "call-1", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"source.go"}`}}}},
			{Role: "tool", Name: "read_file", ToolCallID: "call-1", Content: "observed source"},
			{Role: "assistant", Reasoning: "retained thought", ReasoningContent: true, ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", HasIndex: true, Text: "retained thought", Extra: map[string]json.RawMessage{"vendor": json.RawMessage(`{"retained":true}`)}}}},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacy, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(legacy)
			want := hex.EncodeToString(sum[:])
			got, err := jsonDigest(tc.value)
			if err != nil || got != want {
				t.Fatalf("digest=%s want=%s err=%v", got, want, err)
			}
			body, err := marshalStepJSON(tc.value)
			if err != nil || string(body) != string(legacy) {
				t.Fatalf("evidence JSON changed: %s want=%s err=%v", body, legacy, err)
			}
		})
	}
}

func TestMarshalStepJSON_PreservesLegacyMetadataError(t *testing.T) {
	req := model.ChatRequest{Messages: []model.Message{{Role: "assistant", ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", Extra: map[string]json.RawMessage{"first": json.RawMessage(`1`), "invalid": json.RawMessage(`{`)}}}}}}
	_, legacyErr := json.Marshal(req)
	_, err := marshalStepJSON(req)
	var got, want *json.MarshalerError
	if !errors.As(err, &got) || !errors.As(legacyErr, &want) || got.Type != want.Type {
		t.Fatalf("legacy metadata error changed: got=%v want=%v", err, legacyErr)
	}
}

func TestController_ReplaysLegacyModelRequestDigest(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := t.Context()
	req := model.ChatRequest{Model: "test-model", Messages: []model.Message{{Role: "user", Content: "Retain the prior answer."}}, MaxTokens: 1024}
	projected := ProjectForContinuation(req, 0, nil, "", false)
	legacy, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(legacy)
	const taskID, turnID = "task-legacy-digest", "task-legacy-digest/turn-1"
	stepID := StableStepID(runID, taskID, turnID, 1, "model", 0)
	if _, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: runID, TaskID: taskID, StepID: stepID, Kind: "model", IdempotencyKey: stepID, InputDigest: hex.EncodeToString(sum[:])}); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(textResponse("legacy answer", model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := ev.Put(ctx, evidence.Object{Kind: evidence.KindModelResponse, MediaType: "application/json", InlineBody: body})
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.CompleteStep(ctx, runID, stepID, stored.ID, stored.ContentSHA256, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) { return req, nil },
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			return nil, testingError("legacy replay must not call the provider")
		}),
		RunLedger: ledger, Evidence: ev, StepJournal: ledger, RunID: runID, TaskID: taskID, TurnID: turnID,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ctrl.Run(ctx)
	if err != nil || result == nil || result.Message.Content != "legacy answer" || providerCalls != 0 {
		t.Fatalf("legacy replay: result=%+v provider_calls=%d err=%v", result, providerCalls, err)
	}
}
