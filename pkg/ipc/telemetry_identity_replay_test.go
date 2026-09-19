package ipc

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop/lifecycle"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
)

func TestTelemetryIdentityReplayPersistsAgentLoopEvents(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	storeClosed := false
	defer func() {
		if !storeClosed {
			_ = store.Close()
		}
	}()

	sessionID := "sess-identity-replay"
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	if err := store.CreateSession(&storage.Session{ID: sessionID, Principal: "tester", CreatedAt: now, LastActive: now, Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	telemetryHub := telemetry.NewHub()
	defer telemetryHub.Close()
	telemetryEvents, unsubscribe := telemetryHub.Subscribe()
	defer unsubscribe()
	observer := telemetry.NewAgentLoopObserver(telemetryHub)
	server := NewServer(Config{ProjectRoot: tmpDir}, store, telemetryHub, nil, nil, &config.Config{}, nil, nil)

	hostileRequestedModel := "bad requested model prompt-secret " + strings.Repeat("x", 140)
	hostileResponseModel := "provider\nprivate reasoning"
	hostileResponseID := "resp-" + strings.Repeat("y", 140)
	rawProviderError := "raw provider error with chain-of-thought"

	sources := []lifecycle.Event{
		{
			Sequence: 1, Type: lifecycle.ModelResponse, Timestamp: now, SessionID: sessionID, TaskID: "task-live", TurnID: "turn-live", StepID: "step-live",
			ModelID: "alias/live", ProviderID: "provider/live", RequestedModel: "alias/live", SelectedModel: "selected/live", ResponseModel: "provider/live-release", ResponseID: "resp-live",
			Usage: lifecycle.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, Status: "conclusive",
		},
		{
			Sequence: 2, Type: lifecycle.ModelResponse, Timestamp: now.Add(time.Second), SessionID: sessionID, TaskID: "task-replay", TurnID: "turn-replay", StepID: "step-replay",
			ModelID: "alias/replay", ProviderID: "provider/replay", RequestedModel: "alias/replay", SelectedModel: "selected/replay", ResponseModel: "provider/replay-release", ResponseID: "resp-replay",
			ExecutionIdentityConflicted: true, Replayed: true, Usage: lifecycle.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}, Status: "conclusive",
		},
		{
			Sequence: 3, Type: lifecycle.ModelError, Timestamp: now.Add(2 * time.Second), SessionID: sessionID, TaskID: "task-error", TurnID: "turn-error", StepID: "step-error",
			ModelID: "legacy/error", ProviderID: "provider/error", RequestedModel: hostileRequestedModel, SelectedModel: "selected/error", ResponseModel: hostileResponseModel, ResponseID: hostileResponseID,
			Error: rawProviderError,
		},
		{
			Sequence: 4, Type: lifecycle.TurnEnd, Timestamp: now.Add(3 * time.Second), SessionID: sessionID, TaskID: "task-incomplete", TurnID: "turn-incomplete",
			ModelID: "alias/incomplete", ProviderID: "provider/incomplete", RequestedModel: "alias/incomplete", SelectedModel: "selected/incomplete", ResponseModel: "provider/incomplete-release", ResponseID: "resp-incomplete",
			Usage: lifecycle.Usage{PromptTokens: 7, CompletionTokens: 8, TotalTokens: 15}, Status: "incomplete", FinishReason: "invalid_completion", StopReason: "progress_policy",
		},
		{
			Sequence: 5, Type: lifecycle.ModelResponse, Timestamp: now.Add(4 * time.Second), SessionID: sessionID, TaskID: "task-absent", TurnID: "turn-absent", StepID: "step-absent",
			ModelID: "legacy/absent", ProviderID: "provider/absent", Usage: lifecycle.Usage{TotalTokens: 1}, Status: "conclusive",
		},
	}

	for _, source := range sources {
		projected := observeAgentLoopTelemetry(t, observer, telemetryEvents, source)
		server.broadcastTelemetry(projected)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close store before replay: %v", err)
	}
	storeClosed = true

	reopened, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	storedEvents, err := reopened.ListIPCEventsAfter(sessionID, "", 5000)
	if err != nil {
		t.Fatalf("list ipc events after reopen: %v", err)
	}
	if len(storedEvents) != len(sources) {
		t.Fatalf("stored event count = %d, want %d: %#v", len(storedEvents), len(sources), storedEvents)
	}

	replayed := make(map[string]telemetry.Event, len(storedEvents))
	for _, stored := range storedEvents {
		if !strings.HasPrefix(stored.Type, "telemetry.agent.") {
			t.Fatalf("stored event type %q does not preserve telemetry agent namespace", stored.Type)
		}
		assertNoRawTelemetry(t, stored.Payload)
		var payload telemetry.Event
		if err := json.Unmarshal(stored.Payload, &payload); err != nil {
			t.Fatalf("unmarshal stored telemetry payload: %v\n%s", err, stored.Payload)
		}
		if payload.SessionID != sessionID {
			t.Fatalf("payload session id = %q, want %q", payload.SessionID, sessionID)
		}
		replayed[payload.TaskID] = payload
	}

	live := requireTelemetryTask(t, replayed, "task-live")
	requireEventType(t, live, telemetry.EventAgentModelResponse)
	requireString(t, live.Data, "requested_model", "alias/live")
	requireString(t, live.Data, "selected_model", "selected/live")
	requireString(t, live.Data, "response_model", "provider/live-release")
	requireString(t, live.Data, "response_id", "resp-live")
	requireString(t, live.Data, "provider", "provider/live")
	requireString(t, live.Data, "status", "conclusive")
	requireNumber(t, live.Data, "prompt_tokens", 10)
	requireNumber(t, live.Data, "completion_tokens", 5)
	requireNumber(t, live.Data, "total_tokens", 15)

	replay := requireTelemetryTask(t, replayed, "task-replay")
	requireEventType(t, replay, telemetry.EventAgentModelResponse)
	requireString(t, replay.Data, "requested_model", "alias/replay")
	requireString(t, replay.Data, "selected_model", "selected/replay")
	requireString(t, replay.Data, "response_model", "provider/replay-release")
	requireString(t, replay.Data, "response_id", "resp-replay")
	requireBool(t, replay.Data, "replayed", true)
	requireBool(t, replay.Data, "execution_identity_conflicted", true)
	requireNumber(t, replay.Data, "total_tokens", 5)

	modelErr := requireTelemetryTask(t, replayed, "task-error")
	requireEventType(t, modelErr, telemetry.EventAgentModelError)
	requireString(t, modelErr.Data, "provider", "provider/error")
	requireString(t, modelErr.Data, "selected_model", "selected/error")
	requireFingerprint(t, modelErr.Data, "requested_model")
	requireFingerprint(t, modelErr.Data, "response_model")
	requireFingerprint(t, modelErr.Data, "response_id")
	requireString(t, modelErr.Data, "error_code", "model_error")
	requireFingerprint(t, modelErr.Data, "error_fingerprint")
	requireNumber(t, modelErr.Data, "error_length", len(rawProviderError))

	incomplete := requireTelemetryTask(t, replayed, "task-incomplete")
	requireEventType(t, incomplete, telemetry.EventAgentTurnEnded)
	requireString(t, incomplete.Data, "requested_model", "alias/incomplete")
	requireString(t, incomplete.Data, "selected_model", "selected/incomplete")
	requireString(t, incomplete.Data, "response_model", "provider/incomplete-release")
	requireString(t, incomplete.Data, "response_id", "resp-incomplete")
	requireString(t, incomplete.Data, "status", "incomplete")
	requireString(t, incomplete.Data, "finish_code", "invalid_completion")
	requireString(t, incomplete.Data, "stop_code", "progress_policy")
	requireNumber(t, incomplete.Data, "prompt_tokens", 7)
	requireNumber(t, incomplete.Data, "completion_tokens", 8)
	requireNumber(t, incomplete.Data, "total_tokens", 15)

	absent := requireTelemetryTask(t, replayed, "task-absent")
	requireEventType(t, absent, telemetry.EventAgentModelResponse)
	requireString(t, absent.Data, "model", "legacy/absent")
	requireString(t, absent.Data, "provider", "provider/absent")
	requireString(t, absent.Data, "status", "conclusive")
	requireAbsent(t, absent.Data, "requested_model")
	requireAbsent(t, absent.Data, "selected_model")
	requireAbsent(t, absent.Data, "response_model")
	requireAbsent(t, absent.Data, "response_id")
	requireAbsent(t, absent.Data, "execution_identity_conflicted")
	requireNumber(t, absent.Data, "total_tokens", 1)
}

func observeAgentLoopTelemetry(t *testing.T, observer lifecycle.Observer, events <-chan telemetry.Event, source lifecycle.Event) telemetry.Event {
	t.Helper()
	observer(source)
	select {
	case event := <-events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for telemetry event %s", source.Type)
		return telemetry.Event{}
	}
}

func requireTelemetryTask(t *testing.T, events map[string]telemetry.Event, taskID string) telemetry.Event {
	t.Helper()
	event, ok := events[taskID]
	if !ok {
		t.Fatalf("missing telemetry event for task %q in %#v", taskID, events)
	}
	if event.Data == nil {
		t.Fatalf("telemetry event for task %q has nil data", taskID)
	}
	return event
}

func requireEventType(t *testing.T, event telemetry.Event, want telemetry.EventType) {
	t.Helper()
	if event.Type != want {
		t.Fatalf("event type = %q, want %q", event.Type, want)
	}
}

func requireString(t *testing.T, data map[string]any, key, want string) {
	t.Helper()
	got, ok := data[key].(string)
	if !ok || got != want {
		t.Fatalf("data[%q] = %#v, want %q", key, data[key], want)
	}
}

func requireFingerprint(t *testing.T, data map[string]any, key string) {
	t.Helper()
	got, ok := data[key].(string)
	if !ok || !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("data[%q] = %#v, want sha256 fingerprint", key, data[key])
	}
}

func requireBool(t *testing.T, data map[string]any, key string, want bool) {
	t.Helper()
	got, ok := data[key].(bool)
	if !ok || got != want {
		t.Fatalf("data[%q] = %#v, want %t", key, data[key], want)
	}
}

func requireNumber(t *testing.T, data map[string]any, key string, want int) {
	t.Helper()
	got, ok := data[key].(float64)
	if !ok || got != float64(want) {
		t.Fatalf("data[%q] = %#v, want %d", key, data[key], want)
	}
}

func requireAbsent(t *testing.T, data map[string]any, key string) {
	t.Helper()
	if _, ok := data[key]; ok {
		t.Fatalf("data[%q] unexpectedly present: %#v", key, data[key])
	}
}

func assertNoRawTelemetry(t *testing.T, raw json.RawMessage) {
	t.Helper()
	encoded := string(raw)
	for _, forbidden := range []string{
		"prompt-secret",
		"private reasoning",
		"raw provider error",
		"chain-of-thought",
		"arguments",
		"result",
	} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("stored telemetry payload leaked %q: %s", forbidden, encoded)
		}
	}
	if !json.Valid(raw) {
		t.Fatalf("stored telemetry payload is invalid JSON: %s", raw)
	}
}
