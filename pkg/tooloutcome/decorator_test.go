package tooloutcome

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/tool/external"
)

func TestObservation_ModifyingToolStateFacts(t *testing.T) {
	root := newToolOutcomeGitRepo(t)
	target := filepath.Join(root, "tracked.txt")

	changed := Begin(context.Background(), root, string(tool.ImpactModifying)).
		Finish(context.Background(), agentloop.ToolOutcome{Success: true}, tool.ToolMetadata{Impact: tool.ImpactModifying}, &builtin.Result{Success: true}, nil)
	if !changed.StateObserved || changed.StateChanged {
		t.Fatalf("initial no-op observation = %+v, want observed without change", changed)
	}

	observation := Begin(context.Background(), root, string(tool.ImpactModifying))
	if err := os.WriteFile(target, []byte("after\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	outcome := observation.Finish(context.Background(), agentloop.ToolOutcome{Success: true}, tool.ToolMetadata{Impact: tool.ImpactModifying}, &builtin.Result{Success: true}, nil)
	if !outcome.StateObserved || !outcome.StateChanged {
		t.Fatalf("changed observation = %+v, want observed state change", outcome)
	}
}

func TestObservation_VerificationFactsComeFromTestingMetadataAndResult(t *testing.T) {
	passing := Begin(context.Background(), "", string(tool.ImpactReadOnly)).
		Finish(context.Background(), agentloop.ToolOutcome{Success: true}, tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly, Verification: true}, &builtin.Result{Success: true}, nil)
	if !passing.VerificationObserved || !passing.VerificationPassed {
		t.Fatalf("passing verification = %+v, want observed pass", passing)
	}

	failing := Begin(context.Background(), "", string(tool.ImpactReadOnly)).
		Finish(context.Background(), agentloop.ToolOutcome{Success: false}, tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly, Verification: true}, &builtin.Result{Success: false}, nil)
	if !failing.VerificationObserved || failing.VerificationPassed {
		t.Fatalf("failing verification = %+v, want observed fail", failing)
	}
}

func TestObservation_TrustedVerificationObservesWorkspaceSideEffects(t *testing.T) {
	root := newToolOutcomeGitRepo(t)
	metadata := tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly, Verification: true}
	observation := BeginWithMetadata(context.Background(), root, metadata)
	if err := os.WriteFile(filepath.Join(root, "coverage.out"), []byte("coverage\n"), 0o644); err != nil {
		t.Fatalf("write verification side effect: %v", err)
	}
	outcome := observation.Finish(context.Background(), agentloop.ToolOutcome{Success: true}, metadata, &builtin.Result{Success: true}, nil)
	if !outcome.VerificationObserved || !outcome.VerificationPassed || !outcome.StateObserved || !outcome.StateChanged {
		t.Fatalf("verification side-effect outcome = %+v, want verification and state change", outcome)
	}
}

func TestObservation_CategoryTestingWithoutCapabilityIsNotVerification(t *testing.T) {
	outcome := Begin(context.Background(), "", string(tool.ImpactReadOnly)).
		Finish(context.Background(), agentloop.ToolOutcome{Success: true}, tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly}, &builtin.Result{Success: true}, nil)
	if outcome.VerificationObserved || outcome.VerificationPassed {
		t.Fatalf("category-only testing outcome = %+v, want no verification", outcome)
	}
}

func TestObservation_GenerateTestIsModifyingNotVerification(t *testing.T) {
	root := newToolOutcomeGitRepo(t)
	metadata := tool.GetMetadata(&builtin.GenerateTestTool{})
	observation := Begin(context.Background(), root, string(metadata.Impact))
	if err := os.WriteFile(filepath.Join(root, "tracked_test.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatalf("write generated test: %v", err)
	}
	outcome := observation.Finish(context.Background(), agentloop.ToolOutcome{Success: true}, metadata, &builtin.Result{Success: true}, nil)
	if !outcome.StateObserved || !outcome.StateChanged {
		t.Fatalf("generate_test outcome = %+v, want observed mutation", outcome)
	}
	if outcome.VerificationObserved || outcome.VerificationPassed {
		t.Fatalf("generate_test outcome = %+v, want no verification facts", outcome)
	}
}

func TestObservation_InferredExternalTestNameDoesNotVerify(t *testing.T) {
	metadata := tool.GetMetadata(namedToolOutcomeTestTool("external_test_probe"))
	outcome := Begin(context.Background(), "", string(metadata.Impact)).
		Finish(context.Background(), agentloop.ToolOutcome{Success: true}, metadata, &builtin.Result{Success: true}, nil)
	if metadata.Category != tool.CategoryTesting {
		t.Fatalf("external test metadata category = %v, want testing", metadata.Category)
	}
	if metadata.Verification || outcome.VerificationObserved || outcome.VerificationPassed {
		t.Fatalf("external inferred test outcome = %+v with metadata %+v, want no verification", outcome, metadata)
	}
}

func TestObservation_ExternalRunTestsReplacementDoesNotVerifyByName(t *testing.T) {
	metadata := tool.GetMetadata(namedToolOutcomeTestTool("run_tests"))
	outcome := Begin(context.Background(), "", string(metadata.Impact)).
		Finish(context.Background(), agentloop.ToolOutcome{Success: true}, metadata, &builtin.Result{Success: true}, nil)
	if metadata.Verification || outcome.VerificationObserved || outcome.VerificationPassed {
		t.Fatalf("external run_tests outcome = %+v with metadata %+v, want no verification by name", outcome, metadata)
	}
}

func TestObservation_ExternalRunTestsReplacementSuccessDoesNotVerify(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "tool.sh")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s' '{\"success\":true,\"data\":{\"ok\":true}}'\n"), 0o755); err != nil {
		t.Fatalf("write external tool: %v", err)
	}
	registry := tool.NewRegistry()
	registry.Register(external.NewTool(&external.ToolManifest{
		Name:        "run_tests",
		Description: "external replacement",
		Parameters:  map[string]any{"type": "object"},
		TimeoutMs:   1000,
	}, executable))
	registered, ok := registry.Get("run_tests")
	if !ok {
		t.Fatal("run_tests missing after replacement")
	}
	metadata := tool.GetMetadata(registered)
	result, err := registry.Execute("run_tests", map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	outcome := BeginWithMetadata(context.Background(), "", metadata).
		Finish(context.Background(), agentloop.ToolOutcome{Success: result.Success}, metadata, result, err)
	if metadata.Impact == tool.ImpactReadOnly || metadata.Verification || outcome.VerificationObserved || outcome.VerificationPassed {
		t.Fatalf("external replacement metadata=%+v outcome=%+v, want non-readonly and no verification", metadata, outcome)
	}
}

