package experiment

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestVariantReportIndexMatchesLinearLookupSemantics(t *testing.T) {
	reports := []VariantReport{
		{VariantID: "v1", RunID: "run-1", VariantName: "first"},
		{VariantID: "v1", RunID: "run-2", VariantName: "duplicate variant"},
		{VariantID: "collision", RunID: "run-3", VariantName: "variant collision"},
		{VariantID: "v4", RunID: "collision", VariantName: "run collision wins"},
		{VariantID: "v5", RunID: "run-5", VariantName: "first duplicate run"},
		{VariantID: "v6", RunID: "run-5", VariantName: "second duplicate run"},
		{VariantID: "v7", RunID: "", VariantName: "empty run id wins"},
		{VariantID: "", RunID: "run-8", VariantName: "empty variant id fallback"},
	}
	index := newVariantReportIndex(reports)
	for _, id := range []string{"run-1", "run-2", "v1", "collision", "run-5", "missing", ""} {
		got := index.find(id)
		want := oldLinearVariantReportLookup(reports, id)
		if got != want {
			t.Fatalf("index.find(%q) = %v, want %v", id, reportName(got), reportName(want))
		}
	}
}

func TestRenderResultsTableUsesRankingOrderAndSkipsMissingRefs(t *testing.T) {
	report := &ComparisonReport{
		Variants: []VariantReport{
			{
				VariantID:          "variant-1",
				RunID:              "01M1B7JQ8QAAAAAAAAAAAAA001",
				VariantName:        "release-a",
				ModelID:            "openai/gpt-5.6-luna-pro:2026-09-04",
				Status:             RunCompleted,
				Metrics:            RunMetrics{PromptTokens: 10, CompletionTokens: 5, TotalCost: 0.01, DurationMs: 1000},
				CriteriaScore:      1,
				VerificationStatus: "verified",
				RankEligible:       true,
				InputDigest:        "73c5c776b62806e847e44b1cbab9b80625289113830a2659cd027b456c7c6fec",
			},
			{
				VariantID:          "variant-1",
				RunID:              "01M1B7JQ8QAAAAAAAAAAAAA003",
				VariantName:        "release-a-repeat",
				ModelID:            "openai/gpt-5.6-luna-pro:2026-09-04-current",
				Status:             RunCompleted,
				Metrics:            RunMetrics{PromptTokens: 7, CompletionTokens: 3, TotalCost: 0.02, DurationMs: 2000},
				VerificationStatus: "unverified: missing automated evaluation",
			},
			{
				VariantID:          "variant-2",
				RunID:              "01M1B7JQ8QAAAAAAAAAAAAA002",
				VariantName:        "release-b",
				ModelID:            "openai/gpt-5.6-luna-pro:2026-09-05",
				Status:             RunFailed,
				Metrics:            RunMetrics{PromptTokens: 8, CompletionTokens: 2},
				VerificationStatus: "unverified: missing automated evaluation",
			},
		},
		Rankings: []Ranking{
			{RunID: "missing-run", Rank: 1},
			{RunID: "01M1B7JQ8QAAAAAAAAAAAAA003"},
			{RunID: "01M1B7JQ8QAAAAAAAAAAAAA001", Rank: 1, Winner: true},
			{RunID: "01M1B7JQ8QAAAAAAAAAAAAA002"},
		},
	}

	var out bytes.Buffer
	renderer := NewTerminalReporterWithOutput(&out, nil)
	renderer.SetNoColor(true)
	renderer.renderResultsTable(report)
	got := out.String()
	if strings.Contains(got, "missing-run") {
		t.Fatalf("render output included missing ranking reference:\n%s", got)
	}
	first := strings.Index(got, "01M1B7JQ8QAAAAAAAAAAAAA003")
	second := strings.Index(got, "01M1B7JQ8QAAAAAAAAAAAAA001")
	third := strings.Index(got, "01M1B7JQ8QAAAAAAAAAAAAA002")
	if first < 0 || second < 0 || third < 0 || !(first < second && second < third) {
		t.Fatalf("render output did not preserve ranking order:\n%s", got)
	}
	for _, want := range []string{
		"openai/gpt-5.6-luna-pro:2026-09-04",
		"openai/gpt-5.6-luna-pro:2026-09-04-current",
		"openai/gpt-5.6-luna-pro:2026-09-05",
		"73c5c776b628",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("render output missing %q:\n%s", want, got)
		}
	}
}

func BenchmarkVariantReportLookup(b *testing.B) {
	for _, size := range []int{100, 1000, 10000} {
		reports, rankings := syntheticVariantReportLookupFixture(size)
		b.Run(fmt.Sprintf("linear/%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var found int
				for _, ranking := range rankings {
					if oldLinearVariantReportLookup(reports, ranking.RunID) != nil {
						found++
					}
				}
				if found != len(rankings) {
					b.Fatalf("found %d reports, want %d", found, len(rankings))
				}
			}
		})
		b.Run(fmt.Sprintf("indexed/%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				index := newVariantReportIndex(reports)
				var found int
				for _, ranking := range rankings {
					if index.find(ranking.RunID) != nil {
						found++
					}
				}
				if found != len(rankings) {
					b.Fatalf("found %d reports, want %d", found, len(rankings))
				}
			}
		})
	}
}

func syntheticVariantReportLookupFixture(size int) ([]VariantReport, []Ranking) {
	reports := make([]VariantReport, size)
	rankings := make([]Ranking, size)
	for i := 0; i < size; i++ {
		runID := fmt.Sprintf("run-%06d", i)
		reports[i] = VariantReport{
			VariantID:          fmt.Sprintf("variant-%06d", i/2),
			RunID:              runID,
			VariantName:        fmt.Sprintf("variant-%06d", i/2),
			ModelID:            fmt.Sprintf("provider/model:%06d", i),
			Status:             RunCompleted,
			VerificationStatus: "verified",
			RankEligible:       true,
		}
		rankings[i] = Ranking{RunID: runID, Rank: i + 1}
	}
	return reports, rankings
}

func oldLinearVariantReportLookup(reports []VariantReport, id string) *VariantReport {
	for i := range reports {
		if reports[i].RunID == id {
			return &reports[i]
		}
	}
	for i := range reports {
		if reports[i].VariantID == id {
			return &reports[i]
		}
	}
	return nil
}

func reportName(report *VariantReport) string {
	if report == nil {
		return "<nil>"
	}
	return report.VariantName
}
