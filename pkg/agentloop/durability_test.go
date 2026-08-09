package agentloop

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
)

func newDurableControllerStores(t *testing.T) (*runledger.SQLiteStore, *evidence.SQLiteStore, string) {
	t.Helper()
	dir := t.TempDir()
	ev, err := evidence.New(filepath.Join(dir, "shared.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	runID := "run-durable"
	if _, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: runID, SessionID: "durable-test"}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	return ledger, ev, runID
}

func TestController_ReplaysCompletedModelStepWithoutCallingProvider(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	providerCalls := 0
	build := func(context.Context, int) (model.ChatRequest, error) {
		return model.ChatRequest{Model: "test-model"}, nil
	}
	call := func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		return textResponse("durable result", model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}), nil
	}
	config := ControllerConfig{
		BuildRequest: build,
		CallModel:    ModelCallerFunc(call),
		RunLedger:    ledger,
		Evidence:     ev,
		StepJournal:  ledger,
		RunID:        runID,
		SessionID:    "durable-test",
		TaskID:       "task-1",
		TurnID:       "task-1/cp-001/turn-000",
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController first: %v", err)
	}
	if _, err := first.Run(ctx); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	secondProviderCalls := 0
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		secondProviderCalls++
		return nil, testingError("provider should not be called during replay")
	})
	second, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController second: %v", err)
	}
	result, err := second.Run(ctx)
	if err != nil {
		t.Fatalf("replay Run: %v", err)
	}
	if secondProviderCalls != 0 || providerCalls != 1 {
		t.Fatalf("provider calls = first %d, replay %d; want 1 and 0", providerCalls, secondProviderCalls)
	}
	if got, _ := result.Message.Content.(string); got != "durable result" {
		t.Fatalf("replayed message = %q, want durable result", got)
	}
}

func TestController_ReplaysToolResultWithoutExecutingDispatcher(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	providerCalls := 0
	dispatchCalls := 0
	build := func(context.Context, int) (model.ChatRequest, error) {
		return model.ChatRequest{Model: "test-model"}, nil
	}
	firstProvider := func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		providerCalls++
		if providerCalls == 1 {
			return toolCallResponse("call-1", "read_file", `{}`, model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}), nil
		}
		return textResponse("after tool", model.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}), nil
	}
	config := ControllerConfig{
		BuildRequest: build,
		CallModel:    ModelCallerFunc(firstProvider),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			dispatchCalls++
			return []ToolOutcome{{Content: "file contents", Success: true, EffectClass: "modifying"}}, nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-1",
		TurnID:      "task-1/cp-001/turn-000",
	}
	first, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController first: %v", err)
	}
	if _, err := first.Run(ctx); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	replayProviderCalls := 0
	config.CallModel = ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
		replayProviderCalls++
		return nil, testingError("provider should not be called during tool replay")
	})
	config.DispatchTools = ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
		return nil, testingError("dispatcher should not be called during tool replay")
	})
	second, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController second: %v", err)
	}
	result, err := second.Run(ctx)
	if err != nil {
		t.Fatalf("replay Run: %v", err)
	}
	if replayProviderCalls != 0 || dispatchCalls != 1 {
		t.Fatalf("replay calls = provider %d, original dispatch %d; want 0 and 1", replayProviderCalls, dispatchCalls)
	}
	if got, _ := result.Message.Content.(string); got != "after tool" {
		t.Fatalf("replayed final message = %q, want after tool", got)
	}
}

func TestController_LargeToolResultStaysInEvidence(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	largeResult := strings.Repeat("payload-probe-", 1<<16) // ~900 KiB
	providerCalls := 0
	config := ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			providerCalls++
			if providerCalls == 1 {
				return toolCallResponse("call-1", "read_file", `{}`, model.Usage{}), nil
			}
			return textResponse("done", model.Usage{}), nil
		}),
		DispatchTools: ToolDispatcherFunc(func(context.Context, []model.ToolCall) ([]ToolOutcome, error) {
			return []ToolOutcome{{Content: largeResult, Success: true, EffectClass: "readonly"}}, nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-1",
		TurnID:      "task-1/cp-001/turn-000",
	}
	controller, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if _, err := controller.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var completed *runledger.Event
	for i := range events {
		if events[i].Type == runledger.EventToolCompleted {
			completed = &events[i]
		}
	}
	if completed == nil {
		t.Fatalf("events = %d, want a tool.completed event", len(events))
	}
	encoded, err := json.Marshal(completed.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if len(encoded) > 4096 {
		t.Fatalf("tool.completed payload is %d bytes, want compact evidence-referenced payload", len(encoded))
	}
	if strings.Contains(string(encoded), "payload-probe-payload-probe-") {
		t.Fatalf("tool.completed payload embeds the tool result body")
	}
	evidenceID, _ := completed.Payload["output_evidence_id"].(string)
	if evidenceID == "" {
		t.Fatalf("payload = %s, want output_evidence_id", encoded)
	}
	obj, err := ev.Get(ctx, evidenceID)
	if err != nil {
		t.Fatalf("evidence Get %s: %v", evidenceID, err)
	}
	if !strings.Contains(string(obj.InlineBody), "payload-probe-") || len(obj.InlineBody) < len(largeResult) {
		t.Fatalf("evidence body = %d bytes, want the full tool result", len(obj.InlineBody))
	}
}

func TestController_StepEvidenceSurvivesRetentionUntilRunReleased(t *testing.T) {
	ledger, ev, runID := newDurableControllerStores(t)
	ctx := context.Background()
	config := ControllerConfig{
		BuildRequest: func(context.Context, int) (model.ChatRequest, error) {
			return model.ChatRequest{Model: "test-model"}, nil
		},
		CallModel: ModelCallerFunc(func(context.Context, model.ChatRequest, bool) (*model.ChatResponse, error) {
			return textResponse("pinned result", model.Usage{}), nil
		}),
		RunLedger:   ledger,
		Evidence:    ev,
		StepJournal: ledger,
		RunID:       runID,
		SessionID:   "durable-test",
		TaskID:      "task-1",
		TurnID:      "task-1/cp-001/turn-000",
	}
	controller, err := NewController(config)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if _, err := controller.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// A sweep with the cutoff far in the future reaps every unpinned
	// ephemeral object; the run's step evidence must survive it.
	future := time.Now().UTC().Add(365 * 24 * time.Hour)
	removed, err := ev.Sweep(ctx, evidence.DefaultRetentionPolicy(), future)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("sweep removed %v, want pinned step evidence retained", removed)
	}
	step, err := ledger.GetStep(ctx, runID, StableStepID(runID, "task-1", "task-1/cp-001/turn-000", 1, "model", 0))
	if err != nil {
		t.Fatalf("GetStep: %v", err)
	}
	if _, err := ev.Get(ctx, step.OutputEvidenceID); err != nil {
		t.Fatalf("output evidence gone after sweep: %v", err)
	}

	// Pruning the run releases the pins; the next sweep reaps.
	released, err := ev.ReleaseByReason(ctx, RunPinReason(runID))
	if err != nil {
		t.Fatalf("ReleaseByReason: %v", err)
	}
	if released == 0 {
		t.Fatal("ReleaseByReason released nothing, want the run's step pins")
	}
	removed, err = ev.Sweep(ctx, evidence.DefaultRetentionPolicy(), future)
	if err != nil {
		t.Fatalf("second Sweep: %v", err)
	}
	if len(removed) == 0 {
		t.Fatal("sweep after release removed nothing, want step evidence reaped")
	}
}

type testError string

func (e testError) Error() string { return string(e) }

func testingError(message string) error { return testError(message) }
