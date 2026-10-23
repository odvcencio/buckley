package commands

import (
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/verifyreceipt"
	"testing"
)

func TestSuppliedEvidence_CannotInventVerificationResults(t *testing.T) {
	for _, state := range []VerificationState{VerificationPass, VerificationFail} {
		t.Run(string(state), func(t *testing.T) {
			parsed := baseParsedReview(GradeC, false)
			parsed.BuildVerification, parsed.TestVerification = state, state
			result := &ReviewAgentResult{Parsed: parsed}
			for _, def := range []interface {
				ValidateAgentExecution(any, *oneshot.AgentResult) error
			}{
				ReviewBranchDef{SuppliedEvidenceOnly: true}, ReviewPRDef{SuppliedEvidenceOnly: true},
			} {
				if err := def.ValidateAgentExecution(result, nil); err == nil {
					t.Fatalf("unsupported %s verification claim accepted by %T", state, def)
				}
			}
		})
	}
}

func TestSuppliedEvidence_ClaimsMatchReport(t *testing.T) {
	paired := func(status verifyreceipt.Status) *VerificationReport {
		build, tests := receipt("ev_build", status, 0, "**"), receipt("ev_tests", status, 0, "**")
		build.Argv, tests.Argv = []string{"go", "build", "./..."}, []string{"go", "test", "./..."}
		if status == verifyreceipt.StatusFail {
			build.ExitCode, tests.ExitCode = 1, 1
		}
		report := EvaluateVerification(VerificationInput{Mode: VerifyNone, Files: []string{"a.go"}, Receipts: []verifyreceipt.Receipt{build, tests}})
		return &report
	}
	partial := paired(verifyreceipt.StatusPass)
	partial.Unverified = []string{"other.go"}
	for _, tc := range []struct {
		name    string
		report  *VerificationReport
		state   VerificationState
		wantErr bool
	}{
		{"passing", paired(verifyreceipt.StatusPass), VerificationPass, false},
		{"partial", partial, VerificationPass, true},
		{"documentation only", &VerificationReport{NotRequired: []string{"README.md"}}, VerificationPass, true},
		{"failing", paired(verifyreceipt.StatusFail), VerificationFail, false},
		{"passing cannot claim failure", paired(verifyreceipt.StatusPass), VerificationFail, true},
		{"unavailable", nil, VerificationUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed := baseParsedReview(GradeC, false)
			parsed.BuildVerification, parsed.TestVerification = tc.state, tc.state
			err := validateReviewSuppliedEvidence(&ReviewAgentResult{Parsed: parsed}, tc.report)
			if (err != nil) != tc.wantErr {
				t.Fatalf("claim %s: %v, want error %v", tc.state, err, tc.wantErr)
			}
		})
	}
}
