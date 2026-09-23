package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/reviewpolicy"
)

func TestCapturePRCIAdmission_GoTestEvidenceCoversChangedTest(t *testing.T) {
	pr := reachabilityTestPR()
	files := []string{"pkg/tool/builtin/git_test.go"}
	base := reachabilityTestRunner(pr, "")
	logCalls := 0
	run := func(name string, args ...string) ([]byte, error) {
		if name == "gh" && hasPRArgPrefix(args, "run", "view", "--job") && hasPRArg(args, "--log") {
			logCalls++
			return []byte("Test\tUNKNOWN STEP\t2026-09-22T22:50:00Z ok  m31labs.dev/buckley/pkg/tool/builtin 0.01s\n"), nil
		}
		return base(name, args...)
	}
	capture, err := capturePRCIAdmission(run,
		prReference{Number: pr.Number, Host: pr.Host, Repository: pr.Repository}, pr, files)
	if err != nil {
		t.Fatal(err)
	}
	if capture.ReachabilityErr != nil || capture.Receipt.Decision != reviewpolicy.CIAdmissionAllow ||
		capture.Receipt.TestReachabilityStatus != reviewpolicy.CIReachabilityCovered {
		t.Fatalf("capture = %#v", capture)
	}
	if err := capture.Receipt.Authorize(prCIAdmissionExpectation(pr, files)); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if capture.Receipt.TestReachabilityEvidence.RunID != 123 || capture.Receipt.TestReachabilityEvidence.JobID != 456 ||
		capture.Receipt.TestReachabilityEvidence.MergeSHA != "merge-sha" {
		t.Fatalf("reachability provenance = %#v", capture.Receipt.TestReachabilityEvidence)
	}
	if logCalls != 0 {
		t.Fatalf("CI log lines influenced structural reachability: %d calls", logCalls)
	}
}

func TestCapturePRCIAdmission_GoTestEvidenceFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name         string
		mode         string
		wantDecision reviewpolicy.CIAdmissionDecision
		wantReason   reviewpolicy.CIAdmissionReason
	}{
		{"package has no buildable source", "no-source", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"package source ignored by Go", "ignored-source", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"package source is build constrained", "source-build-tag", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"CI contract drift", "contract-drift", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"merge commit parent drift", "merge-parent-drift", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"required run base drift", "run-base-drift", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"stale run head", "stale-head", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"unrelated required job link", "wrong-link", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"build-constrained test", "build-tag", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"missing test file", "missing-file", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"skipped Go test step", "skipped-step", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"changed test entrypoint", "changed-entrypoint", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"changed earlier CI script", "changed-pretest-script", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"changed workflow", "changed-workflow", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"symlinked test", "symlink-test", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"nested module", "nested-module", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"truncated head tree", "truncated-tree", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
		{"unexpected workflow", "other-workflow", reviewpolicy.CIAdmissionUnavailable, reviewpolicy.CIAdmissionReasonTestReachabilityUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			pr := reachabilityTestPR()
			files := []string{"pkg/tool/builtin/git_test.go"}
			if test.mode == "changed-entrypoint" {
				files = append(files, "scripts/test.sh")
			}
			if test.mode == "changed-pretest-script" {
				files = append(files, "scripts/tests/test_detect_affected_surfaces.sh")
			}
			if test.mode == "changed-workflow" {
				files = append(files, ".github/workflows/ci.yml")
			}
			capture, err := capturePRCIAdmission(reachabilityTestRunner(pr, test.mode),
				prReference{Number: pr.Number, Host: pr.Host, Repository: pr.Repository}, pr, files)
			if err != nil {
				t.Fatal(err)
			}
			if capture.Receipt.Decision != test.wantDecision || capture.Receipt.Reason != test.wantReason {
				t.Fatalf("admission = %s/%s, reachability error %v", capture.Receipt.Decision, capture.Receipt.Reason, capture.ReachabilityErr)
			}
			if err := capture.Receipt.Authorize(prCIAdmissionExpectation(pr, files)); err == nil {
				t.Fatal("non-covered test authorized")
			}
		})
	}
}

func TestCapturePRCIAdmission_UnsupportedTestTypeRemainsUnavailable(t *testing.T) {
	pr := reachabilityTestPR()
	files := []string{"tests/test_parser.py"}
	capture, err := capturePRCIAdmission(reachabilityTestRunner(pr, ""),
		prReference{Number: pr.Number, Host: pr.Host, Repository: pr.Repository}, pr, files)
	if err != nil {
		t.Fatal(err)
	}
	if capture.Receipt.Decision != reviewpolicy.CIAdmissionUnavailable || capture.ReachabilityErr == nil {
		t.Fatalf("unsupported test capture = %#v", capture)
	}
}

