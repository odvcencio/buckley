package commands

import (
	"strings"
	"testing"
)

func TestSuppliedEvidenceApprovalRejectsProvisionalStates(t *testing.T) {
	coverage := "- **File**: `ratchet.go` — inspected the exact changed file.\n" +
		"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
		"- **Verification**: supplied evidence only."
	base := completeReviewWithCoverage(coverage)
	definitions := []struct {
		name string
		def  interface {
			ParseResult(string) (any, error)
			ValidateResult(any) error
		}
	}{
		{"branch", ReviewBranchDef{ChangedFiles: []string{"ratchet.go"}, SuppliedEvidenceOnly: true}},
		{"PR", ReviewPRDef{ChangedFiles: []string{"ratchet.go"}, SuppliedEvidenceOnly: true, CIStatus: "pending (1/2)"}},
	}
	for _, entry := range definitions {
		t.Run(entry.name, func(t *testing.T) {
			valid, err := entry.def.ParseResult(base)
			if err != nil {
				t.Fatal(err)
			}
			if err := entry.def.ValidateResult(valid); err != nil {
				t.Fatalf("passing supplied status was rejected: %v", err)
			}
			for _, state := range []string{"PENDING", "NOT_RUN", "UNKNOWN"} {
				for _, field := range []string{"Build", "Tests"} {
					t.Run(field+" "+state, func(t *testing.T) {
						review := strings.Replace(base, "- "+field+": PASS", "- "+field+": "+state, 1)
						result, err := entry.def.ParseResult(review)
						if err != nil {
							t.Fatal(err)
						}
						if err := entry.def.ValidateResult(result); err == nil || !strings.Contains(err.Error(), "an unverified approval requires "+field+" status") {
							t.Fatalf("%s approval with %s %s returned %v", entry.name, field, state, err)
						}
					})
				}
			}
			result, err := entry.def.ParseResult(strings.Replace(base, "- Build: PASS", "- Build: NOT_APPLICABLE", 1))
			if err != nil {
				t.Fatal(err)
			}
			if err := entry.def.ValidateResult(result); err == nil || !strings.Contains(err.Error(), "an unverified approval requires Build status") {
				t.Fatalf("approval with Build NOT_APPLICABLE returned %v", err)
			}
		})
	}
}

func TestSuppliedEvidenceApprovalKeepsUnavailableStatus(t *testing.T) {
	coverage := "- **File**: `ratchet.go` — inspected the exact changed file.\n" +
		"- **Feedback disposition**: `NONE_SUPPLIED` — no prior feedback was supplied.\n" +
		"- **Verification**: not independently run."
	review := completeReviewWithCoverage(coverage)
	review = strings.Replace(review, "- Build: PASS", "- Build: UNAVAILABLE", 1)
	review = strings.Replace(review, "- Tests: PASS", "- Tests: UNAVAILABLE", 1)
	def := ReviewBranchDef{ChangedFiles: []string{"ratchet.go"}, SuppliedEvidenceOnly: true}
	result, err := def.ParseResult(review)
	if err != nil {
		t.Fatal(err)
	}
	if err := def.ValidateResult(result); err != nil {
		t.Fatalf("unavailable supplied verification was rejected: %v", err)
	}
	if err := def.ValidateAgentExecution(result, nil); err != nil {
		t.Fatalf("unavailable supplied verification unexpectedly required an execution receipt: %v", err)
	}
}
