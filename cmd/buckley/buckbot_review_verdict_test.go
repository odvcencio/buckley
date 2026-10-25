package main

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot/commands"
)

func TestFormatBuckbotGitHubReview_ApprovalEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, build, tests, marker, verdict string
	}{
		{"verified", "PASS", "PASS", "[!TIP]", "POSITIVE REVIEW · CHECK MERGE GATES"},
		{"no test gate", "PASS", "NOT_APPLICABLE", "[!TIP]", "POSITIVE REVIEW · CHECK MERGE GATES"},
		{"unavailable", "UNAVAILABLE", "UNAVAILABLE", "[!NOTE]", "CONDITIONAL APPROVAL · VERIFICATION REQUIRED"},
		{"pending", "PENDING", "PENDING", "[!NOTE]", "CONDITIONAL APPROVAL · VERIFICATION REQUIRED"},
		{"failing", "FAIL", "FAIL", "[!CAUTION]", "REVIEW CONFLICTS WITH FAILED VERIFICATION"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := &commands.ParsedReview{
				Grade: commands.GradeA, Approved: true,
				BuildVerification: commands.VerificationState(tc.build),
				TestVerification:  commands.VerificationState(tc.tests),
			}
			body := formatBuckbotGitHubReview(parsed, "## Summary\nInspected the change.", "1234567890abcdef")
			if !strings.Contains(body, tc.marker) || !strings.Contains(body, tc.verdict) {
				t.Fatalf("review label = %q, want %q and %q", body, tc.marker, tc.verdict)
			}
			if strings.Contains(body, "READY TO MERGE") {
				t.Fatalf("Buckbot claimed merge readiness without checking merge gates: %q", body)
			}
		})
	}
}
