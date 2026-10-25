package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

func reasoningDigestRequest() model.ChatRequest {
	signature := "opaque-signature"
	return model.ChatRequest{Model: "test-model", MaxTokens: 1024, Messages: []model.Message{
		{Role: "user", Content: "Continue the retained task."},
		{Role: "assistant", ReasoningDetails: []model.ReasoningDetail{{Type: "reasoning.text", Text: "retained thought", Signature: &signature, Extra: map[string]json.RawMessage{
			"field_a": json.RawMessage(`{"z":1,"a":2}`), "field_b": json.RawMessage(`9007199254740993`),
		}}}},
	}}
}

func TestModelInputDigest_StableAcrossMetadataRoundTrips(t *testing.T) {
	ctrl := &Controller{}
	original, before := reasoningDigestRequest(), reasoningDigestRequest()
	want, err := ctrl.modelInputDigest(t.Context(), "step", original)
	if err != nil || !strings.HasPrefix(want, reasoningDigestPrefix) {
		t.Fatalf("digest=%q err=%v", want, err)
	}
	for range 128 {
		got, err := ctrl.modelInputDigest(t.Context(), "step", original)
		if err != nil || got != want {
			t.Fatalf("digest=%q want=%q err=%v", got, want, err)
		}
		body, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var decoded model.ChatRequest
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		got, err = ctrl.modelInputDigest(t.Context(), "step", decoded)
		if err != nil || got != want {
			t.Fatalf("decoded digest=%q want=%q err=%v", got, want, err)
		}
	}
	if !reflect.DeepEqual(original, before) {
		t.Fatal("digest mutated the provider request")
	}
}

