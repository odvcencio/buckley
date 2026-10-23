package commands

import (
	"fmt"
	"strings"
)

func validateReviewSuppliedEvidence(result any, report *VerificationReport) error {
	review, ok := result.(*ReviewAgentResult)
	if !ok || review == nil || review.Parsed == nil {
		return fmt.Errorf("supplied-evidence review requires a parsed review result")
	}
	passing := report != nil && len(report.Covered) > 0 && report.Qualifier() == "verified"
	failing := report != nil && len(report.Failing) > 0
	if review.Parsed.Approved && failing {
		return fmt.Errorf("an approval is inconsistent with failing verification evidence: %s; report it as a finding and do not approve", strings.Join(report.FailingSummary(), "; "))
	}
	for _, claim := range []struct {
		name  string
		state VerificationState
	}{
		{"Build", review.Parsed.BuildVerification}, {"Tests", review.Parsed.TestVerification},
	} {
		if (claim.state == VerificationPass && !passing) || (claim.state == VerificationFail && !failing) {
			return fmt.Errorf("%s %s has no supporting supplied verification evidence; report UNAVAILABLE", claim.name, claim.state)
		}
	}
	return nil
}
