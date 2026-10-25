package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/verifyreceipt"
)

func TestRecordCommands_PreservesShellArguments(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("POSIX shell unavailable")
	}
	marker := filepath.Join(t.TempDir(), "expanded")
	tests := []struct {
		name  string
		paths []string
		argv  []string
	}{
		{"simple", []string{"client/**"}, []string{"make", "test-js"}},
		{"test filter", []string{"pkg/**"}, []string{"go", "test", "-run", "TestA|TestB", "./..."}},
		{"shell script", []string{"src with spaces/**"}, []string{"sh", "-c", `printf '%s' "$1"; exit 0`, "test", "a b"}},
		{"literal values", []string{"it's here/**", "$(printf injected > '" + marker + "')/**"}, []string{"check", "", "*", "$HOME", "`id`", "a\\b", "-x;y", "line one\n```\nline two"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report := VerificationReport{Unverified: []string{"file.go"}}
			// Use matching globs so the report selects the declared command.
			report.Unverified = []string{strings.TrimSuffix(tt.paths[0], "**") + "file.go"}
			report.Declared = []DeclaredVerification{{Paths: tt.paths, Command: tt.argv}}
			commands := report.RecordCommands()
			if len(commands) != 1 {
				t.Fatalf("commands = %v", commands)
			}
			// Intercept the suggested Buckley invocation without running its check.
			out, err := exec.Command("sh", "-c", "buckley() { printf '%s\\000' \"$@\"; }\n"+commands[0]).CombinedOutput()
			if err != nil {
				t.Fatalf("suggested command: %v\n%s", err, out)
			}
			want := []string{"verify", "record"}
			for _, path := range tt.paths {
				want = append(want, "--paths", path)
			}
			want = append(append(want, "--"), tt.argv...)
			got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("shell argv = %#v, want %#v\ncommand: %s", got, want, commands[0])
			}
		})
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell expanded a suggested argument: %v", err)
	}
}

func TestVerificationReport_ActionableBoundedGuidance(t *testing.T) {
	report := VerificationReport{Mode: VerifyReceipt, Rejected: []string{"the reviewed tree has uncommitted changes"}}
	for i := range 7 {
		path := fmt.Sprintf("pkg%d/**", i)
		report.Unverified = append(report.Unverified, fmt.Sprintf("pkg%d/file.go", i))
		report.Declared = append(report.Declared, DeclaredVerification{Paths: []string{path}, Command: []string{"go", "test", fmt.Sprintf("./pkg%d/...", i)}})
	}
	code, gate := report.Gate()
	if code != VerificationGateMissing || !strings.Contains(gate, report.Rejected[0]) {
		t.Fatalf("missing evidence gate lost the rejection reason: %d %s", code, gate)
	}
	markdown := report.Markdown()
	for _, want := range []string{"### Record verification", "clean tree", "rerun Buckbot", "    buckley verify record --paths 'pkg0/**' -- go test ./pkg0/...", "2 more verification commands omitted"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("guidance missing %q:\n%s", want, markdown)
		}
	}
	if strings.Count(markdown, "    buckley verify record") != 5 {
		t.Fatalf("next-step commands were not bounded:\n%s", markdown)
	}
	report.Unverified = nil
	if strings.Contains(report.Markdown(), "### Record verification") {
		t.Fatal("verified report suggests unnecessary verification")
	}
}

func TestVerificationReport_UnknownCommandTemplate(t *testing.T) {
	report := VerificationReport{Unverified: []string{"custom/file.ext"}}
	if got := report.RecordCommands(); len(got) != 1 || got[0] != "buckley verify record --paths 'custom/**' -- '<your test command>'" {
		t.Fatalf("template = %v", got)
	}
}

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

