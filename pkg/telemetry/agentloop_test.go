package telemetry

import (
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
		Sequence:     7,
		Type:         lifecycle.ModelError,
		Timestamp:    time.Unix(42, 0).UTC(),
		RunID:        "run-1",
		SessionID:    "session-1",
		TaskID:       "task-1",
		TurnID:       "turn-1",
		Round:        2,
		Attempt:      3,
		RunAttempt:   4,
		Continuation: true,
		Phase:        "model",
		ModelID:      "deepseek/deepseek-v4-pro-0813",
		ProviderID:   "openrouter",
		Error:        "provider failed after arbitrary output",
		FinishReason: "model_error",
		StopReason:   "model_error",
		Usage:        lifecycle.Usage{PromptTokens: 11, CompletionTokens: 13, TotalTokens: 24},
	})

	select {
	case event := <-ch:
		if event.Type != EventAgentModelError || event.SessionID != "session-1" || event.TaskID != "task-1" {
			t.Fatalf("event = %+v", event)
		}
		if event.Data["model"] != "deepseek/deepseek-v4-pro-0813" || event.Data["provider"] != "openrouter" {
			t.Fatalf("identity metadata = %+v", event.Data)
		}
		if event.Data["round"] != 2 || event.Data["attempt"] != 3 || event.Data["run_attempt"] != 4 || event.Data["continuation"] != true || event.Data["sequence"] != uint64(7) {
			t.Fatalf("ordering metadata = %+v", event.Data)
		}
		if event.Data["prompt_tokens"] != 11 || event.Data["total_tokens"] != 24 {
			t.Fatalf("usage metadata = %+v", event.Data)
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
