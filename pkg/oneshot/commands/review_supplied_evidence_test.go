package commands

import (
	"slices"
	"strings"
	"testing"
)

func TestSuppliedEvidenceDefsNeverRunVerification(t *testing.T) {
	branch := ReviewBranchDef{ChangedFiles: []string{"a.go"}, Depth: "balanced", SuppliedEvidenceOnly: true}
	pr := ReviewPRDef{ChangedFiles: []string{"a.go"}, Depth: "balanced", SuppliedEvidenceOnly: true}
	project := ReviewProjectDef{Depth: "balanced", SuppliedEvidenceOnly: true}
	for name, tools := range map[string][]string{"branch": branch.AllowedTools(), "pr": pr.AllowedTools(), "project": project.AllowedTools()} {
		if slices.Contains(tools, "run_verification") {
			t.Errorf("%s review exposes run_verification: %v", name, tools)
		}
	}
	if len(branch.AgentEvidenceRequests()) != 0 || len(pr.AgentEvidenceRequests()) != 0 {
		t.Error("supplied-evidence reviews planned host verification")
	}
	for name, prompt := range map[string]string{"branch": branch.SystemPrompt(), "pr": pr.SystemPrompt()} {
		if !strings.Contains(prompt, "You do not run build or test commands") {
			t.Errorf("%s prompt lacks the verification policy", name)
		}
	}
	// The zero value keeps the opt-in execution path.
	if !slices.Contains((ReviewBranchDef{}).AllowedTools(), "run_verification") || len((ReviewBranchDef{ChangedFiles: []string{"a.go"}}).AgentEvidenceRequests()) == 0 {
		t.Error("execution path no longer available when opted in")
	}
}

func TestSuppliedEvidenceApprovalDoesNotNeedPassingTests(t *testing.T) {
	parsed := baseParsedReview(GradeA, true)
	parsed.BuildVerification, parsed.TestVerification = VerificationUnavailable, VerificationUnavailable
	opts := ReviewValidationOptions{AllowUnverifiedApproval: true}
	if err := validateApprovalEvidence(parsed, opts); err != nil {
		t.Fatalf("unverified approval rejected: %v", err)
	}
	if err := validateApprovalEvidence(parsed, ReviewValidationOptions{}); err == nil {
		t.Fatal("without the option, an approval still needs Build PASS")
	}
	parsed.TestVerification = VerificationFail
	if err := validateApprovalEvidence(parsed, opts); err == nil {
		t.Fatal("approval over failing tests accepted")
	}
	if err := validateApprovalRemoteCI(ReviewValidationOptions{AllowUnverifiedApproval: true, RequirePassingRemoteCI: true, CIStatus: "pending (1/2)"}); err != nil {
		t.Fatalf("pending CI blocked an unverified approval: %v", err)
	}
}

func TestFailingEvidenceBlocksApprovalOnly(t *testing.T) {
	approved := &ReviewAgentResult{Parsed: baseParsedReview(GradeA, true)}
	declined := &ReviewAgentResult{Parsed: baseParsedReview(GradeC, false)}
	failing := []string{"receipt ev_X: `make test-js` (FAIL)"}
	if err := (ReviewBranchDef{SuppliedEvidenceOnly: true, FailingEvidence: failing}).ValidateAgentExecution(approved, nil); err == nil || !strings.Contains(err.Error(), "make test-js") {
		t.Fatalf("approval over failing evidence: %v", err)
	}
	if err := (ReviewBranchDef{SuppliedEvidenceOnly: true, FailingEvidence: failing}).ValidateAgentExecution(declined, nil); err != nil {
		t.Fatalf("a non-approval must complete: %v", err)
	}
	if err := (ReviewPRDef{SuppliedEvidenceOnly: true}).ValidateAgentExecution(approved, nil); err != nil {
		t.Fatalf("missing evidence must not block: %v", err)
	}
}