func TestEvaluateVerificationNewestReceiptForACommandWins(t *testing.T) {
	older := receipt("ev_OLDFAIL", verifyreceipt.StatusFail, 2, "client/**")
	newer := receipt("ev_NEWPASS", verifyreceipt.StatusPass, 0, "client/**")
	newer.StartedAt = older.StartedAt.Add(time.Hour)
	for _, order := range [][]verifyreceipt.Receipt{{older, newer}, {newer, older}} {
		rep := EvaluateVerification(VerificationInput{Mode: VerifyReceipt, Files: []string{"client/a.js"}, Receipts: order})
		if len(rep.Failing) != 0 || len(rep.Covered) != 1 || !strings.Contains(rep.Covered[0].Label, "NEWPASS") {
			t.Fatalf("report = %+v", rep)
		}
	}
	// A different command is not superseded.
	other := receipt("ev_OTHER", verifyreceipt.StatusFail, 1, "client/**")
	other.Argv = []string{"npm", "test"}
	rep := EvaluateVerification(VerificationInput{Mode: VerifyReceipt, Files: []string{"client/a.js"}, Receipts: []verifyreceipt.Receipt{newer, other}})
	if len(rep.Failing) != 1 || len(rep.Covered) != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

// With no ci_checks mapping declared, CI on the reviewed head counts for every
// changed path once every reported check has completed and none failed.
func TestEvaluateVerificationUnmappedCI(t *testing.T) {
	files := []string{"client/js/a.js", "scene/x.go"}
	base := VerificationInput{Mode: VerifyReceiptOrCI, Files: files, ChecksOnHead: true,
		Declared: []DeclaredVerification{{Name: "js", Paths: []string{"client/**"}, Command: []string{"make", "test-js"}}}}

	t.Run("all green is verified and names its basis", func(t *testing.T) {
		in := base
		in.Checks = []CICheck{{"build", CICheckPass}, {"test", CICheckPass}, {"docs", CICheckSkipped}}
		rep := EvaluateVerification(in)
		if len(rep.Unverified) != 0 || len(rep.Failing) != 0 || len(rep.Covered) != 1 || len(rep.Rejected) != 0 {
			t.Fatalf("report = %+v", rep)
		}
		if code, msg := rep.Gate(); code != VerificationGateOK {
			t.Fatalf("gate = %d %q", code, msg)
		}
		md := rep.Markdown()
		for _, want := range []string{"CI: all 2 checks passed on the reviewed head", "no path mapping declared; counts for all changed paths", "1 skipped"} {
			if !strings.Contains(md, want) {
				t.Errorf("markdown lacks %q:\n%s", want, md)
			}
		}
	})

	t.Run("one failing blocks and names the check", func(t *testing.T) {
		in := base
		in.Checks = []CICheck{{"build", CICheckPass}, {"browser-tests", CICheckFail}}
		rep := EvaluateVerification(in)
		if len(rep.Failing) != 1 || !strings.Contains(rep.Failing[0].Label, "browser-tests") || len(rep.Covered) != 0 {
			t.Fatalf("report = %+v", rep)
		}
		code, msg := rep.Gate()
		if code != VerificationGateFailing || !strings.Contains(msg, "browser-tests") {
			t.Fatalf("gate = %d %q", code, msg)
		}
	})

	t.Run("pending or queued is not verified and says so", func(t *testing.T) {
		in := base
		in.Checks = []CICheck{{"build", CICheckPass}, {"test", CICheckPending}}
		rep := EvaluateVerification(in)
		if len(rep.Covered) != 0 || len(rep.Unverified) != 2 {
			t.Fatalf("report = %+v", rep)
		}
		code, msg := rep.Gate()
		if code != VerificationGateMissing || !strings.Contains(msg, "not finished") || !strings.Contains(msg, "test") {
			t.Fatalf("gate = %d %q", code, msg)
		}
	})

	t.Run("only skipped checks prove nothing", func(t *testing.T) {
		in := base
		in.Checks = []CICheck{{"docs", CICheckSkipped}}
		if rep := EvaluateVerification(in); len(rep.Covered) != 0 || len(rep.Unverified) != 2 {
			t.Fatalf("report = %+v", rep)
		}
	})

	t.Run("duplicate check names keep the worst state", func(t *testing.T) {
		in := base
		in.Checks = []CICheck{{"test", CICheckFail}, {"test", CICheckPass}}
		if rep := EvaluateVerification(in); len(rep.Failing) != 1 {
			t.Fatalf("report = %+v", rep)
		}
	})

	t.Run("another commit's checks never count", func(t *testing.T) {
		in := base
		in.ChecksOnHead = false
		in.Checks = []CICheck{{"build", CICheckPass}}
		if rep := EvaluateVerification(in); len(rep.Covered) != 0 {
			t.Fatalf("report = %+v", rep)
		}
	})

	t.Run("receipt mode ignores CI", func(t *testing.T) {
		in := base
		in.Mode = VerifyReceipt
		in.Checks = []CICheck{{"build", CICheckPass}}
		if rep := EvaluateVerification(in); len(rep.Covered) != 0 {
			t.Fatalf("report = %+v", rep)
		}
	})

	t.Run("receipts still cover their paths beside unmapped CI", func(t *testing.T) {
		in := base
		in.Mode = VerifyReceipt
		in.Receipts = []verifyreceipt.Receipt{receipt("ev_R", verifyreceipt.StatusPass, 0, "client/**")}
		rep := EvaluateVerification(in)
		if strings.Join(rep.Unverified, ",") != "scene/x.go" || len(rep.Covered) != 1 {
			t.Fatalf("report = %+v", rep)
		}
	})
}
