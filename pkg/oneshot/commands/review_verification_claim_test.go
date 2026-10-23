package commands

import (
	"m31labs.dev/buckley/pkg/oneshot"
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
	for _, tc := range []struct {
		name    string
		report  *VerificationReport
		state   VerificationState
		wantErr bool
	}{
		{"passing", &VerificationReport{Covered: []EvidenceItem{{Label: "go test ./..."}}}, VerificationPass, false},
		{"partial", &VerificationReport{Covered: []EvidenceItem{{Label: "go test ./..."}}, Unverified: []string{"other.go"}}, VerificationPass, true},
		{"documentation only", &VerificationReport{NotRequired: []string{"README.md"}}, VerificationPass, true},
		{"failing", &VerificationReport{Failing: []EvidenceItem{{Label: "go test ./..."}}}, VerificationFail, false},
		{"passing cannot claim failure", &VerificationReport{Covered: []EvidenceItem{{Label: "go test ./..."}}}, VerificationFail, true},
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
