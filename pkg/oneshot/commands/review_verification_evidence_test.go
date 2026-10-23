package commands

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/verifyreceipt"
)

func receipt(id string, status verifyreceipt.Status, exit int, paths ...string) verifyreceipt.Receipt {
	return verifyreceipt.Receipt{
		ID: id, Argv: []string{"make", "test-js"}, Paths: paths, Status: status, ExitCode: exit,
		StartedAt: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC), DurationMS: 5000,
		Env: verifyreceipt.Environment{Hostname: "box", OS: "linux", Arch: "amd64"},
	}
}

func TestParseVerificationMode(t *testing.T) {
	for in, want := range map[string]VerificationMode{"": VerifyNone, "none": VerifyNone, "Receipt": VerifyReceipt, "receipt-or-ci": VerifyReceiptOrCI} {
		if got, err := ParseVerificationMode(in); err != nil || got != want {
			t.Errorf("ParseVerificationMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseVerificationMode("always"); err == nil {
		t.Error("unknown mode accepted")
	}
}

func TestEvaluateVerificationMapsReceiptsToPaths(t *testing.T) {
	rep := EvaluateVerification(VerificationInput{
		Mode:  VerifyReceipt,
		Files: []string{"client/js/a.js", "scene/x.go", "docs/readme.md", "README.md"},
		Receipts: []verifyreceipt.Receipt{
			receipt("ev_AAAAAAAAAAAA", verifyreceipt.StatusPass, 0, "client/**"),
		},
	})
	if len(rep.Covered) != 1 || strings.Join(rep.Covered[0].Paths, ",") != "client/js/a.js" {
		t.Fatalf("covered = %+v", rep.Covered)
	}
	if strings.Join(rep.Unverified, ",") != "scene/x.go" {
		t.Fatalf("unverified = %v, want scene/x.go (docs need no evidence)", rep.Unverified)
	}
	if len(rep.NotRequired) != 2 {
		t.Fatalf("not required = %v", rep.NotRequired)
	}
	if rep.Qualifier() != "unverified" {
		t.Fatalf("qualifier = %s", rep.Qualifier())
	}
}

func TestEvaluateVerificationFailingReceiptIsFailingNotMissing(t *testing.T) {
	rep := EvaluateVerification(VerificationInput{
		Mode:     VerifyReceipt,
		Files:    []string{"client/js/a.js"},
		Receipts: []verifyreceipt.Receipt{receipt("ev_BBBBBBBBBBBB", verifyreceipt.StatusFail, 2, "client/**")},
	})
	if len(rep.Failing) != 1 || len(rep.Unverified) != 0 || len(rep.Covered) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	code, msg := rep.Gate()
	if code != VerificationGateFailing || !strings.Contains(msg, "failing") || !strings.Contains(msg, "client/js/a.js") {
		t.Fatalf("gate = %d %q", code, msg)
	}
}

func TestEvaluateVerificationCIChecks(t *testing.T) {
	declared := []DeclaredVerification{{Name: "js", Paths: []string{"client/**"}, CIChecks: []string{"js-tests", "js-tests-b"}}}
	base := VerificationInput{Mode: VerifyReceiptOrCI, Files: []string{"client/js/a.js", "scene/x.go"}, ChecksOnHead: true, Declared: declared}

	pass := base
	pass.Checks = []CICheck{{"js-tests", CICheckPass}, {"js-tests-b", CICheckPass}, {"lint", CICheckFail}}
	rep := EvaluateVerification(pass)
	if len(rep.Covered) != 1 || strings.Join(rep.Covered[0].Paths, ",") != "client/js/a.js" || len(rep.Failing) != 0 {
		t.Fatalf("declared CI: %+v", rep)
	}
	if strings.Join(rep.Unverified, ",") != "scene/x.go" {
		t.Fatalf("unverified = %v", rep.Unverified)
	}

	pending := base
	pending.Checks = []CICheck{{"js-tests", CICheckPass}, {"js-tests-b", CICheckPending}}
	if rep := EvaluateVerification(pending); len(rep.Covered) != 0 || len(rep.Unverified) != 2 {
		t.Fatalf("a pending check must not cover: %+v", rep)
	}

	failed := base
	failed.Checks = []CICheck{{"js-tests", CICheckFail}, {"js-tests-b", CICheckPass}}
	if rep := EvaluateVerification(failed); len(rep.Failing) != 1 || len(rep.Covered) != 0 {
		t.Fatalf("failed check: %+v", rep)
	}

	otherHead := pass
	otherHead.ChecksOnHead = false
	if rep := EvaluateVerification(otherHead); len(rep.Covered) != 0 {
		t.Fatalf("checks from another commit counted: %+v", rep)
	}

	receiptOnly := pass
	receiptOnly.Mode = VerifyReceipt
	if rep := EvaluateVerification(receiptOnly); len(rep.Covered) != 0 {
		t.Fatalf("receipt mode counted CI: %+v", rep)
	}
}

func TestEvaluateVerificationDefaultCICoverageSaysSo(t *testing.T) {
	in := VerificationInput{Mode: VerifyReceiptOrCI, Files: []string{"a.go", "b.go"}, ChecksOnHead: true,
		Checks: []CICheck{{"build", CICheckPass}, {"test", CICheckPass}, {"docs", CICheckSkipped}}}
	rep := EvaluateVerification(in)
	if len(rep.Covered) != 1 || len(rep.Covered[0].Paths) != 2 || !strings.Contains(rep.Covered[0].Detail, "default coverage") {
		t.Fatalf("default CI coverage = %+v", rep.Covered)
	}
	in.Checks = append(in.Checks, CICheck{"slow", CICheckPending})
	if rep := EvaluateVerification(in); len(rep.Covered) != 0 {
		t.Fatalf("a pending check must block default coverage: %+v", rep)
	}
}

func TestVerificationGate(t *testing.T) {
	declared := []DeclaredVerification{{Name: "js", Paths: []string{"client/**"}, Command: []string{"make", "test-js"}}}
	in := VerificationInput{Mode: VerifyReceipt, Files: []string{"client/js/a.js", "scene/x.go"}, Declared: declared}
	rep := EvaluateVerification(in)
	rep.PolicySource = "--require-verification"
	code, msg := rep.Gate()
	if code != VerificationGateMissing {
		t.Fatalf("code = %d, want 16", code)
	}
	for _, want := range []string{"client/js/a.js", "scene/x.go", "buckley verify record --paths 'client/**' -- make test-js", "--require-verification", "No model call"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	in.Mode = VerifyNone
	if code, _ := EvaluateVerification(in).Gate(); code != VerificationGateOK {
		t.Fatalf("mode none must not gate, got %d", code)
	}
	in.Mode = VerifyReceipt
	in.Receipts = []verifyreceipt.Receipt{receipt("ev_C", verifyreceipt.StatusPass, 0, "client/**", "scene/**")}
	if code, _ := EvaluateVerification(in).Gate(); code != VerificationGateOK {
		t.Fatalf("fully covered must pass, got %d", code)
	}
}

func TestVerificationMarkdownListsCoveredFailingAndUnverified(t *testing.T) {
	rep := EvaluateVerification(VerificationInput{
		Mode:  VerifyNone,
		Files: []string{"a.js", "b.go", "c.rs"},
		Receipts: []verifyreceipt.Receipt{
			receipt("ev_PASSPASSPASS", verifyreceipt.StatusPass, 0, "*.js"),
			receipt("ev_FAILFAILFAIL", verifyreceipt.StatusFail, 1, "*.go"),
		},
		Rejected: []string{"ev_X: log digest does not match the receipt"},
	})
	md := rep.Markdown()
	for _, want := range []string{"Verification: **failing**", "### Covered", "make test-js", "a.js", "### Failing evidence (a finding)", "b.go", "### Unverified paths", "c.rs", "### Rejected receipts"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}
