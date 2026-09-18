package runner

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type fakeTaskVerificationTool struct {
	result *builtin.Result
	err    error
	seen   []map[string]any
	after  func()
}

func (f *fakeTaskVerificationTool) ExecuteWithContext(_ context.Context, params map[string]any) (*builtin.Result, error) {
	cloned := make(map[string]any, len(params))
	for key, value := range params {
		cloned[key] = value
	}
	f.seen = append(f.seen, cloned)
	if f.after != nil {
		f.after()
	}
	return f.result, f.err
}

func TestRunTaskVerificationChecks_LegacyProseIsUnverified(t *testing.T) {
	runner := &Runner{}
	status, results, err := runner.runTaskVerificationChecks(context.Background(), nil, orchestrator.Task{
		ID:           "task-legacy",
		Verification: []string{"Run the important tests"},
	}, taskVerificationHost{})
	if err == nil || !strings.Contains(err.Error(), "legacy verification prose") {
		t.Fatalf("error = %v, want legacy prose rejection", err)
	}
	if status != taskVerificationStatusUnverified {
		t.Fatalf("status = %q, want %q", status, taskVerificationStatusUnverified)
	}
	if len(results) != 0 {
		t.Fatalf("results = %+v, want none", results)
	}
}

func TestRunTaskVerificationChecks_NoChecksIsNotRequested(t *testing.T) {
	runner := &Runner{}
	status, results, err := runner.runTaskVerificationChecks(context.Background(), nil, orchestrator.Task{ID: "task-none"}, taskVerificationHost{})
	if err != nil {
		t.Fatalf("runTaskVerificationChecks: %v", err)
	}
	if status != taskVerificationStatusNotRequested {
		t.Fatalf("status = %q, want %q", status, taskVerificationStatusNotRequested)
	}
	if len(results) != 0 {
		t.Fatalf("results = %+v, want none", results)
	}
}

func TestRunTaskVerificationChecks_ValidatesCheckIDsBeforeToolEffects(t *testing.T) {
	runner := &Runner{}
	for _, task := range []orchestrator.Task{
		{ID: "blank", VerificationChecks: []orchestrator.TaskVerificationCheck{{Kind: "test"}}},
		{ID: "dupe", VerificationChecks: []orchestrator.TaskVerificationCheck{
			{ID: "same", Kind: "test"},
			{ID: "same", Kind: "build"},
		}},
		{ID: "timeout", VerificationChecks: []orchestrator.TaskVerificationCheck{{ID: "slow", Kind: "test", TimeoutSeconds: 901}}},
	} {
		called := false
		status, results, err := runner.runTaskVerificationChecks(context.Background(), nil, task, taskVerificationHost{
			makeTool: func(string, string, time.Duration) (taskVerificationTool, func(), error) {
				called = true
				return nil, nil, nil
			},
		})
		if err == nil {
			t.Fatalf("task %s unexpectedly passed validation", task.ID)
		}
		if called {
			t.Fatalf("task %s created verification tool before validating check IDs", task.ID)
		}
		if status != taskVerificationStatusUnverified || len(results) != 0 {
			t.Fatalf("task %s status/results = %q/%+v, want unverified/no results", task.ID, status, results)
		}
	}
}

