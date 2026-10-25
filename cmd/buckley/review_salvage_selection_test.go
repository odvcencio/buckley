package main

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestReviewResultFromAgent_PreservesEarlierCompleteFinding(t *testing.T) {
	first := salvageSelectionReport("earlier complete report", true)
	last := salvageSelectionReport("later rejected report", false)
	// The evidence lies beyond the existing 4,000-rune diagnostic excerpt.
	if strings.Index(first, "retained evidence beyond excerpt") < 4000 {
		t.Fatal("fixture does not exercise excerpt loss")
	}
	result := reviewResultFromAgent(salvageSelectionRun(last, true,
		salvageSelectionAttempt("primary", 1, first),
		salvageSelectionAttempt("primary", 2, last)), nil)
	if !strings.Contains(result.reviewText, "retained evidence beyond excerpt") || !strings.Contains(result.reviewText, "Preserved Primary attempt 1") {
		t.Fatalf("earlier finding was lost from the incomplete artifact: %s", result.reviewText)
	}
	if result.parsed != nil || !result.incomplete || !strings.Contains(result.reviewText, "must not be used as an approval") {
		t.Fatal("salvage must remain incomplete and cannot expose an approval")
	}
	if strings.Count(result.reviewText, "earlier complete report") != 1 || !strings.Contains(result.reviewText, "Response is preserved above.") {
		t.Fatal("diagnostics repeated the report already preserved in full")
	}
}

func TestAppendReviewAttemptDiagnostics_DoesNotRepeatPreservedResponse(t *testing.T) {
	trace := &transparency.Trace{Attempts: []transparency.TraceAttempt{
		salvageSelectionAttempt("primary", 1, "earlier rejected draft"),
		salvageSelectionAttempt("primary", 2, "preserved complete response"),
	}}
	got := appendReviewAttemptDiagnostics("preserved complete response", trace)
	if strings.Count(got, "preserved complete response") != 1 || !strings.Contains(got, "earlier rejected draft") || !strings.Contains(got, "### Primary attempt 2") {
		t.Fatalf("diagnostics lost attribution or duplicated the selected response: %s", got)
	}
}

func TestReviewResultFromAgent_SalvageSelection(t *testing.T) {
	complete := salvageSelectionReport("complete earlier report", false)
	richer := salvageSelectionReport("earlier finding report", true)
	latest := salvageSelectionReport("latest report", false)
	sparse := "## Grade: B\n\n## Summary\nSparse rejected report.\n"
	for _, tc := range []struct {
		name       string
		current    string
		attempts   []transparency.TraceAttempt
		incomplete bool
		critic     int
		want       string
		preserved  bool
	}{
		{"section completeness precedes recency", sparse, []transparency.TraceAttempt{salvageSelectionAttempt("primary", 1, complete), salvageSelectionAttempt("primary", 2, sparse)}, true, 0, "complete earlier report", true},
		{"equally complete reports prefer latest", latest, []transparency.TraceAttempt{salvageSelectionAttempt("primary", 1, complete), salvageSelectionAttempt("primary", 2, latest)}, true, 0, "latest report", false},
		{"successful repair stays authoritative", latest, []transparency.TraceAttempt{salvageSelectionAttempt("primary", 1, richer), salvageSelectionAttempt("primary", 2, latest)}, false, 0, "latest report", false},
		{"failed critic is not replaced by primary", sparse, []transparency.TraceAttempt{salvageSelectionAttempt("primary", 1, richer), salvageSelectionAttempt("approval critic", 1, sparse)}, true, 1, "Sparse rejected report.", false},
		{"critic repairs retain earlier critic findings", latest, []transparency.TraceAttempt{salvageSelectionAttempt("primary", 1, complete), salvageSelectionAttempt("approval critic", 1, richer), salvageSelectionAttempt("approval critic", 2, latest)}, true, 2, "retained evidence beyond excerpt", true},
		{"no trace keeps current report", latest, nil, true, 0, "latest report", false},
		{"unparseable trace keeps current report", latest, []transparency.TraceAttempt{salvageSelectionAttempt("primary", 1, "not a report")}, true, 0, "latest report", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := salvageSelectionRun(tc.current, tc.incomplete, tc.attempts...)
			run.CriticAttempts = tc.critic
			result := reviewResultFromAgent(run, nil)
			body := strings.Split(result.reviewText, "## Review attempt diagnostics")[0]
			if !strings.Contains(body, tc.want) || strings.Contains(body, "> Preserved ") != tc.preserved {
				t.Fatalf("unexpected selected report: %s", body)
			}
			if !tc.incomplete && (strings.Contains(body, "retained evidence beyond excerpt") || result.parsed == nil) {
				t.Fatal("successful repair was replaced by rejected findings")
			}
			if tc.incomplete && result.parsed != nil {
				t.Fatal("incomplete result exposed a parsed verdict")
			}
		})
	}
}

func salvageSelectionAttempt(phase string, ordinal int, review string) transparency.TraceAttempt {
	return transparency.TraceAttempt{Phase: phase, Attempt: ordinal, ValidationError: "rejected", Trace: &transparency.Trace{Content: review}}
}

func salvageSelectionRun(review string, incomplete bool, attempts ...transparency.TraceAttempt) *oneshot.RunResult {
	return &oneshot.RunResult{Value: &commands.ReviewAgentResult{Review: review, Parsed: commands.ParseReview(review)}, Incomplete: incomplete, IncompleteReason: "validation exhausted", Trace: &transparency.Trace{Attempts: attempts}}
}

func salvageSelectionReport(summary string, finding bool) string {
	body := "## Grade: B\n\n## Summary\n" + summary + "\n\n## Build & Test Status\n- **Build**: UNAVAILABLE\n- **Tests**: UNAVAILABLE\n\n## Coverage\n- **File**: `example.go` — inspected.\n- **Feedback disposition**: NONE_SUPPLIED\n\n## Invariant Audit\n" + strings.Repeat("Inspected one stated invariant. ", 170) + "\n\n## Falsification\n- **Conclusion**: UNRESOLVED — more evidence needed.\n\n## Findings\n"
	if finding {
		body += "### FINDING-001: Keep completed evidence\n- **Severity**: MINOR\n- **File**: `example.go:12`\n- **Evidence**: retained evidence beyond excerpt\n- **Impact**: a useful finding disappears after repair\n- **Fix**: retain the complete prior report\n"
	} else {
		body += "None.\n"
	}
	return body + "\n## Verdict\n- **Decision**: NEEDS DISCUSSION\n- **Blockers**: none\n- **Suggestions**: none\n"
}
