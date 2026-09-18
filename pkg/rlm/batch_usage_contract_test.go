package rlm

import (
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/transparency"
)

func TestCloneBatchResultPreservesRichUsageAndPointerIsolation(t *testing.T) {
	reasoning := 5
	cached := 7
	original := BatchResult{TaskID: "task-1", Usage: transparency.TokenUsage{
		Input:                     10,
		Output:                    4,
		ReportedTotal:             14,
		ReportedReasoning:         &reasoning,
		ReportedCachedInput:       &cached,
		UsageEvidenceMissing:      true,
		ReportedCacheWrite:        2,
		ReportedUsageInconsistent: true,
	}}
	cloned := cloneBatchResult(original)
	if !reflect.DeepEqual(cloned.Usage, original.Usage) {
		t.Fatalf("cloned usage = %#v, want %#v", cloned.Usage, original.Usage)
	}
	*original.Usage.ReportedReasoning = 99
	*original.Usage.ReportedCachedInput = 100
	if *cloned.Usage.ReportedReasoning != 5 || *cloned.Usage.ReportedCachedInput != 7 {
		t.Fatalf("clone aliases optional usage fields: %+v", cloned.Usage)
	}
}

func TestBatchResultUsageAggregationUsesAddTokenUsage(t *testing.T) {
	reasoning := 2
	first := transparency.TokenUsage{Input: 100, Output: 50, ReportedTotal: 150, ReportedReasoning: &reasoning}
	second := transparency.TokenUsage{UsageEvidenceMissing: true, ReportedCacheWrite: 3}
	var got transparency.TokenUsage
	got = transparency.AddTokenUsage(got, first)
	got = transparency.AddTokenUsage(got, second)
	if got.Total() != 150 || got.ReportedReasoning == nil || *got.ReportedReasoning != 2 || !got.UsageEvidenceMissing || got.ReportedCacheWrite != 3 {
		t.Fatalf("aggregated usage = %+v, want rich fields preserved", got)
	}
}