func TestModelInputDigest_PreservesOpaqueMetadata(t *testing.T) {
	ctrl := &Controller{}
	original := reasoningDigestRequest()
	want, err := ctrl.modelInputDigest(t.Context(), "step", original)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, key, value string }{
		{"large_integer", "field_b", `9007199254740992`},
		{"opaque_object_order", "field_a", `{"a":2,"z":1}`},
		{"null", "field_b", `null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := reasoningDigestRequest()
			req.Messages[1].ReasoningDetails[0].Extra[tc.key] = json.RawMessage(tc.value)
			got, err := ctrl.modelInputDigest(t.Context(), "step", req)
			if err != nil || got == want {
				t.Fatalf("changed metadata digest=%q original=%q err=%v", got, want, err)
			}
		})
	}
	req := reasoningDigestRequest()
	req.Messages[1].ReasoningDetails[0].Signature = nil
	req.Messages[1].ReasoningDetails[0].Extra["signature"] = json.RawMessage("null")
	req.Messages[1].ReasoningDetails[0].Extra["field_b"] = json.RawMessage{}
	got, err := ctrl.modelInputDigest(t.Context(), "step", req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded model.ChatRequest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ctrl.modelInputDigest(t.Context(), "step", decoded)
	if err != nil || got != roundTrip {
		t.Fatalf("null metadata changed on round trip: %q %q %v", got, roundTrip, err)
	}
	delete(decoded.Messages[1].ReasoningDetails[0].Extra, "signature")
	absent, err := ctrl.modelInputDigest(t.Context(), "step", decoded)
	if err != nil || absent == got {
		t.Fatalf("explicit null signature lost: %q %q %v", got, absent, err)
	}
	req.Messages[1].ReasoningDetails[0].Extra["field_a"] = json.RawMessage(`{`)
	if _, err := ctrl.modelInputDigest(t.Context(), "step", req); err == nil {
		t.Fatal("invalid metadata accepted")
	}
}

func TestModelInputDigest_MarkerCannotCollideAcrossDetails(t *testing.T) {
	ctrl := &Controller{}
	original := reasoningDigestRequest()
	// Keep another unordered detail so both requests use the versioned path.
	original.Messages[1].ReasoningDetails = append(original.Messages[1].ReasoningDetails, original.Messages[1].ReasoningDetails[0])
	want, err := ctrl.modelInputDigest(t.Context(), "step", original)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(original.Messages[1].ReasoningDetails[0].Extra)
	if err != nil {
		t.Fatal(err)
	}
	original.Messages[1].ReasoningDetails[0].Extra = map[string]json.RawMessage{"$buckley.reasoning-extra.v2": body}
	got, err := ctrl.modelInputDigest(t.Context(), "step", original)
	if err != nil || got == want {
		t.Fatalf("marker collided across details: %q %q %v", got, want, err)
	}
	original.Messages[1].ReasoningDetails = original.Messages[1].ReasoningDetails[:1]
	got, err = ctrl.modelInputDigest(t.Context(), "step", original)
	if err != nil || strings.HasPrefix(got, reasoningDigestPrefix) {
		t.Fatalf("single provider marker changed digest version: %q %v", got, err)
	}
}

type digestVersionJournal struct {
	DurableStepJournal
	step  runledger.ExecutionStep
	err   error
	calls int
}

func (j *digestVersionJournal) GetStep(context.Context, string, string) (runledger.ExecutionStep, error) {
	j.calls++
	return j.step, j.err
}

func TestModelInputDigest_PreservesLegacyRequestsAndRows(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra map[string]json.RawMessage
	}{
		{name: "absent"},
		{name: "one", extra: map[string]json.RawMessage{"vendor": json.RawMessage(`1`)}},
		{name: "one_and_typed_signature", extra: map[string]json.RawMessage{"vendor": json.RawMessage(`1`), "signature": json.RawMessage(`"opaque-signature"`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := reasoningDigestRequest()
			req.Messages[1].ReasoningDetails[0].Extra = tc.extra
			journal := &digestVersionJournal{err: errors.New("unexpected journal lookup")}
			ctrl := &Controller{cfg: ControllerConfig{StepJournal: journal}}
			want, err := jsonDigest(req)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ctrl.modelInputDigest(t.Context(), "step", req)
			if err != nil || got != want || journal.calls != 0 {
				t.Fatalf("got=%q want=%q lookups=%d err=%v", got, want, journal.calls, err)
			}
		})
	}
	ledger, _, runID := newDurableControllerStores(t)
	const stepID = "legacy-metadata"
	seeded, _, err := ledger.BeginStep(t.Context(), runledger.ExecutionStep{RunID: runID, StepID: stepID, Kind: "model", InputDigest: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	ctrl := &Controller{cfg: ControllerConfig{StepJournal: ledger, RunID: runID}}
	got, err := ctrl.modelInputDigest(t.Context(), stepID, reasoningDigestRequest())
	if err != nil || len(got) != 64 {
		t.Fatalf("retained legacy digest changed version: %q %v", got, err)
	}
	after, err := ledger.GetStep(t.Context(), runID, stepID)
	if err != nil || !reflect.DeepEqual(seeded, after) {
		t.Fatalf("legacy row changed: %+v err=%v", after, err)
	}
	failure := errors.New("journal unavailable")
	ctrl.cfg.StepJournal = &digestVersionJournal{err: failure}
	_, err = ctrl.modelInputDigest(t.Context(), stepID, reasoningDigestRequest())
	if !errors.Is(err, failure) {
		t.Fatalf("journal failure ignored: %v", err)
	}
}

func TestController_ReasoningDigestLookupFailsBeforeProvider(t *testing.T) {
	failure := errors.New("journal unavailable")
	providerCalls := 0
	ctrl, err := NewController(ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) { return reasoningDigestRequest(), nil },
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			return nil, errors.New("provider must not be called")
		}),
		StepJournal: &digestVersionJournal{err: failure},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctrl.Run(t.Context()); !errors.Is(err, failure) || providerCalls != 0 {
		t.Fatalf("lookup failure: provider_calls=%d err=%v", providerCalls, err)
	}
}

func TestController_ReplaysReasoningMetadataAfterStoreReopen(t *testing.T) {
	dir := t.TempDir()
	open := func() (*runledger.SQLiteStore, *evidence.SQLiteStore) {
		ev, err := evidence.New(filepath.Join(dir, "shared.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = ev.Close() })
		ledger, err := runledger.NewWithDB(ev.DB())
		if err != nil {
			t.Fatal(err)
		}
		return ledger, ev
	}
	ledger, ev := open()
	const runID, taskID, turnID = "reasoning-run", "reasoning-task", "reasoning-task/turn-1"
	if _, err := ledger.StartRun(t.Context(), runledger.AgentRun{RunID: runID, SessionID: "reasoning-session"}); err != nil {
		t.Fatal(err)
	}
	current := reasoningDigestRequest()
	providerCalls, toolCalls, pricingCalls := 0, 0, 0
	run := func(replay bool) (*Result, error) {
		messages := append([]model.Message(nil), current.Messages...)
		ctrl, err := NewController(ControllerConfig{
			BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
				req := current
				req.Messages = append([]model.Message(nil), messages...)
				return testToolRequest(req, "read_file"), nil
			},
			CallModel: ModelCallerFunc(func(_ context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
				providerCalls++
				if replay {
					return nil, errors.New("provider called during replay")
				}
				if !reflect.DeepEqual(req.Messages[1].ReasoningDetails, current.Messages[1].ReasoningDetails) {
					t.Fatal("digest changed provider metadata")
				}
				if providerCalls == 1 {
					return toolCallResponse("call-read", "read_file", `{"path":"source.go"}`, model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}), nil
				}
				return textResponse("retained answer", model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}), nil
			}),
			DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
				toolCalls++
				if replay {
					return nil, errors.New("tool called during replay")
				}
				return []ToolOutcome{{Content: "retained source", Success: true, EffectClass: "readonly"}}, nil
			}),
			History:    HistorySinkFunc(func(m model.Message) { messages = append(messages, m) }),
			MaxCostUSD: 1, CostForUsage: func(model.Usage) (float64, error) {
				pricingCalls++
				if replay {
					return 0, errors.New("pricing called during replay")
				}
				return 0.000001, nil
			},
			RunLedger: ledger, Evidence: ev, StepJournal: ledger, RunID: runID, TaskID: taskID, TurnID: turnID,
		})
		if err != nil {
			return nil, err
		}
		return ctrl.Run(t.Context())
	}
	first, err := run(false)
	if err != nil || first.Message.Content != "retained answer" || providerCalls != 2 || toolCalls != 1 {
		t.Fatalf("first=%+v provider=%d tool=%d err=%v", first, providerCalls, toolCalls, err)
	}
	beforePricing := pricingCalls
	stepID := StableStepID(runID, taskID, turnID, 1, "model", 0)
	stored, err := ledger.GetStep(t.Context(), runID, stepID)
	if err != nil || !strings.HasPrefix(stored.InputDigest, reasoningDigestPrefix) {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if err := ev.Close(); err != nil {
		t.Fatal(err)
	}
	ledger, ev = open()
	for range 16 {
		body, err := json.Marshal(current)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &current); err != nil {
			t.Fatal(err)
		}
		replayed, err := run(true)
		if err != nil || replayed.Message.Content != "retained answer" {
			t.Fatalf("replay=%+v err=%v", replayed, err)
		}
	}
	current.Messages[1].ReasoningDetails[0].Extra["field_b"] = json.RawMessage(`42`)
	if _, err := run(true); err == nil || !strings.Contains(err.Error(), "input digest changed") {
		t.Fatalf("changed metadata accepted: %v", err)
	}
	after, err := ledger.GetStep(t.Context(), runID, stepID)
	if err != nil || !reflect.DeepEqual(stored, after) {
		t.Fatalf("completed row changed: %+v err=%v", after, err)
	}
	if providerCalls != 2 || toolCalls != 1 || pricingCalls != beforePricing {
		t.Fatalf("replay effects: provider=%d tool=%d pricing=%d want=%d", providerCalls, toolCalls, pricingCalls, beforePricing)
	}
}
