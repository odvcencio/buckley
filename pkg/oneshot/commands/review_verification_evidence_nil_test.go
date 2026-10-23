package commands

import "testing"

func TestVerificationReportFailingSummary_NilReport(t *testing.T) {
	var report *VerificationReport
	if got := report.FailingSummary(); got != nil {
		t.Fatalf("FailingSummary() on nil report = %v, want nil", got)
	}
}
