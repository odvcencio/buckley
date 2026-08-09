package agentloop

import (
	"context"
	"path/filepath"
	"testing"

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

type testError string

func (e testError) Error() string { return string(e) }

func testingError(message string) error { return testError(message) }