func TestObservation_FingerprintFailureFailsClosed(t *testing.T) {
	root := t.TempDir()
	outcome := Begin(context.Background(), root, string(tool.ImpactModifying)).
		Finish(context.Background(), agentloop.ToolOutcome{Success: true}, tool.ToolMetadata{Impact: tool.ImpactModifying}, &builtin.Result{Success: true}, nil)
	if !outcome.StateObservationFailed || outcome.StateObserved || outcome.StateChanged || outcome.StateObservationError == "" {
		t.Fatalf("non-git modifying outcome = %+v, want fail-closed state observation error", outcome)
	}
}

func TestObservation_UnbornRepositoryMutationThenVerificationCanComplete(t *testing.T) {
	root := t.TempDir()
	runToolOutcomeGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("staged\n"), 0o644); err != nil {
		t.Fatalf("write staged file: %v", err)
	}
	runToolOutcomeGit(t, root, "add", "tracked.txt")

	mutationMetadata := tool.ToolMetadata{Impact: tool.ImpactModifying}
	mutationObservation := BeginWithMetadata(context.Background(), root, mutationMetadata)
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatalf("edit staged file: %v", err)
	}
	mutation := mutationObservation.Finish(
		context.Background(),
		agentloop.ToolOutcome{Success: true},
		mutationMetadata,
		&builtin.Result{Success: true},
		nil,
	)
	if mutation.StateObservationFailed || !mutation.StateObserved || !mutation.StateChanged {
		t.Fatalf("unborn mutation outcome = %+v, want observed state change", mutation)
	}

	verificationMetadata := tool.ToolMetadata{Category: tool.CategoryTesting, Impact: tool.ImpactReadOnly, Verification: true}
	verification := BeginWithMetadata(context.Background(), root, verificationMetadata).Finish(
		context.Background(),
		agentloop.ToolOutcome{Success: true},
		verificationMetadata,
		&builtin.Result{Success: true},
		nil,
	)
	if verification.StateObservationFailed || !verification.StateObserved || verification.StateChanged || !verification.VerificationPassed {
		t.Fatalf("unborn verification outcome = %+v, want stable passing verification", verification)
	}

	snapshot := agentloop.ProgressSnapshot{
		StateObservedCalls:        2,
		StateChangedCalls:         1,
		LastStateChangeSequence:   1,
		VerificationObservedCalls: 1,
		VerificationPassedCalls:   1,
		LastVerificationSequence:  2,
		LastVerificationPassed:    true,
	}
	contract := agentloop.CompletionContract{
		RequirePostChangeVerification: true,
		RequireObservableChange:       true,
		TaskIntent:                    agentloop.MutationIntent,
	}
	if err := contract.Validate(snapshot); err != nil {
		t.Fatalf("completion contract rejected unborn mutation plus verification: %v", err)
	}
}

func TestObservation_ReadOnlyDoesNotObserveWorkspaceState(t *testing.T) {
	root := newToolOutcomeGitRepo(t)
	observation := Begin(context.Background(), root, string(tool.ImpactReadOnly))
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	outcome := observation.Finish(context.Background(), agentloop.ToolOutcome{Success: true}, tool.ToolMetadata{Impact: tool.ImpactReadOnly}, &builtin.Result{Success: true}, nil)
	if outcome.StateObserved || outcome.StateChanged {
		t.Fatalf("read-only observation = %+v, want no state observation", outcome)
	}
}

func newToolOutcomeGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runToolOutcomeGit(t, root, "init", "-q")
	runToolOutcomeGit(t, root, "config", "user.name", "Buckley Test")
	runToolOutcomeGit(t, root, "config", "user.email", "buckley@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	runToolOutcomeGit(t, root, "add", "tracked.txt")
	runToolOutcomeGit(t, root, "commit", "-qm", "base")
	return root
}

func runToolOutcomeGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

type namedToolOutcomeTestTool string

func (t namedToolOutcomeTestTool) Name() string { return string(t) }
func (t namedToolOutcomeTestTool) Description() string {
	return "test helper"
}
func (t namedToolOutcomeTestTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}
func (t namedToolOutcomeTestTool) Execute(map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true}, nil
}
