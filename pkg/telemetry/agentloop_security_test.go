package telemetry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/agentloop/lifecycle"
)

func TestNewAgentLoopObserverProjectsFreeFormTextToBoundedMetadata(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	ch, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	sentinels := []string{
		"PROMPT_SENTINEL_do_not_publish",
		"TOOL_ARGS_SENTINEL_do_not_publish",
		"TOOL_RESULT_SENTINEL_do_not_publish",
		"MODEL_OUTPUT_SENTINEL_do_not_publish",
		"FINAL_ANSWER_SENTINEL_do_not_publish",
		"sk-12345678901234567890",
	}
	nested := map[string]any{
		"promptText": sentinels[0],
		"tool_args": []any{
			map[string]any{"toolArguments": sentinels[1]},
			map[string]any{"tool-result": sentinels[2]},
		},
		"provider": map[string]any{
			"modelOutput":   sentinels[3],
			"final_answer":  sentinels[4],
			"authorization": "Bearer " + sentinels[5],
		},
	}
	nestedJSON, err := json.Marshal(nested)
	if err != nil {
		t.Fatalf("marshal nested fixture: %v", err)
	}
	invalid := string([]byte{0xff, 0xfe, 0xfd})
	longUnicode := strings.Repeat("U0001f9ea", 5000)
	freeForm := string(nestedJSON) + invalid + longUnicode

	NewAgentLoopObserver(hub)(lifecycle.Event{
		Sequence:     1,
		Type:         lifecycle.ModelError,
		Timestamp:    time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		SessionID:    "session " + invalid + longUnicode,
		TaskID:       "task " + invalid + longUnicode,
		TurnID:       "turn " + invalid + longUnicode,
		Phase:        "provider phase " + sentinels[3],
		ModelID:      "model " + invalid + longUnicode,
		ProviderID:   "provider " + invalid + longUnicode,
		ToolName:     "tool " + invalid + longUnicode,
		ToolCallID:   "call " + invalid + longUnicode,
		Status:       sentinels[4],
		FinishReason: freeForm,
		StopReason:   freeForm,
		Error:        freeForm,
		EvidenceIDs:  []string{sentinels[0], sentinels[1], sentinels[2], sentinels[3], sentinels[4]},
	})

	event := <-ch
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal telemetry: %v", err)
	}
	if !utf8.Valid(encoded) {
		t.Fatalf("serialized telemetry is invalid UTF-8: %q", encoded)
	}
	if len(encoded) > agentLoopMaxSerializedBytes {
		t.Fatalf("serialized telemetry length = %d, want <= %d", len(encoded), agentLoopMaxSerializedBytes)
	}
	for _, sentinel := range sentinels {
		if strings.Contains(string(encoded), sentinel) {
			t.Fatalf("serialized telemetry leaked %q: %s", sentinel, encoded)
		}
	}
	if strings.Contains(string(encoded), "�") {
		t.Fatalf("serialized telemetry retained invalid UTF-8 replacement text: %s", encoded)
	}
	for _, forbidden := range []string{"error", "stop_reason", "finish_reason", "evidence_ids"} {
		if _, ok := event.Data[forbidden]; ok {
			t.Fatalf("raw field %q present in %#v", forbidden, event.Data)
		}
	}
	if event.Data["error_code"] != "model_error" || event.Data["stop_code"] != "other" || event.Data["finish_code"] != "other" {
		t.Fatalf("categorical projection = %#v", event.Data)
	}
	if event.Data["phase"] != "unknown" || event.Data["status"] != "unknown" {
		t.Fatalf("unknown enum projection = %#v", event.Data)
	}
	for _, key := range []string{"error_fingerprint", "stop_fingerprint", "finish_fingerprint"} {
		value, ok := event.Data[key].(string)
		if !ok || len(value) != len("sha256:")+32 || !strings.HasPrefix(value, "sha256:") {
			t.Fatalf("%s = %#v, want bounded fingerprint", key, event.Data[key])
		}
	}
	for key, raw := range event.Data {
		if value, ok := raw.(string); ok && len(value) > agentLoopMaxIdentifierBytes {
			t.Fatalf("%s string length = %d, want <= %d", key, len(value), agentLoopMaxIdentifierBytes)
		}
	}
}

func TestNewAgentLoopObserverRetainsOnlyAllowlistedReasons(t *testing.T) {
	hub := NewHub()
	defer hub.Close()
	ch, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	observer := NewAgentLoopObserver(hub)

	observer(lifecycle.Event{
		Type:         lifecycle.TurnEnd,
		FinishReason: "loop_guard",
		StopReason:   "finalization_failed",
		Status:       "incomplete",
	})
	known := <-ch
	if known.Data["finish_code"] != "loop_guard" || known.Data["stop_code"] != "finalization_failed" || known.Data["status"] != "incomplete" {
		t.Fatalf("known enum projection = %#v", known.Data)
	}
	if _, ok := known.Data["stop_fingerprint"]; ok {
		t.Fatalf("known stop reason unexpectedly fingerprinted: %#v", known.Data)
	}

	observer(lifecycle.Event{
		Type:         lifecycle.TurnEnd,
		FinishReason: "provider said " + strings.Repeat("x", 512),
		StopReason:   "tool output said " + strings.Repeat("y", 512),
		Status:       "provider-defined",
	})
	unknown := <-ch
	if unknown.Data["finish_code"] != "other" || unknown.Data["stop_code"] != "other" || unknown.Data["status"] != "unknown" {
		t.Fatalf("unknown enum projection = %#v", unknown.Data)
	}
}

func TestNewAgentLoopObserverNilAndClosedHubAreSafe(t *testing.T) {
	observer := NewAgentLoopObserver(nil)
	if observer == nil {
		t.Fatal("observer = nil, want no-op observer")
	}
	observer(lifecycle.Event{Type: lifecycle.TurnStart})

	hub := NewHub()
	hub.Close()
	NewAgentLoopObserver(hub)(lifecycle.Event{
		Type:       lifecycle.TurnEnd,
		Error:      "must be dropped after close",
		StopReason: "finalization_failed",
	})
}
