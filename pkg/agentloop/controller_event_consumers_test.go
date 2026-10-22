package agentloop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/durability/modelstep"
	"m31labs.dev/buckley/pkg/runledger"
)

type recordingEventStore struct {
	runledger.Store
	calls   int
	last    runledger.Event
	failure error
}

func (s *recordingEventStore) Append(_ context.Context, event runledger.Event) (runledger.Event, error) {
	s.calls++
	s.last = event
	return event, s.failure
}

func TestController_RecordEventConsumers(t *testing.T) {
	for _, tc := range []struct {
		name                                         string
		ledger, observer, panicObserver, appendFails bool
	}{
		{name: "none"},
		{name: "observer", observer: true},
		{name: "ledger", ledger: true},
		{name: "both", ledger: true, observer: true},
		{name: "observer_panic", ledger: true, observer: true, panicObserver: true},
		{name: "ledger_failure", ledger: true, observer: true, appendFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const credential = "sk-abcdefghijklmnopqrstuvwxyz123456"
			rawError := strings.Repeat("provider failed with "+credential+"\n", 100)
			rawStderr := "stderr " + credential
			payload := map[string]any{"step_id": "step-1", "phase": "tool", "tool": "read_file", "call_id": "call-1", "model": " model/raw ", "provider_id": " provider/raw ", "round": 2, "attempt": 1, "error": rawError, "stderr": rawStderr, "success": true, "prompt_tokens": 5, "completion_tokens": 3}
			evidenceIDs := []string{"evidence-1"}
			store := &recordingEventStore{}
			cfg := ControllerConfig{RunID: "run-1", SessionID: "session-1", TaskID: "task-1", TurnID: "turn-1"}
			if tc.ledger {
				cfg.RunLedger = store
			}
			if tc.appendFails {
				store.failure = errors.New("ledger unavailable")
			}
			observed := 0
			var live LifecycleEvent
			if tc.observer {
				cfg.LifecycleObserver = func(e LifecycleEvent) {
					observed++
					live = e
					live.EvidenceIDs = append([]string(nil), e.EvidenceIDs...)
					success := *e.Success
					live.Success = &success
					e.EvidenceIDs[0] = "observer edit"
					*e.Success = false
					if tc.panicObserver {
						panic("broken observer")
					}
				}
			}
			ctrl := &Controller{cfg: cfg}
			ctrl.recordEventWithEvidence(t.Context(), runledger.EventToolFailed, payload, evidenceIDs)
			wantObserved, wantLedger := 0, 0
			if tc.observer {
				wantObserved = 1
			}
			if tc.ledger {
				wantLedger = 1
			}
			if observed != wantObserved || store.calls != wantLedger || ctrl.lifecycleSeq != uint64(wantObserved) {
				t.Fatalf("observer=%d ledger=%d sequence=%d", observed, store.calls, ctrl.lifecycleSeq)
			}
			if payload["error"] != rawError || payload["stderr"] != rawStderr || payload["success"] != true || evidenceIDs[0] != "evidence-1" {
				t.Fatal("event delivery mutated caller data")
			}
			if tc.observer {
				if live.Sequence != 1 || live.Type != LifecycleToolError || live.RunID != cfg.RunID || live.SessionID != cfg.SessionID || live.TaskID != cfg.TaskID || live.TurnID != cfg.TurnID {
					t.Fatalf("live identity=%+v", live)
				}
				if live.StepID != "step-1" || live.ToolCallID != "call-1" || live.ToolName != "read_file" || live.ModelID != "model/raw" || live.ProviderID != "provider/raw" || live.Round != 2 || live.Attempt != 1 || live.Usage.TotalTokens != 8 || live.Success == nil || !*live.Success {
					t.Fatalf("live fields=%+v", live)
				}
				if live.Error != modelstep.NormalizeErrorText(rawError) || live.Stderr != modelstep.NormalizeErrorText(rawStderr) || strings.Contains(live.Error, credential) || utf8.RuneCountInString(live.Error) > modelstep.MaxPersistedErrorRunes || live.Timestamp.IsZero() || live.EvidenceIDs[0] != "evidence-1" {
					t.Fatal("live redaction, bounds, or evidence changed")
				}
			}
			if tc.ledger {
				event := store.last
				if event.Type != runledger.EventToolFailed || event.RunID != cfg.RunID || event.SessionID != cfg.SessionID || event.TaskID != cfg.TaskID || event.ModelID != " model/raw " || event.ProviderID != " provider/raw " || event.Timestamp.IsZero() || event.EvidenceIDs[0] != "evidence-1" {
					t.Fatalf("ledger fields=%+v", event)
				}
				if event.Payload["error"] != modelstep.NormalizeErrorText(rawError) || event.Payload["stderr"] != modelstep.NormalizeErrorText(rawStderr) || event.Payload["success"] != true {
					t.Fatal("ledger diagnostics changed")
				}
				if tc.observer && !event.Timestamp.Equal(live.Timestamp) {
					t.Fatal("ledger and live timestamps differ")
				}
				event.Payload["round"] = 99
				if payload["round"] != 2 {
					t.Fatal("ledger payload aliases caller map")
				}
			}
		})
	}
}

func TestController_RecordEventNilReceiver(t *testing.T) {
	var ctrl *Controller
	ctrl.recordEventWithEvidence(t.Context(), runledger.EventToolFailed, map[string]any{"error": "ignored"}, []string{"evidence-1"})
}