func TestVerifyTaskExecutionRecord_StructuredPassUsesCapturedSnapshotAndHostEvidence(t *testing.T) {
	repo := initRunnerVerificationRepo(t)
	tool := &fakeTaskVerificationTool{result: &builtin.Result{
		Success: true,
		Data: map[string]any{
			"status":    "PASS",
			"kind":      "test",
			"language":  "go",
			"path":      ".",
			"exit_code": 0,
			"evidence":  "CONFIRMED_PASS",
		},
	}}
	var gotSnapshotRoot, gotSourceRoot string
	runner := &Runner{}
	record := &orchestrator.TaskExecutionRecord{TaskID: "task-pass"}
	err := runner.verifyTaskExecutionRecordWithHost(context.Background(), &orchestrator.Plan{
		Context: orchestrator.PlanContext{RepoRoot: repo},
	}, orchestrator.Task{
		ID: "task-pass",
		VerificationChecks: []orchestrator.TaskVerificationCheck{{
			ID: "go-unit", Kind: "test", Language: "go", Path: ".", Pattern: "TestThing", TimeoutSeconds: 7,
		}},
	}, record, taskVerificationHost{makeTool: func(snapshotRoot, sourceRoot string, timeout time.Duration) (taskVerificationTool, func(), error) {
		gotSnapshotRoot = snapshotRoot
		gotSourceRoot = sourceRoot
		if timeout != 0 {
			t.Fatalf("host timeout = %s, want unchanged default", timeout)
		}
		if _, err := os.Stat(filepath.Join(snapshotRoot, "go.mod")); err != nil {
			t.Fatalf("snapshot root missing go.mod: %v", err)
		}
		return tool, nil, nil
	}})
	if err != nil {
		t.Fatalf("verifyTaskExecutionRecordWithHost: %v", err)
	}
	if record.VerificationStatus != taskVerificationStatusPass {
		t.Fatalf("record status = %q, want pass", record.VerificationStatus)
	}
	if len(record.VerificationResults) != 1 {
		t.Fatalf("verification results = %+v, want one", record.VerificationResults)
	}
	got := record.VerificationResults[0]
	if got.CheckID != "go-unit" || got.Status != "PASS" || got.SnapshotID == "" {
		t.Fatalf("verification result = %+v, want host pass with snapshot", got)
	}
	if !strings.HasPrefix(got.EvidenceID, "task-verification:v1:") {
		t.Fatalf("evidence id = %q, want host-generated receipt", got.EvidenceID)
	}
	if gotSourceRoot != repo || gotSnapshotRoot == "" || gotSnapshotRoot == repo {
		t.Fatalf("roots snapshot=%q source=%q repo=%q", gotSnapshotRoot, gotSourceRoot, repo)
	}
	if len(tool.seen) != 1 || tool.seen[0]["kind"] != "test" || tool.seen[0]["language"] != "go" ||
		tool.seen[0]["path"] != "." || tool.seen[0]["pattern"] != "TestThing" || tool.seen[0]["timeout_seconds"] != 7 {
		t.Fatalf("tool params = %+v, want bounded run_verification schema", tool.seen)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Result, &payload); err != nil {
		t.Fatalf("result JSON: %v", err)
	}
	if payload["success"] != true {
		t.Fatalf("result payload = %+v, want success true", payload)
	}
}

