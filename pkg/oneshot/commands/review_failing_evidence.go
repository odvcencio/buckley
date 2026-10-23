package commands

import (
	"fmt"
	"strings"
)

// validateNoApprovalOverFailingEvidence rejects an approval while a receipt or
// CI check covering the changed paths is failing. Failing evidence is a
// finding; missing evidence never blocks a review.
func validateNoApprovalOverFailingEvidence(result any, failing []string) error {
	if len(failing) == 0 {
		return nil
	}
	review, ok := result.(*ReviewAgentResult)
	if !ok || review.Parsed == nil || !review.Parsed.Approved {
		return nil
	}
	return fmt.Errorf("an approval is inconsistent with failing verification evidence: %s; report it as a finding and do not approve", strings.Join(failing, "; "))
}
