package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

func newVerifyRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	for _, f := range []string{"client/a.js", "scene/x.go"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	t.Chdir(dir)
	t.Setenv(envBuckleyDataDir, filepath.Join(t.TempDir(), "data"))
	return dir
}

func verifyTestOpts(mode string) automatedReviewOptions {
	cfg := config.DefaultConfig()
	cfg.Review.Verification.Declared = []config.DeclaredVerification{{Name: "js", Paths: []string{"client/**"}, Command: []string{"sh", "-c", "exit 0"}}}
	opts := defaultAutomatedReviewOptions(cfg)
	opts.requireVerification = mode
	return opts
}

var verifyFiles = []string{"client/a.js", "scene/x.go"}

func TestResolveVerificationReportFailsFastWithoutReceipts(t *testing.T) {
	newVerifyRepo(t)
	report, err := verifyTestOpts("receipt").resolveVerificationReport(context.Background(), verificationTarget{Files: verifyFiles, HeadRev: "HEAD"})
	if err == nil || exitCodeForError(err) != commands.VerificationGateMissing {
		t.Fatalf("err = %v (exit %d), want exit 16", err, exitCodeForError(err))
	}
	for _, want := range []string{"client/a.js", "scene/x.go", "buckley verify record", "--require-verification flag", "No model call"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message missing %q:\n%s", want, err)
		}
	}
	if report == nil || len(report.Unverified) != 2 {
		t.Fatalf("report = %+v", report)
	}
}

func TestResolveVerificationReportNoneCompletesAsUnverified(t *testing.T) {
	newVerifyRepo(t)
	report, err := verifyTestOpts("").resolveVerificationReport(context.Background(), verificationTarget{Files: verifyFiles, HeadRev: "HEAD"})
	if err != nil {
		t.Fatalf("policy none must not gate: %v", err)
	}
	if report.Qualifier() != "unverified" || !strings.Contains(report.PolicySource, "default") {
		t.Fatalf("report = %+v", report)
	}
}

func TestVerifyRecordThenReviewGateUsesReceipt(t *testing.T) {
	repo := newVerifyRepo(t)
	if err := recordAndPrint(repo, []string{"sh", "-c", "exit 0"}, []string{"client/**", "scene/**"}, "all", "record"); err != nil {
		t.Fatal(err)
	}
	report, err := verifyTestOpts("receipt").resolveVerificationReport(context.Background(), verificationTarget{Files: verifyFiles, HeadRev: "HEAD"})
	if err != nil {
		t.Fatalf("covered paths must pass: %v", err)
	}
	if len(report.Covered) != 1 || len(report.Covered[0].Paths) != 2 || report.Qualifier() != "verified" {
		t.Fatalf("report = %+v", report)
	}
	// A commit changes the tree: the old receipt no longer matches.
	if err := os.WriteFile(filepath.Join(repo, "scene/x.go"), []byte("y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@e", "commit", "-q", "-m", "change"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	_, err = verifyTestOpts("receipt").resolveVerificationReport(context.Background(), verificationTarget{Files: verifyFiles, HeadRev: "HEAD"})
	if exitCodeForError(err) != commands.VerificationGateMissing {
		t.Fatalf("stale receipt accepted: %v", err)
	}
}

func TestResolveVerificationReportFailingReceiptExits17(t *testing.T) {
	repo := newVerifyRepo(t)
	_ = recordAndPrint(repo, []string{"sh", "-c", "exit 4"}, []string{"client/**", "scene/**"}, "", "record")
	_, err := verifyTestOpts("receipt").resolveVerificationReport(context.Background(), verificationTarget{Files: verifyFiles, HeadRev: "HEAD"})
	if exitCodeForError(err) != commands.VerificationGateFailing {
		t.Fatalf("err = %v (exit %d), want 17", err, exitCodeForError(err))
	}
	report, err := verifyTestOpts("none").resolveVerificationReport(context.Background(), verificationTarget{Files: verifyFiles, HeadRev: "HEAD"})
	if err != nil || len(report.Failing) != 1 || report.Qualifier() != "failing" {
		t.Fatalf("policy none must report failing evidence without gating: %v %+v", err, report)
	}
}

func TestResolveVerificationReportPolicyPrecedenceAndLabels(t *testing.T) {
	newVerifyRepo(t)
	opts := verifyTestOpts("")
	target := verificationTarget{Files: verifyFiles, HeadRev: "HEAD", Labels: []string{commands.LabelVerifyRequired}}
	if _, err := opts.resolveVerificationReport(context.Background(), target); exitCodeForError(err) != 16 || !strings.Contains(err.Error(), commands.LabelVerifyRequired) {
		t.Fatalf("label policy: %v", err)
	}
	opts.requireVerification = "none"
	if _, err := opts.resolveVerificationReport(context.Background(), target); err != nil {
		t.Fatalf("flag none must beat the label: %v", err)
	}
	opts.requireVerification = ""
	if err := os.MkdirAll(".buckley", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".buckley/config.yaml", []byte("buckbot:\n  verification:\n    require: receipt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target.Labels = nil
	if _, err := opts.resolveVerificationReport(context.Background(), target); exitCodeForError(err) != 16 || !strings.Contains(err.Error(), "repository config") {
		t.Fatalf("repo config policy: %v", err)
	}
}

func TestVerifyRecordRequiresPathsUnlessDeclared(t *testing.T) {
	newVerifyRepo(t)
	t.Setenv("HOME", t.TempDir())
	if err := runVerifyRecord([]string{"--", "true"}); err == nil || !strings.Contains(err.Error(), "--paths") {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyRecordRefusesDirtyTree(t *testing.T) {
	repo := newVerifyRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := recordAndPrint(repo, []string{"true"}, []string{"**"}, "", "record"); err == nil || !strings.Contains(err.Error(), "not clean") {
		t.Fatalf("err = %v", err)
	}
}