func TestRunTaskVerificationChecks_MixedLegacyProsePreservesStructuredResultsButBlocksPass(t *testing.T) {
	repo := initRunnerVerificationRepo(t)
	tool := &fakeTaskVerificationTool{result: &builtin.Result{
		Success: true,
		Data:    map[string]any{"status": "PASS"},
	}}
	runner := &Runner{}
	status, results, err := runner.runTaskVerificationChecks(context.Background(), &orchestrator.Plan{
		Context: orchestrator.PlanContext{RepoRoot: repo},
	}, orchestrator.Task{
		ID:           "task-mixed",
		Verification: []string{"Confirm the deployment manually"},
		VerificationChecks: []orchestrator.TaskVerificationCheck{{
			ID: "unit", Kind: "test", Language: "go",
		}},
	}, taskVerificationHost{makeTool: func(string, string, time.Duration) (taskVerificationTool, func(), error) {
		return tool, nil, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "legacy verification prose") {
		t.Fatalf("error = %v, want legacy prose to remain unverified", err)
	}
	if status != taskVerificationStatusUnverified {
		t.Fatalf("status = %q, want unverified", status)
	}
	if len(results) != 1 || results[0].Status != "PASS" {
		t.Fatalf("results = %+v, want retained structured pass", results)
	}
}

func TestRunTaskVerificationChecks_NonPassBlocksCompletionButRetainsResult(t *testing.T) {
	repo := initRunnerVerificationRepo(t)
	tool := &fakeTaskVerificationTool{result: &builtin.Result{
		Success: false,
		Error:   "verification command failed",
		Data: map[string]any{
			"status":    "FAIL",
			"exit_code": 1,
			"stderr":    "boom",
		},
	}}
	runner := &Runner{}
	status, results, err := runner.runTaskVerificationChecks(context.Background(), &orchestrator.Plan{
		Context: orchestrator.PlanContext{RepoRoot: repo},
	}, orchestrator.Task{
		ID:                 "task-fail",
		VerificationChecks: []orchestrator.TaskVerificationCheck{{ID: "check-fail", Kind: "test", Language: "go"}},
	}, taskVerificationHost{makeTool: func(string, string, time.Duration) (taskVerificationTool, func(), error) {
		return tool, nil, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "did not pass: FAIL") {
		t.Fatalf("error = %v, want failed check", err)
	}
	if status != taskVerificationStatusFail {
		t.Fatalf("status = %q, want fail", status)
	}
	if len(results) != 1 || results[0].Status != "FAIL" || results[0].EvidenceID == "" || len(results[0].Result) == 0 {
		t.Fatalf("results = %+v, want retained fail evidence", results)
	}
}

func TestRunStructuredTaskVerification_RequiresExplicitTrustworthyPass(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *builtin.Result
		err    error
		want   string
	}{
		{
			name:   "success without explicit status",
			result: &builtin.Result{Success: true},
			want:   "no explicit status",
		},
		{
			name:   "pass with success false",
			result: &builtin.Result{Success: false, Data: map[string]any{"status": "PASS"}},
			want:   "success=false",
		},
		{
			name:   "pass with result error",
			result: &builtin.Result{Success: true, Error: "boom", Data: map[string]any{"status": "PASS"}},
			want:   "result error",
		},
		{
			name:   "pass with execution error",
			result: &builtin.Result{Success: true, Data: map[string]any{"status": "PASS"}},
			err:    context.Canceled,
			want:   "execution error",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &fakeTaskVerificationTool{result: tc.result, err: tc.err}
			got, err := runStructuredTaskVerification(context.Background(), tool, "snapshot-1", "task-trust",
				orchestrator.TaskVerificationCheck{ID: "trust", Kind: "test"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if got.Status != "UNAVAILABLE" {
				t.Fatalf("status = %q, want UNAVAILABLE", got.Status)
			}
			if got.EvidenceID == "" || !strings.Contains(string(got.Result), "success") {
				t.Fatalf("result = %+v, want retained unverified receipt", got)
			}
		})
	}
}

func TestRunStructuredTaskVerification_EvidenceBindsExactCheckDefinition(t *testing.T) {
	tool := &fakeTaskVerificationTool{result: &builtin.Result{Success: true, Data: map[string]any{"status": "PASS"}}}
	first, err := runStructuredTaskVerification(context.Background(), tool, "snapshot-1", "task-bind",
		orchestrator.TaskVerificationCheck{ID: "unit", Kind: "test", Language: "go", Path: ".", Pattern: "TestA"})
	if err != nil {
		t.Fatalf("first verification: %v", err)
	}
	second, err := runStructuredTaskVerification(context.Background(), tool, "snapshot-1", "task-bind",
		orchestrator.TaskVerificationCheck{ID: "unit", Kind: "test", Language: "go", Path: ".", Pattern: "TestB"})
	if err != nil {
		t.Fatalf("second verification: %v", err)
	}
	if first.EvidenceID == second.EvidenceID {
		t.Fatalf("evidence id did not bind check definition: %q", first.EvidenceID)
	}
}

func TestRunTaskVerificationChecks_PathEscapeIsUnavailableThroughBoundedTool(t *testing.T) {
	repo := initRunnerVerificationRepo(t)
	runner := &Runner{}
	status, results, err := runner.runTaskVerificationChecks(context.Background(), &orchestrator.Plan{
		Context: orchestrator.PlanContext{RepoRoot: repo},
	}, orchestrator.Task{
		ID:                 "task-escape",
		VerificationChecks: []orchestrator.TaskVerificationCheck{{ID: "escape", Kind: "test", Language: "go", Path: "../outside"}},
	}, taskVerificationHost{})
	if err == nil {
		t.Fatal("path escape unexpectedly passed")
	}
	if status != taskVerificationStatusFail {
		t.Fatalf("status = %q, want fail", status)
	}
	if len(results) != 1 || results[0].Status != "UNAVAILABLE" {
		t.Fatalf("results = %+v, want unavailable path escape result", results)
	}
	if !strings.Contains(string(results[0].Result), "escapes") && !strings.Contains(string(results[0].Result), "outside") {
		t.Fatalf("result = %s, want path escape diagnostic", results[0].Result)
	}
}

func TestRunTaskVerificationChecks_MaterializedWorkspaceChangedDuringChecksRejectsPass(t *testing.T) {
	repo := initRunnerVerificationRepo(t)
	var snapshotRoot string
	tool := &fakeTaskVerificationTool{
		result: &builtin.Result{
			Success: true,
			Data:    map[string]any{"status": "PASS"},
		},
		after: func() {
			if err := os.WriteFile(filepath.Join(snapshotRoot, "sample.go"), []byte("package sample\n\nconst changed = true\n"), 0o644); err != nil {
				t.Fatalf("mutate materialized snapshot: %v", err)
			}
		},
	}
	runner := &Runner{}
	status, results, err := runner.runTaskVerificationChecks(context.Background(), &orchestrator.Plan{
		Context: orchestrator.PlanContext{RepoRoot: repo},
	}, orchestrator.Task{
		ID:                 "task-materialized-stale",
		VerificationChecks: []orchestrator.TaskVerificationCheck{{ID: "check-pass", Kind: "test", Language: "go"}},
	}, taskVerificationHost{makeTool: func(root, _ string, _ time.Duration) (taskVerificationTool, func(), error) {
		snapshotRoot = root
		return tool, nil, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "materialized task verification snapshot changed") {
		t.Fatalf("error = %v, want materialized snapshot rejection", err)
	}
	if status != taskVerificationStatusUnverified {
		t.Fatalf("status = %q, want unverified", status)
	}
	if len(results) != 2 || results[0].Status != "PASS" || results[1].CheckID != "__materialized_snapshot__" ||
		results[1].Status != "UNAVAILABLE" {
		t.Fatalf("results = %+v, want retained pass plus materialized snapshot rejection", results)
	}
}

func TestRunTaskVerificationChecks_SourceChangedDuringChecksRejectsPass(t *testing.T) {
	repo := initRunnerVerificationRepo(t)
	tool := &fakeTaskVerificationTool{
		result: &builtin.Result{
			Success: true,
			Data:    map[string]any{"status": "PASS"},
		},
		after: func() {
			if err := os.WriteFile(filepath.Join(repo, "late_untracked.go"), []byte("package sample\n"), 0o644); err != nil {
				t.Fatalf("write late untracked source: %v", err)
			}
		},
	}
	runner := &Runner{}
	status, results, err := runner.runTaskVerificationChecks(context.Background(), &orchestrator.Plan{
		Context: orchestrator.PlanContext{RepoRoot: repo},
	}, orchestrator.Task{
		ID:                 "task-stale",
		VerificationChecks: []orchestrator.TaskVerificationCheck{{ID: "check-pass", Kind: "test", Language: "go"}},
	}, taskVerificationHost{makeTool: func(string, string, time.Duration) (taskVerificationTool, func(), error) {
		return tool, nil, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "source changed during checks") {
		t.Fatalf("error = %v, want source changed rejection", err)
	}
	if status != taskVerificationStatusUnverified {
		t.Fatalf("status = %q, want unverified", status)
	}
	if len(results) != 2 || results[0].Status != "PASS" || results[1].CheckID != "__source_snapshot__" ||
		results[1].Status != "UNAVAILABLE" {
		t.Fatalf("results = %+v, want retained pass plus stale-source rejection", results)
	}
}

func initRunnerVerificationRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runRunnerVerificationGit(t, repo, "init", "-q")
	runRunnerVerificationGit(t, repo, "config", "user.name", "Buckley Test")
	runRunnerVerificationGit(t, repo, "config", "user.email", "buckley@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.test/runnerverify\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "sample.go"), []byte("package sample\n"), 0o644); err != nil {
		t.Fatalf("write sample.go: %v", err)
	}
	runRunnerVerificationGit(t, repo, "add", ".")
	runRunnerVerificationGit(t, repo, "commit", "-qm", "base")
	return repo
}

func runRunnerVerificationGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
