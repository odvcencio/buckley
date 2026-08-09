package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/reviewsandbox"
)

type fakeReviewVerifier struct {
	result  reviewsandbox.Result
	request reviewsandbox.Request
	ctx     context.Context
	calls   int
}

func (f *fakeReviewVerifier) Verify(ctx context.Context, request reviewsandbox.Request) reviewsandbox.Result {
	f.calls++
	f.ctx = ctx
	f.request = request
	return f.result
}

func TestRunVerificationToolSuccessRequiresPassAndZeroExit(t *testing.T) {
	cases := []struct {
		name     string
		status   reviewsandbox.Status
		exitCode int
		want     bool
	}{
		{"pass", reviewsandbox.StatusPass, 0, true},
		{"pass with nonzero exit", reviewsandbox.StatusPass, 1, false},
		{"fail", reviewsandbox.StatusFail, 1, false},
		{"unavailable", reviewsandbox.StatusUnavailable, -1, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tool, err := NewRunVerificationTool(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			fake := &fakeReviewVerifier{result: reviewsandbox.Result{
				Kind:     reviewsandbox.KindTest,
				Language: reviewsandbox.LanguageGo,
				Path:     "pkg/tool",
				Pattern:  "TestFocused",
				Command:  "/usr/local/go/bin/go",
				Argv:     []string{"/usr/local/go/bin/go", "test", "."},
				ExitCode: test.exitCode,
				Status:   test.status,
				// CONFIRMED_FAIL requires attributable output; a silent
				// failure is INCONCLUSIVE by design.
				Stderr: "FAIL: TestFocused",
			}}
			tool.verifier = fake
			result, err := tool.Execute(map[string]any{
				"kind":     "test",
				"language": "go",
				"path":     "pkg/tool",
				"pattern":  "TestFocused",
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success != test.want {
				t.Fatalf("Success = %v, want %v; result=%#v", result.Success, test.want, result)
			}
			for key, want := range map[string]any{
				"kind":      "test",
				"path":      "pkg/tool",
				"pattern":   "TestFocused",
				"exit_code": test.exitCode,
				"status":    string(test.status),
			} {
				if got := result.Data[key]; got != want {
					t.Fatalf("Data[%q] = %#v, want %#v", key, got, want)
				}
			}
			wantEvidence := "INCONCLUSIVE"
			if test.status == reviewsandbox.StatusPass && test.exitCode == 0 {
				wantEvidence = "CONFIRMED_PASS"
			} else if test.status == reviewsandbox.StatusFail {
				wantEvidence = "CONFIRMED_FAIL"
			}
			if got := result.Data["evidence"]; got != wantEvidence {
				t.Fatalf("Data[evidence] = %#v, want %q", got, wantEvidence)
			}
			wantProofs := 0
			if test.status == reviewsandbox.StatusPass && test.exitCode == 0 {
				wantProofs = 2
			}
			if got, ok := result.Data["proves"].([]string); !ok || len(got) != wantProofs {
				t.Fatalf("Data[proves] = %#v, want %d entries", result.Data["proves"], wantProofs)
			}
			if got, ok := result.Data["argv"].([]string); !ok || len(got) == 0 {
				t.Fatalf("trusted argv missing: %#v", result.Data["argv"])
			}
		})
	}
}

func TestRunVerificationToolClampsRequestedTimeoutToReviewPlan(t *testing.T) {
	tool, err := NewRunVerificationTool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool.SetTimeoutLimit(90 * time.Second)
	fake := &fakeReviewVerifier{result: reviewsandbox.Result{
		Kind:     reviewsandbox.KindTest,
		Language: reviewsandbox.LanguageGo,
		Path:     ".",
		ExitCode: 0,
		Status:   reviewsandbox.StatusPass,
	}}
	tool.verifier = fake

	result, err := tool.Execute(map[string]any{
		"kind":            "test",
		"language":        "go",
		"timeout_seconds": 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("clamped verification failed: %#v", result)
	}
	if fake.request.Timeout != 90*time.Second {
		t.Fatalf("verification timeout = %s, want 90s", fake.request.Timeout)
	}
	parameters := tool.Parameters()
	if got := parameters.Properties["timeout_seconds"].Default; got != 90 {
		t.Fatalf("schema default timeout = %v, want 90", got)
	}
}

func TestRunVerificationToolUsesCallerContextAndSealedRoot(t *testing.T) {
	root := t.TempDir()
	tool, err := NewRunVerificationTool(root)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeReviewVerifier{result: reviewsandbox.Result{
		Kind:     reviewsandbox.KindBuild,
		Language: reviewsandbox.LanguageGo,
		Path:     ".",
		ExitCode: -1,
		Status:   reviewsandbox.StatusUnavailable,
	}}
	tool.verifier = fake
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := tool.ExecuteWithContext(ctx, map[string]any{"kind": "build", "language": "go"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatal("cancelled verification reported success")
	}
	if fake.ctx != ctx || fake.ctx.Err() != context.Canceled {
		t.Fatalf("verifier did not receive caller context: %#v", fake.ctx)
	}
	if fake.request.SnapshotRoot != tool.snapshotRoot || fake.request.SnapshotRoot == "" {
		t.Fatalf("verifier was not bound to sealed snapshot: %#v", fake.request)
	}
}

func TestNewRunVerificationToolRejectsInvalidSnapshot(t *testing.T) {
	if _, err := NewRunVerificationTool(""); err == nil {
		t.Fatal("empty snapshot root was accepted")
	}
	if _, err := NewRunVerificationTool(t.TempDir() + "/missing"); err == nil {
		t.Fatal("missing snapshot root was accepted")
	}
}

func TestRunVerificationToolRejectsHostTestRequiredInDocker(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(`
- Do not run repo-wide `+"`go test ./...`"+` on the host.
- Focused package/unit tests inside Docker, scoped with `+"`-run`"+` whenever possible.
`), 0o644); err != nil {
		t.Fatal(err)
	}
	tool, err := NewRunVerificationTool(root)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeReviewVerifier{result: reviewsandbox.Result{
		Kind:     reviewsandbox.KindTest,
		Language: reviewsandbox.LanguageGo,
		Status:   reviewsandbox.StatusPass,
		ExitCode: 0,
	}}
	tool.verifier = fake

	result, err := tool.Execute(map[string]any{
		"kind":     "test",
		"language": "go",
		"path":     ".",
		"pattern":  "TestMergeStacks|TestFaithful",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 0 {
		t.Fatalf("forbidden host test reached verifier %d times", fake.calls)
	}
	if result.Success || result.Data["status"] != string(reviewsandbox.StatusUnavailable) ||
		result.Data["evidence"] != "INCONCLUSIVE" {
		t.Fatalf("policy rejection = %#v", result)
	}
	if !strings.Contains(result.Error, "Docker") || !strings.Contains(result.Error, "not started") {
		t.Fatalf("policy rejection error = %q", result.Error)
	}
}

func TestRunVerificationToolAbridgedFailureKeepsOutputTails(t *testing.T) {
	tool, err := NewRunVerificationTool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeReviewVerifier{result: reviewsandbox.Result{
		Kind:     reviewsandbox.KindTest,
		Language: reviewsandbox.LanguageGo,
		Path:     "cmd/buckley",
		Command:  "go",
		ExitCode: 1,
		Status:   reviewsandbox.StatusFail,
		Stdout:   strings.Repeat("noise line\n", 900),
		Stderr:   strings.Repeat("x", 6_000) + "\nFAIL: TestSomething assertion mismatch",
	}}
	tool.verifier = fake
	result, err := tool.Execute(map[string]any{
		"kind":     "test",
		"language": "go",
		"path":     "cmd/buckley",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ShouldAbridge {
		t.Fatalf("large output result = %#v, want abridged", result)
	}
	tail, _ := result.DisplayData["stderr_tail"].(string)
	if !strings.Contains(tail, "FAIL: TestSomething assertion mismatch") {
		t.Fatalf("stderr_tail = %q, want the failure summary retained", tail)
	}
	if len(tail) > verificationTailBytes+8 {
		t.Fatalf("stderr_tail is %d bytes, want bounded", len(tail))
	}
	if result.DisplayData["evidence"] != "CONFIRMED_FAIL" {
		t.Fatalf("evidence = %v, want CONFIRMED_FAIL with attributable output", result.DisplayData["evidence"])
	}
}

func TestRunVerificationToolSilentFailureIsInconclusive(t *testing.T) {
	tool, err := NewRunVerificationTool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeReviewVerifier{result: reviewsandbox.Result{
		Kind:     reviewsandbox.KindTest,
		Language: reviewsandbox.LanguageGo,
		Path:     "cmd/buckley",
		Command:  "go",
		ExitCode: 1,
		Status:   reviewsandbox.StatusFail,
	}}
	tool.verifier = fake
	result, err := tool.Execute(map[string]any{
		"kind":     "test",
		"language": "go",
		"path":     "cmd/buckley",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Data["evidence"] != "INCONCLUSIVE" {
		t.Fatalf("evidence = %v, want INCONCLUSIVE for a failure with no captured output", result.Data["evidence"])
	}
}