func TestVerifyPRGoTestFile_PlatformSuffixRemainsUnavailable(t *testing.T) {
	pr := reachabilityTestPR()
	if err := verifyPRGoTestFile(func(string, ...string) ([]byte, error) {
		t.Fatal("platform-specific test should fail before file fetch")
		return nil, nil
	}, pr, "merge-sha", "pkg/parser_linux_test.go"); err == nil {
		t.Fatal("platform-specific test accepted")
	}
}

func TestVerifyPRGoTestFile_IgnoredFilenameRemainsUnavailable(t *testing.T) {
	pr := reachabilityTestPR()
	if err := verifyPRGoTestFile(func(string, ...string) ([]byte, error) {
		t.Fatal("ignored test should fail before file fetch")
		return nil, nil
	}, pr, "merge-sha", "pkg/_hidden_test.go"); err == nil {
		t.Fatal("ignored test filename accepted")
	}
}

func TestParsePRRequiredJobLink_EnterpriseHostPort(t *testing.T) {
	pr := reachabilityTestPR()
	pr.Host = "github.corp.example:8443"
	runID, jobID, err := parsePRRequiredJobLink(
		"https://github.corp.example:8443/odvcencio/buckley/actions/runs/123/job/456", pr)
	if err != nil || runID != 123 || jobID != 456 {
		t.Fatalf("required job link = %d/%d, error = %v", runID, jobID, err)
	}
}

func TestGoTestScriptIncludesDir_SkipsIgnoredDirectories(t *testing.T) {
	for _, test := range []struct {
		dir  string
		want bool
	}{
		{"pkg/tool/builtin", true},
		{"cmd/buckley", true},
		{"pkg/testdata/fixture", false},
		{"pkg/_hidden", false},
		{"pkg/.hidden", false},
		{"pkg/vendor/example", false},
		{"cmd/other", false},
	} {
		if got := goTestScriptIncludesDir(test.dir); got != test.want {
			t.Errorf("goTestScriptIncludesDir(%q) = %v, want %v", test.dir, got, test.want)
		}
	}
}

func TestRevalidatePRContext_GoTestEvidenceChangeInvalidatesReview(t *testing.T) {
	ctx := stablePRRevalidationContext()
	ctx.Files = []string{"pkg/tool/builtin/git_test.go"}
	target := ctx.target
	capture, err := capturePRCIAdmission(reachabilityTestRunner(ctx.PR, ""), target, ctx.PR, ctx.Files)
	if err != nil || capture.Receipt.Decision != reviewpolicy.CIAdmissionAllow {
		t.Fatalf("capture = %#v, error = %v", capture, err)
	}
	ctx.CIAdmission = capture.Receipt
	for _, test := range []struct {
		name        string
		mode        string
		wantChanged bool
	}{
		{"stable", "", false},
		{"package source disappeared", "no-source", true},
		{"required run head changed", "stale-head", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			base := stablePRRevalidationRunner(prRevalidationOutputs{requiredChecks: `[{"name":"Test","state":"SUCCESS"}]`})
			adapter := reachabilityTestRunner(ctx.PR, test.mode)
			run := func(name string, args ...string) ([]byte, error) {
				switch {
				case name == "gh" && hasPRArgPrefix(args, "pr", "checks", "208", "--json", "name,state,link"):
					return adapter(name, args...)
				case name == "gh" && len(args) > 0 && args[0] == "run":
					return adapter(name, args...)
				case name == "gh" && len(args) > 1 && args[0] == "api" &&
					(strings.Contains(args[1], "/git/trees/") || strings.Contains(args[1], "/git/commits/") ||
						strings.Contains(args[1], "/pulls/") || strings.Contains(args[1], "/contents/") ||
						strings.Contains(args[1], "/actions/runs/")):
					return adapter(name, args...)
				default:
					return base(name, args...)
				}
			}
			changed, err := revalidatePRContext(ctx, run)
			if err != nil {
				t.Fatalf("revalidatePRContext: %v", err)
			}
			if (changed != "") != test.wantChanged {
				t.Fatalf("changed = %q, wantChanged = %v", changed, test.wantChanged)
			}
		})
	}
}

func reachabilityTestPR() *PRInfo {
	return &PRInfo{
		Number: 208, Host: "github.com", Repository: "odvcencio/buckley",
		BaseBranch: "main", BaseSHA: "base-sha", HeadBranch: "topic", HeadSHA: "head-sha",
	}
}

