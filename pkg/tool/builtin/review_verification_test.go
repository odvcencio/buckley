package builtin

import (
	"context"
	"testing"

	"m31labs.dev/buckley/pkg/reviewsandbox"
)

type fakeReviewVerifier struct {
	result  reviewsandbox.Result
	request reviewsandbox.Request
	ctx     context.Context
}

func (f *fakeReviewVerifier) Verify(ctx context.Context, request reviewsandbox.Request) reviewsandbox.Result {
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
			if got, ok := result.Data["argv"].([]string); !ok || len(got) == 0 {
				t.Fatalf("trusted argv missing: %#v", result.Data["argv"])
			}
		})
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
