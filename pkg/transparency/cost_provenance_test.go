package transparency

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCostProvenance_SummaryRetainsKnownSubtotal(t *testing.T) {
	ledger := NewCostLedger()
	ledger.Record(CostEntry{Timestamp: time.Now().Add(-48 * time.Hour), Cost: 2, CostUnknown: true, Tokens: TokenUsage{Input: 10}})
	ledger.Record(CostEntry{Cost: 3, Tokens: TokenUsage{Output: 20}})
	summary := ledger.Summary()
	if summary.SessionCost != 5 || !summary.SessionCostUnknown || summary.TodayCost != 3 || summary.TodayCostUnknown || summary.InvocationCount != 2 || summary.SessionTokens.Total() != 30 {
		t.Fatalf("mixed historical summary = %+v", summary)
	}
	ledger.Record(CostEntry{CostUnknown: true})
	summary = ledger.Summary()
	if summary.SessionCost != 5 || !summary.TodayCostUnknown || summary.TodayCost != 3 || summary.InvocationCount != 3 {
		t.Fatalf("mixed current summary = %+v", summary)
	}
}

func TestCostProvenance_AggregateAndJSON(t *testing.T) {
	for _, unknownFirst := range []bool{false, true} {
		first := &Trace{Cost: 2, CostUnknown: unknownFirst}
		second := &Trace{Cost: 3, CostUnknown: !unknownFirst}
		aggregate := AggregateTraceAttempts([]TraceAttempt{{Trace: first}, {Trace: second}})
		if aggregate == nil || aggregate.Cost != 5 || !aggregate.CostUnknown {
			t.Fatalf("aggregate = %+v", aggregate)
		}
		if first.Cost != 2 || first.CostUnknown != unknownFirst || second.Cost != 3 || second.CostUnknown == unknownFirst {
			t.Fatal("aggregation mutated source traces")
		}
		raw, err := json.Marshal(aggregate)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Trace
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if !decoded.CostUnknown || decoded.Cost != 5 {
			t.Fatalf("roundtrip = %+v", decoded)
		}
	}
}

func TestCostProvenance_KnownZeroIsNotUnknown(t *testing.T) {
	trace := NewTraceBuilder("free", "free-model", "test").WithCostUnknown(false).Complete(TokenUsage{Input: 10}, 0)
	ledger := NewCostLedger()
	ledger.Record(CostEntry{Tokens: trace.Tokens, Cost: trace.Cost, CostUnknown: trace.CostUnknown})
	summary := ledger.Summary()
	if trace.CostUnknown || summary.SessionCostUnknown || summary.TodayCostUnknown || summary.SessionCost != 0 || summary.InvocationCount != 1 {
		t.Fatalf("known zero lost provenance: trace=%+v summary=%+v", trace, summary)
	}
}
