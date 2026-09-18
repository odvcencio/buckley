package telemetry

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop/lifecycle"
)

func TestNewAgentLoopObserverPublishesCategoricalLifecycleMetadataOnce(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	ch, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	NewAgentLoopObserver(hub)(lifecycle.Event{
		Sequence:       7,
		Type:           lifecycle.ModelError,
		Timestamp:      time.Unix(42, 0).UTC(),
		RunID:          "run-1",
		SessionID:      "session-1",
		TaskID:         "task-1",
		TurnID:         "turn-1",
		Round:          2,
		Attempt:        3,
		RunAttempt:     4,
		Continuation:   true,
		Phase:          "model",
		ModelID:        "deepseek/deepseek-v4-pro-0813",
		ProviderID:     "openrouter",
		RequestedModel: "alias/review",
		SelectedModel:  "deepseek/deepseek-v4-pro-0813",
		ResponseModel:  "deepseek/deepseek-v4-pro-0813-release",
		ResponseID:     "resp_abc-123",
		Error:          "provider failed after arbitrary output",
		FinishReason:   "model_error",
		StopReason:     "model_error",
		Usage:          lifecycle.Usage{PromptTokens: 11, CompletionTokens: 13, TotalTokens: 24, Estimated: true},
	})

	select {
	case event := <-ch:
		if event.Type != EventAgentModelError || event.SessionID != "session-1" || event.TaskID != "task-1" {
			t.Fatalf("event = %+v", event)
		}
		if event.Data["model"] != "deepseek/deepseek-v4-pro-0813" || event.Data["provider"] != "openrouter" {
			t.Fatalf("identity metadata = %+v", event.Data)
		}
		if event.Data["requested_model"] != "alias/review" || event.Data["selected_model"] != "deepseek/deepseek-v4-pro-0813" ||
			event.Data["response_model"] != "deepseek/deepseek-v4-pro-0813-release" || event.Data["response_id"] != "resp_abc-123" {
			t.Fatalf("execution identity metadata = %+v", event.Data)
		}
		if event.Data["round"] != 2 || event.Data["attempt"] != 3 || event.Data["run_attempt"] != 4 || event.Data["continuation"] != true || event.Data["sequence"] != uint64(7) {
			t.Fatalf("ordering metadata = %+v", event.Data)
		}
		if event.Data["prompt_tokens"] != 11 || event.Data["total_tokens"] != 24 {
			t.Fatalf("usage metadata = %+v", event.Data)
		}
		if event.Data["usage_estimated"] != true {
			t.Fatalf("usage provenance = %+v", event.Data)
		}
		if event.Data["error_code"] != "model_error" || event.Data["finish_code"] != "model_error" || event.Data["stop_code"] != "model_error" {
			t.Fatalf("categorical metadata = %+v", event.Data)
		}
		if fingerprint, _ := event.Data["error_fingerprint"].(string); !strings.HasPrefix(fingerprint, "sha256:") {
			t.Fatalf("error fingerprint = %#v", event.Data["error_fingerprint"])
		}
		for _, forbidden := range []string{"error", "stop_reason", "finish_reason"} {
			if _, ok := event.Data[forbidden]; ok {
				t.Fatalf("raw field %q present in %+v", forbidden, event.Data)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lifecycle telemetry")
	}

	select {
	case duplicate := <-ch:
		t.Fatalf("observer published duplicate event: %+v", duplicate)
	default:
	}
}

func TestNewAgentLoopObserverPreservesModelExecutionIdentityForReplayedResponse(t *testing.T) {
	event := observeAgentLoopEvent(t, lifecycle.Event{
		Sequence:                    8,
		Type:                        lifecycle.ModelResponse,
		Timestamp:                   time.Unix(43, 0).UTC(),
		SessionID:                   "session-identity",
		TaskID:                      "task-identity",
		ModelID:                     "legacy/requested",
		ProviderID:                  "openrouter",
		RequestedModel:              "user/alias",
		SelectedModel:               "vendor/selected",
		ResponseModel:               "vendor/reported-release",
		ResponseID:                  "resp_exact-42",
		ExecutionIdentityConflicted: true,
		Replayed:                    true,
		Usage:                       lifecycle.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	})
	if event.Type != EventAgentModelResponse {
		t.Fatalf("type = %q", event.Type)
	}
	for key, want := range map[string]any{
		"model":                         "legacy/requested",
		"provider":                      "openrouter",
		"requested_model":               "user/alias",
		"selected_model":                "vendor/selected",
		"response_model":                "vendor/reported-release",
		"response_id":                   "resp_exact-42",
		"execution_identity_conflicted": true,
		"replayed":                      true,
	} {
		if got := event.Data[key]; got != want {
			t.Fatalf("%s = %#v, want %#v in %+v", key, got, want, event.Data)
		}
	}
}

func TestNewAgentLoopObserverOmitsAbsentModelExecutionIdentity(t *testing.T) {
	event := observeAgentLoopEvent(t, lifecycle.Event{Type: lifecycle.ModelResponse, ModelID: "legacy/model", ProviderID: "provider"})
	for _, key := range []string{"requested_model", "selected_model", "response_model", "response_id", "execution_identity_conflicted"} {
		if _, ok := event.Data[key]; ok {
			t.Fatalf("absent identity key %q was published in %+v", key, event.Data)
		}
	}
	if event.Data["model"] != "legacy/model" || event.Data["provider"] != "provider" {
		t.Fatalf("legacy model/provider changed: %+v", event.Data)
	}
}

func TestNewAgentLoopObserverFingerprintsHostileAndOversizedExecutionIdentity(t *testing.T) {
	raw := "  model with spaces and private text " + strings.Repeat("x", agentLoopMaxIdentifierBytes+20)
	event := observeAgentLoopEvent(t, lifecycle.Event{
		Type:           lifecycle.ModelError,
		RequestedModel: raw,
		SelectedModel:  "valid/model",
		ResponseModel:  "reported\nmodel",
		ResponseID:     strings.Repeat("r", agentLoopMaxIdentifierBytes+1),
		Error:          "raw provider error must not appear",
	})
	for _, key := range []string{"requested_model", "response_model", "response_id"} {
		value, _ := event.Data[key].(string)
		if !strings.HasPrefix(value, "sha256:") {
			t.Fatalf("%s = %#v, want fingerprint in %+v", key, event.Data[key], event.Data)
		}
	}
	if event.Data["selected_model"] != "valid/model" {
		t.Fatalf("selected_model = %#v, want preserved valid id", event.Data["selected_model"])
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal event: %v", err)
	}
	if strings.Contains(string(encoded), raw) || strings.Contains(string(encoded), "reported\nmodel") || strings.Contains(string(encoded), "raw provider error") {
		t.Fatalf("event leaked raw hostile metadata: %s", encoded)
	}
	if len(encoded) > agentLoopMaxSerializedBytes {
		t.Fatalf("event length = %d, want <= %d", len(encoded), agentLoopMaxSerializedBytes)
	}
}

func TestNewAgentLoopObserverFullyPopulatedIdentityStaysWithinSchemaLimit(t *testing.T) {
	maxID := strings.Repeat("a", agentLoopMaxIdentifierBytes)
	success := true
	event := observeAgentLoopEvent(t, lifecycle.Event{
		Sequence:                    ^uint64(0),
		Type:                        lifecycle.ModelError,
		Timestamp:                   time.Unix(44, 0).UTC(),
		RunID:                       maxID,
		SessionID:                   maxID,
		TaskID:                      maxID,
		TurnID:                      maxID,
		StepID:                      maxID,
		ModelID:                     maxID,
		ProviderID:                  maxID,
		RequestedModel:              maxID,
		SelectedModel:               maxID,
		ResponseModel:               maxID,
		ResponseID:                  maxID,
		ExecutionIdentityConflicted: true,
		ToolName:                    maxID,
		ToolCallID:                  maxID,
		Round:                       agentLoopMaxCounter + 1,
		Attempt:                     agentLoopMaxCounter + 2,
		RunAttempt:                  agentLoopMaxCounter + 3,
		Continuation:                true,
		Phase:                       "unexpected-but-valid-phase",
		Replayed:                    true,
		Success:                     &success,
		Usage:                       lifecycle.Usage{PromptTokens: agentLoopMaxCounter + 4, CompletionTokens: agentLoopMaxCounter + 5, TotalTokens: agentLoopMaxCounter + 6, Estimated: true},
		CostUSD:                     math.MaxFloat64,
		Status:                      "unexpected-but-valid-status",
		FinishReason:                strings.Repeat("unknown_finish_", 100),
		StopReason:                  strings.Repeat("unknown_stop_", 100),
		Error:                       strings.Repeat("provider error ", 200),
	})
	if event.Data["projection"] == "size_limited" {
		t.Fatalf("fully populated schema fell back to size_limited: %+v", event.Data)
	}
	for _, key := range []string{"requested_model", "selected_model", "response_model", "response_id"} {
		if got := event.Data[key]; got != maxID {
			t.Fatalf("%s = %#v, want full identifier retained", key, got)
		}
	}
	for _, key := range []string{"round", "attempt", "run_attempt", "prompt_tokens", "completion_tokens", "total_tokens"} {
		if got := event.Data[key]; got != agentLoopMaxCounter {
			t.Fatalf("%s = %#v, want capped counter", key, got)
		}
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Marshal event: %v", err)
	}
	if len(encoded) > agentLoopMaxSerializedBytes {
		t.Fatalf("event length = %d, want <= %d", len(encoded), agentLoopMaxSerializedBytes)
	}
	for _, key := range []string{"finish_fingerprint", "stop_fingerprint"} {
		if got, _ := event.Data[key].(string); !strings.HasPrefix(got, "sha256:") {
			t.Fatalf("%s = %#v, want fingerprint", key, event.Data[key])
		}
	}
	if event.Data["success"] != true || event.Data["finish_code"] != "other" || event.Data["stop_code"] != "other" ||
		event.Data["phase"] != "unknown" || event.Data["status"] != "unknown" {
		t.Fatalf("categorical edge metadata = %+v", event.Data)
	}
}

func TestNewAgentLoopObserverMapsRunAttemptBoundaries(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	ch, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	observer := NewAgentLoopObserver(hub)
	observer(lifecycle.Event{Type: lifecycle.AttemptStart, RunAttempt: 2, Continuation: true})
	observer(lifecycle.Event{Type: lifecycle.AttemptEnd, RunAttempt: 2, Continuation: true, Status: "conclusive"})

	started := <-ch
	ended := <-ch
	if started.Type != EventAgentAttemptStarted || ended.Type != EventAgentAttemptEnded {
		t.Fatalf("attempt telemetry types = %q, %q", started.Type, ended.Type)
	}
	if started.Data["run_attempt"] != 2 || started.Data["continuation"] != true || ended.Data["status"] != "conclusive" {
		t.Fatalf("attempt telemetry projection started=%+v ended=%+v", started.Data, ended.Data)
	}
}

func observeAgentLoopEvent(t *testing.T, source lifecycle.Event) Event {
	t.Helper()
	hub := NewHub()
	defer hub.Close()
	ch, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	NewAgentLoopObserver(hub)(source)
	select {
	case event := <-ch:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for lifecycle telemetry")
		return Event{}
	}
}