func reachabilityTestRunner(pr *PRInfo, mode string) prCommandRunner {
	start := time.Date(2026, 9, 22, 22, 49, 38, 0, time.UTC)
	end := start.Add(3 * time.Minute)
	return func(name string, args ...string) ([]byte, error) {
		if name != "gh" {
			return nil, fmt.Errorf("unexpected command %s", name)
		}
		switch {
		case hasPRArgPrefix(args, "pr", "checks", "208", "--json", "name,state"):
			return []byte(`[{"name":"Test","state":"SUCCESS"}]`), nil
		case hasPRArgPrefix(args, "pr", "checks", "208", "--json", "name,state,link"):
			link := "https://" + pr.Host + "/" + pr.Repository + "/actions/runs/123/job/456"
			if mode == "wrong-link" {
				link = "https://github.com/other/repo/actions/runs/123/job/456"
			}
			return []byte(fmt.Sprintf(`[{"name":"Test","state":"SUCCESS","link":%q}]`, link)), nil
		case len(args) > 1 && args[0] == "api" && strings.HasSuffix(args[1], "/pulls/208"):
			return []byte(`{"merge_commit_sha":"merge-sha"}`), nil
		case len(args) > 1 && args[0] == "api" && strings.HasSuffix(args[1], "/git/commits/merge-sha"):
			base := pr.BaseSHA
			if mode == "merge-parent-drift" {
				base = "other-base"
			}
			return []byte(fmt.Sprintf(`{"parents":[{"sha":%q},{"sha":%q}]}`, base, pr.HeadSHA)), nil
		case len(args) > 1 && args[0] == "api" && strings.Contains(args[1], "/git/trees/merge-sha?"):
			modeValue := "100644"
			if mode == "symlink-test" {
				modeValue = "120000"
			}
			nested := ""
			if mode == "nested-module" {
				nested = `,{"path":"pkg/tool/go.mod","mode":"100644","type":"blob"}`
			}
			truncated := "false"
			if mode == "truncated-tree" {
				truncated = "true"
			}
			source := `,{"path":"pkg/tool/builtin/git.go","mode":"100644","type":"blob"}`
			if mode == "no-source" {
				source = ""
			}
			if mode == "ignored-source" {
				source = `,{"path":"pkg/tool/builtin/_git.go","mode":"100644","type":"blob"}`
			}
			return []byte(fmt.Sprintf(`{"truncated":%s,"tree":[{"path":"go.mod","mode":"100644","type":"blob"},{"path":"pkg/tool/builtin/git_test.go","mode":%q,"type":"blob"}%s%s]}`,
				truncated, modeValue, source, nested)), nil
		case len(args) > 1 && args[0] == "api" && strings.Contains(args[1], "/contents/scripts/test.sh?ref=merge-sha"):
			content, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "test.sh"))
			if mode == "contract-drift" {
				content = append(content, '#')
			}
			return content, err
		case len(args) > 1 && args[0] == "api" && strings.Contains(args[1], "/contents/.github/workflows/ci.yml?ref=merge-sha"):
			return os.ReadFile(filepath.Join("..", "..", "..", ".github", "workflows", "ci.yml"))
		case len(args) > 1 && args[0] == "api" && strings.Contains(args[1], "/contents/go.mod?ref=merge-sha"):
			return []byte("module m31labs.dev/buckley\n\ngo 1.26.0\n"), nil
		case len(args) > 1 && args[0] == "api" && strings.Contains(args[1], "/contents/pkg/tool/builtin/git.go?ref=merge-sha"):
			if mode == "source-build-tag" {
				return []byte("//go:build integration\n\npackage builtin\n"), nil
			}
			return []byte("package builtin\n"), nil
		case len(args) > 1 && args[0] == "api" && strings.Contains(args[1], "/contents/pkg/tool/builtin/git_test.go?ref=merge-sha"):
			if mode == "missing-file" {
				return nil, errors.New("not found")
			}
			if mode == "build-tag" {
				return []byte("//go:build integration\n\npackage builtin\n"), nil
			}
			return []byte("package builtin\n"), nil
		case len(args) > 1 && args[0] == "api" && strings.Contains(args[1], "/actions/runs/123"):
			workflow := ".github/workflows/ci.yml"
			base := pr.BaseSHA
			if mode == "run-base-drift" {
				base = "other-base"
			}
			if mode == "other-workflow" {
				workflow = ".github/workflows/other.yml"
			}
			return []byte(fmt.Sprintf(`{"path":%q,"head_sha":%q,"event":"pull_request","pull_requests":[{"number":%d,"base":{"sha":%q},"head":{"sha":%q}}]}`,
				workflow, pr.HeadSHA, pr.Number, base, pr.HeadSHA)), nil
		case len(args) > 2 && args[0] == "run" && args[1] == "view" && args[2] == "123":
			head := pr.HeadSHA
			conclusion := "success"
			if mode == "stale-head" {
				head = "other-head"
			}
			if mode == "skipped-step" {
				conclusion = "skipped"
			}
			return []byte(fmt.Sprintf(`{"headSha":%q,"event":"pull_request","status":"completed","conclusion":"success","jobs":[{"databaseId":456,"name":"Test","conclusion":"success","steps":[{"name":"Run Go tests","conclusion":%q,"startedAt":%q,"completedAt":%q}]}]}`,
				head, conclusion, start.Format(time.RFC3339), end.Format(time.RFC3339))), nil
		default:
			return nil, fmt.Errorf("unexpected gh args: %s", strings.Join(args, " "))
		}
	}
}
