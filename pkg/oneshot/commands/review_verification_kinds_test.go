package commands

import (
	"fmt"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/verifyreceipt"
)

func TestSuppliedEvidence_CheckKindsAndPathCoverage(t *testing.T) {
	type recordedCheck struct {
		argv   []string
		status verifyreceipt.Status
		paths  []string
	}
	buildPass := recordedCheck{[]string{"go", "build", "./..."}, verifyreceipt.StatusPass, []string{"**"}}
	testPass := recordedCheck{[]string{"go", "test", "./..."}, verifyreceipt.StatusPass, []string{"**"}}
	testFail := recordedCheck{[]string{"go", "test", "-count=1", "./..."}, verifyreceipt.StatusFail, []string{"**"}}
	buildFail := recordedCheck{[]string{"go", "build", "./..."}, verifyreceipt.StatusFail, []string{"**"}}
	for _, tc := range []struct {
		name         string
		checks       []recordedCheck
		build, tests VerificationState
		wantErr      bool
	}{
		{"test failure cannot prove build failure", []recordedCheck{testFail}, VerificationFail, VerificationFail, true},
		{"test failure alone", []recordedCheck{testFail}, VerificationUnavailable, VerificationFail, false},
		{"passing test cannot prove build check", []recordedCheck{testPass}, VerificationPass, VerificationPass, true},
		{"passing test alone", []recordedCheck{testPass}, VerificationUnavailable, VerificationPass, false},
		{"passing build cannot prove tests", []recordedCheck{buildPass}, VerificationPass, VerificationPass, true},
		{"passing build alone", []recordedCheck{buildPass}, VerificationPass, VerificationUnavailable, false},
		{"mixed actual results", []recordedCheck{buildPass, testFail}, VerificationPass, VerificationFail, false},
		{"mixed results cannot swap phases", []recordedCheck{buildPass, testFail}, VerificationFail, VerificationPass, true},
		{"build failure alone", []recordedCheck{buildFail}, VerificationFail, VerificationUnavailable, false},
		{"build failure cannot prove test failure", []recordedCheck{buildFail}, VerificationFail, VerificationFail, true},
		{"both passing", []recordedCheck{buildPass, testPass}, VerificationPass, VerificationPass, false},
		{"opaque passing command", []recordedCheck{{[]string{"./scripts/build-and-test.sh"}, verifyreceipt.StatusPass, []string{"**"}}}, VerificationUnavailable, VerificationUnavailable, false},
		{"opaque cannot prove phases", []recordedCheck{{[]string{"./scripts/build-and-test.sh"}, verifyreceipt.StatusPass, []string{"**"}}}, VerificationPass, VerificationPass, true},
		{"opaque failure cannot prove phases", []recordedCheck{{[]string{"./scripts/build-and-test.sh"}, verifyreceipt.StatusFail, []string{"**"}}}, VerificationFail, VerificationFail, true},
		{"disjoint passing phases", []recordedCheck{{buildPass.argv, buildPass.status, []string{"a.go"}}, {testPass.argv, testPass.status, []string{"b.go"}}}, VerificationPass, VerificationPass, true},
		{"passing phase union", []recordedCheck{{buildPass.argv, buildPass.status, []string{"a.go"}}, {[]string{"go", "build", "./pkg/..."}, buildPass.status, []string{"b.go"}}}, VerificationPass, VerificationUnavailable, false},
		{"partially covered phase", []recordedCheck{{buildPass.argv, buildPass.status, []string{"a.go"}}, {[]string{"go", "vet", "./..."}, verifyreceipt.StatusPass, []string{"b.go"}}}, VerificationPass, VerificationUnavailable, true},
		{"compile only test command", []recordedCheck{{[]string{"go", "test", "-run", "^$", "./..."}, verifyreceipt.StatusPass, []string{"**"}}}, VerificationPass, VerificationUnavailable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := VerificationInput{Mode: VerifyNone, Files: []string{"a.go", "b.go"}}
			for i, check := range tc.checks {
				r := receipt(fmt.Sprintf("ev_case_%d", i), check.status, 0, check.paths...)
				r.Argv = check.argv
				if check.status == verifyreceipt.StatusFail {
					r.ExitCode = 1
				}
				input.Receipts = append(input.Receipts, r)
			}
			report := EvaluateVerification(input)
			parsed := baseParsedReview(GradeC, false)
			parsed.BuildVerification, parsed.TestVerification = tc.build, tc.tests
			for _, def := range []interface {
				ValidateAgentExecution(any, *oneshot.AgentResult) error
			}{
				ReviewBranchDef{SuppliedEvidenceOnly: true, Verification: &report},
				ReviewPRDef{SuppliedEvidenceOnly: true, Verification: &report},
			} {
				err := def.ValidateAgentExecution(&ReviewAgentResult{Parsed: parsed}, nil)
				if (err != nil) != tc.wantErr {
					t.Errorf("%T claims build=%s tests=%s: %v, want error=%v", def, tc.build, tc.tests, err, tc.wantErr)
				}
			}
		})
	}
}

func TestSuppliedEvidence_CapturedLunaTestFailureCannotBecomeBuildFailure(t *testing.T) {
	r := receipt("ev_captured", verifyreceipt.StatusFail, 1, "sample.go")
	r.Argv = []string{"go", "test", "-count=1", "./..."}
	report := EvaluateVerification(VerificationInput{Mode: VerifyNone, Files: []string{"sample.go"}, Receipts: []verifyreceipt.Receipt{r}})
	const response = "## Grade: D\n\n## Build & Test Status\n- Build: FAIL — receipt reports go test failed, without build-specific output.\n- Tests: FAIL — receipt reports go test failed.\n"
	result, err := (ReviewBranchDef{}).ParseResult(response)
	if err != nil {
		t.Fatal(err)
	}
	parsed := result.(*ReviewAgentResult).Parsed
	if parsed == nil || parsed.BuildVerification != VerificationFail || parsed.TestVerification != VerificationFail {
		t.Fatal("captured failure claims were not parsed")
	}
	if err := (ReviewBranchDef{SuppliedEvidenceOnly: true, Verification: &report}).ValidateAgentExecution(result, nil); err == nil {
		t.Fatal("captured unsupported Build FAIL claim accepted")
	}
}

func TestSuppliedEvidence_OpaqueCIChecksDoNotProvePhases(t *testing.T) {
	for _, state := range []CICheckState{CICheckPass, CICheckFail} {
		t.Run(string(state), func(t *testing.T) {
			report := EvaluateVerification(VerificationInput{Mode: VerifyReceiptOrCI, Files: []string{"a.go"}, ChecksOnHead: true,
				Checks:   []CICheck{{Name: "Build and Tests", State: state}},
				Declared: []DeclaredVerification{{Name: "all", Paths: []string{"**"}, CIChecks: []string{"Build and Tests"}}}})
			parsed := baseParsedReview(GradeC, false)
			parsed.BuildVerification, parsed.TestVerification = VerificationPass, VerificationPass
			if state == CICheckFail {
				parsed.BuildVerification, parsed.TestVerification = VerificationFail, VerificationFail
			}
			if err := validateReviewSuppliedEvidence(&ReviewAgentResult{Parsed: parsed}, &report); err == nil {
				t.Fatal("CI label alone proved both phases")
			}
			parsed.BuildVerification, parsed.TestVerification = VerificationUnavailable, VerificationUnavailable
			if err := validateReviewSuppliedEvidence(&ReviewAgentResult{Parsed: parsed}, &report); err != nil {
				t.Fatalf("aggregate CI evidence lost: %v", err)
			}
		})
	}
}
