package reviewpolicy

import (
	"errors"
	"testing"
)

func TestNewCIAdmissionReceipt_DistinguishesNoRequiredContextsFromUnavailable(t *testing.T) {
	expectation := testCIAdmissionExpectation()

	noRequired, err := NewCIAdmissionReceipt(CIAdmissionInput{
		Expectation:               expectation,
		RequiredContextsAvailable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if noRequired.Decision != CIAdmissionDeny || noRequired.Reason != CIAdmissionReasonNoRequiredContexts {
		t.Fatalf("no-required decision = %s/%s", noRequired.Decision, noRequired.Reason)
	}
	if err := noRequired.ValidateFor(expectation); err != nil {
		t.Fatalf("stable no-required receipt validation = %v", err)
	}
	if err := noRequired.Authorize(expectation); !errors.Is(err, ErrCIAdmissionDenied) {
		t.Fatalf("no-required authorization error = %v, want denied", err)
	}

	unavailable, err := NewCIAdmissionReceipt(CIAdmissionInput{Expectation: expectation})
	if err != nil {
		t.Fatal(err)
	}
	if unavailable.Decision != CIAdmissionUnavailable || unavailable.Reason != CIAdmissionReasonRequiredContextsUnavailable {
		t.Fatalf("unavailable decision = %s/%s", unavailable.Decision, unavailable.Reason)
	}
	if err := unavailable.ValidateFor(expectation); err != nil {
		t.Fatalf("stable unavailable receipt validation = %v", err)
	}
	if err := unavailable.Authorize(expectation); !errors.Is(err, ErrCIAdmissionUnavailable) {
		t.Fatalf("unavailable authorization error = %v, want unavailable", err)
	}
	if noRequired.RequiredContextsFingerprint == unavailable.RequiredContextsFingerprint {
		t.Fatal("available empty and unavailable observations share a fingerprint")
	}
}

func TestNewCIAdmissionReceipt_CanonicalizesAndAuthorizesPassingRequiredContexts(t *testing.T) {
	expectation := testCIAdmissionExpectation()
	receipt, err := NewCIAdmissionReceipt(CIAdmissionInput{
		Expectation:               expectation,
		RequiredContextsAvailable: true,
		RequiredContexts: []CIRequiredContext{
			{Name: " unit ", State: "success"},
			{Name: "build", State: "PASS"},
			{Name: "build", State: "PASS"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Decision != CIAdmissionAllow || receipt.Reason != CIAdmissionReasonRequiredContextsPassed {
		t.Fatalf("decision = %s/%s", receipt.Decision, receipt.Reason)
	}
	if receipt.TestReachabilityStatus != CIReachabilityNotApplicable {
		t.Fatalf("reachability status = %s, want not applicable", receipt.TestReachabilityStatus)
	}
	if len(receipt.RequiredContexts) != 2 || receipt.RequiredContexts[0].Name != "build" || receipt.RequiredContexts[1].State != "SUCCESS" {
		t.Fatalf("canonical required contexts = %#v", receipt.RequiredContexts)
	}
	if receipt.Digest == "" || receipt.PolicySHA256 == "" || receipt.RequiredContextsFingerprint == "" {
		t.Fatalf("receipt lacks canonical digests: %#v", receipt)
	}
	if err := receipt.Authorize(expectation); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	reordered, err := NewCIAdmissionReceipt(CIAdmissionInput{
		Expectation:               expectation,
		RequiredContextsAvailable: true,
		RequiredContexts: []CIRequiredContext{
			{Name: "unit", State: "SUCCESS"},
			{Name: "build", State: "PASS"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reordered.Digest != receipt.Digest || reordered.RequiredContextsFingerprint != receipt.RequiredContextsFingerprint {
		t.Fatal("required-context input order changed canonical receipt identity")
	}
	renamed, err := NewCIAdmissionReceipt(CIAdmissionInput{
		Expectation:               expectation,
		RequiredContextsAvailable: true,
		RequiredContexts:          []CIRequiredContext{{Name: "different-required-context", State: "SUCCESS"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.RequiredContextsFingerprint == receipt.RequiredContextsFingerprint || renamed.Digest == receipt.Digest {
		t.Fatal("changed required-context identity did not invalidate fingerprints")
	}
}

func TestCIAdmissionReceipt_RejectsMissingStaleAndTamperedReceipts(t *testing.T) {
	expectation := testCIAdmissionExpectation()
	var missing CIAdmissionReceipt
	if err := missing.Authorize(expectation); !errors.Is(err, ErrCIAdmissionMissing) {
		t.Fatalf("missing receipt error = %v", err)
	}

	receipt := testPassingCIAdmissionReceipt(t, expectation)
	stale := expectation
	stale.Identity.HeadSHA = "different-head"
	if err := receipt.Authorize(stale); !errors.Is(err, ErrCIAdmissionStale) {
		t.Fatalf("stale receipt error = %v", err)
	}

	tampered := receipt
	tampered.Decision = CIAdmissionDeny
	if err := tampered.Authorize(expectation); !errors.Is(err, ErrCIAdmissionInvalid) {
		t.Fatalf("tampered decision error = %v", err)
	}
	tampered = receipt
	tampered.RequiredContexts = append([]CIRequiredContext(nil), receipt.RequiredContexts...)
	tampered.RequiredContexts[0].State = "FAILURE"
	if err := tampered.Authorize(expectation); !errors.Is(err, ErrCIAdmissionInvalid) {
		t.Fatalf("tampered context error = %v", err)
	}
	tampered = receipt
	tampered.RequiredContexts = nil
	if err := tampered.Authorize(expectation); !errors.Is(err, ErrCIAdmissionInvalid) {
		t.Fatalf("non-canonical nil contexts error = %v", err)
	}
	tampered = receipt
	tampered.Identity.HeadSHA = "different-head"
	if err := tampered.Authorize(expectation); !errors.Is(err, ErrCIAdmissionInvalid) {
		t.Fatalf("tampered identity error = %v", err)
	}
}

func TestNewCIAdmissionReceipt_RequiresExactSnapshotIdentity(t *testing.T) {
	expectation := testCIAdmissionExpectation()
	expectation.Identity.Host = ""
	if _, err := NewCIAdmissionReceipt(CIAdmissionInput{Expectation: expectation}); !errors.Is(err, ErrCIAdmissionInvalid) {
		t.Fatalf("incomplete identity error = %v", err)
	}
}

func TestNewCIAdmissionReceipt_RecognizedTestFileRequiresUnavailableAdapterFinding(t *testing.T) {
	expectation := testCIAdmissionExpectation()
	expectation.TestReachability = CIReachabilityRequest{
		RecognizedChangedTestFiles: []string{"pkg/z_test.go", "./pkg/z_test.go"},
	}
	receipt := testPassingCIAdmissionReceipt(t, expectation)
	if receipt.TestReachabilityStatus != CIReachabilityUnavailable ||
		receipt.Decision != CIAdmissionUnavailable ||
		receipt.Reason != CIAdmissionReasonTestReachabilityUnavailable {
		t.Fatalf("test-file decision = %s/%s reachability=%s", receipt.Decision, receipt.Reason, receipt.TestReachabilityStatus)
	}
	if len(receipt.TestReachability.RecognizedChangedTestFiles) != 1 || !receipt.TestReachability.Requested {
		t.Fatalf("canonical reachability request = %#v", receipt.TestReachability)
	}
	if err := receipt.Authorize(expectation); !errors.Is(err, ErrCIAdmissionUnavailable) {
		t.Fatalf("recognized test file authorization error = %v", err)
	}

	forgedExpectation := testCIAdmissionExpectation()
	forged := testPassingCIAdmissionReceipt(t, forgedExpectation)
	if err := forged.Authorize(expectation); !errors.Is(err, ErrCIAdmissionStale) {
		t.Fatalf("receipt that omitted recognized test files error = %v, want stale", err)
	}
}

func TestNewCIAdmissionReceipt_GoTestPackageEvidenceAuthorizesExactHead(t *testing.T) {
	expectation := testCIAdmissionExpectation()
	expectation.TestReachability.RecognizedChangedTestFiles = []string{"pkg/tool/builtin/git_test.go", "pkg/reviewpolicy/ci_admission_test.go"}
	evidence := &CIReachabilityEvidence{
		Source: "buckley_ci_go_test_v1", HeadSHA: expectation.Identity.HeadSHA,
		RunID: 123, JobID: 456, Check: "Test", Module: "m31labs.dev/buckley",
		Packages: []string{"m31labs.dev/buckley/pkg/tool/builtin", "m31labs.dev/buckley/pkg/reviewpolicy"},
	}
	input := CIAdmissionInput{
		Expectation: expectation, RequiredContextsAvailable: true,
		RequiredContexts:         []CIRequiredContext{{Name: "Test", State: "SUCCESS"}},
		TestReachabilityEvidence: evidence,
	}
	receipt, err := NewCIAdmissionReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Decision != CIAdmissionAllow || receipt.TestReachabilityStatus != CIReachabilityCovered {
		t.Fatalf("admission = %s/%s reachability=%s", receipt.Decision, receipt.Reason, receipt.TestReachabilityStatus)
	}
	if err := receipt.Authorize(expectation); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	changed := expectation
	changed.Identity.HeadSHA = "new-head"
	if err := receipt.Authorize(changed); !errors.Is(err, ErrCIAdmissionStale) {
		t.Fatalf("changed head authorization = %v", err)
	}
	tampered := receipt
	copy := *receipt.TestReachabilityEvidence
	copy.Packages = []string{"m31labs.dev/buckley/pkg/tool/builtin"}
	tampered.TestReachabilityEvidence = &copy
	if err := tampered.Authorize(expectation); !errors.Is(err, ErrCIAdmissionInvalid) {
		t.Fatalf("tampered packages authorization = %v", err)
	}

	input.TestReachabilityEvidence = &copy
	missing, err := NewCIAdmissionReceipt(input)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Decision != CIAdmissionDeny || missing.Reason != CIAdmissionReasonTestNotCovered ||
		missing.TestReachabilityStatus != CIReachabilityNotCovered {
		t.Fatalf("uncovered test admission = %s/%s reachability=%s", missing.Decision, missing.Reason, missing.TestReachabilityStatus)
	}
	if err := missing.Authorize(expectation); !errors.Is(err, ErrCIAdmissionDenied) {
		t.Fatalf("uncovered test authorization = %v", err)
	}
}

func TestNewCIAdmissionReceipt_RejectsUnboundGoTestEvidence(t *testing.T) {
	expectation := testCIAdmissionExpectation()
	expectation.TestReachability.RecognizedChangedTestFiles = []string{"pkg/git_test.go"}
	evidence := &CIReachabilityEvidence{
		Source: "buckley_ci_go_test_v1", HeadSHA: expectation.Identity.HeadSHA,
		RunID: 123, JobID: 456, Check: "Test", Module: "m31labs.dev/buckley",
		Packages: []string{"m31labs.dev/buckley/pkg"},
	}
	for _, test := range []struct {
		name string
		edit func(*CIReachabilityEvidence)
	}{
		{"wrong head", func(e *CIReachabilityEvidence) { e.HeadSHA = "other-head" }},
		{"unrequired check", func(e *CIReachabilityEvidence) { e.Check = "Other" }},
		{"missing run", func(e *CIReachabilityEvidence) { e.RunID = 0 }},
		{"outside module", func(e *CIReachabilityEvidence) { e.Packages = []string{"other/pkg"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := *evidence
			test.edit(&copy)
			_, err := NewCIAdmissionReceipt(CIAdmissionInput{
				Expectation: expectation, RequiredContextsAvailable: true,
				RequiredContexts:         []CIRequiredContext{{Name: "Test", State: "SUCCESS"}},
				TestReachabilityEvidence: &copy,
			})
			if !errors.Is(err, ErrCIAdmissionInvalid) {
				t.Fatalf("NewCIAdmissionReceipt error = %v", err)
			}
		})
	}
}

func testCIAdmissionExpectation() CIAdmissionExpectation {
	return CIAdmissionExpectation{Identity: CIAdmissionIdentity{
		Host:       "github.com",
		Repository: "m31labs/buckley",
		PRNumber:   208,
		BaseBranch: "main",
		BaseSHA:    "base-sha",
		HeadBranch: "topic",
		HeadSHA:    "head-sha",
	}}
}

func testPassingCIAdmissionReceipt(t *testing.T, expectation CIAdmissionExpectation) CIAdmissionReceipt {
	t.Helper()
	receipt, err := NewCIAdmissionReceipt(CIAdmissionInput{
		Expectation:               expectation,
		RequiredContextsAvailable: true,
		RequiredContexts:          []CIRequiredContext{{Name: "unit", State: "SUCCESS"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
