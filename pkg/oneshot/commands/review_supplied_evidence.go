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
	failing := report != nil && len(report.Failing) > 0
	if review.Parsed.Approved && failing {
		return fmt.Errorf("an approval is inconsistent with failing verification evidence: %s; report it as a finding and do not approve", strings.Join(report.FailingSummary(), "; "))
	}
	for _, claim := range []struct {
		name  string
		kind  string
		state VerificationState
	}{
		{"Build", reviewEvidenceBuild, review.Parsed.BuildVerification}, {"Tests", reviewEvidenceTest, review.Parsed.TestVerification},
	} {
		supported := report.checkStatus(claim.kind)
		if (claim.state == VerificationPass || claim.state == VerificationFail) && claim.state != supported {
			return fmt.Errorf("%s %s has no supporting supplied verification evidence for that check; report %s", claim.name, claim.state, supported)
		}
	}
	return nil
}

// checkStatus separates command outcomes and requires passing coverage for
// every required path. A failure in a different check cannot change this one.
func (r *VerificationReport) checkStatus(kind string) VerificationState {
	if r == nil {
		return VerificationUnavailable
	}
	for _, item := range r.Failing {
		if item.checkKind == kind {
			return VerificationFail
		}
	}
	covered := make(map[string]bool)
	for _, item := range r.Covered {
		for _, path := range item.Paths {
			covered[path] = covered[path] || item.checkKind == kind
		}
	}
	for _, item := range r.Failing {
		for _, path := range item.Paths {
			if _, exists := covered[path]; !exists {
				covered[path] = false
			}
		}
	}
	for _, path := range r.Unverified {
		covered[path] = false
	}
	if len(covered) == 0 {
		return VerificationUnavailable
	}
	for _, pass := range covered {
		if !pass {
			return VerificationUnavailable
		}
	}
	return VerificationPass
}
