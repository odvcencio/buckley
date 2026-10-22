package agentloop

import (
	"context"
	"m31labs.dev/buckley/pkg/runledger"
	"strings"
	"testing"
)

func BenchmarkRecordEventWithEvidence_Consumers(b *testing.B) {
	for _, mode := range []string{"none", "observer", "ledger", "both"} {
		for _, size := range []string{"small", "diagnostic"} {
			b.Run(mode+"/"+size, func(b *testing.B) {
				payload := map[string]any{"step_id": "step-1", "phase": "tool", "tool": "read_file", "call_id": "call-1", "model": "profile/model", "round": 2, "attempt": 1, "success": true}
				if size == "diagnostic" {
					text := strings.Repeat("source failure sk-abcdefghijklmnopqrstuvwxyz123456\n", 400)
					payload["error"] = text
					payload["stderr"] = text
				}
				ledger := &recordingEventStore{}
				cfg := ControllerConfig{RunID: "run-1", SessionID: "session-1", TaskID: "task-1", TurnID: "turn-1"}
				observed := 0
				if mode == "observer" || mode == "both" {
					cfg.LifecycleObserver = func(LifecycleEvent) { observed++ }
				}
				if mode == "ledger" || mode == "both" {
					cfg.RunLedger = ledger
				}
				ctrl := &Controller{cfg: cfg}
				ids := []string{"evidence-1", "evidence-2"}
				ctx := context.Background()
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					ctrl.recordEventWithEvidence(ctx, runledger.EventToolFailed, payload, ids)
				}
				b.StopTimer()
				if (mode == "observer" || mode == "both") && observed != b.N {
					b.Fatal("observer delivery changed")
				}
				if (mode == "ledger" || mode == "both") && ledger.calls != b.N {
					b.Fatal("ledger delivery changed")
				}
			})
		}
	}
}
