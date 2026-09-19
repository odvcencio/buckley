package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/ralph"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

func newGoalTestLedger(t *testing.T) *runledger.SQLiteStore {
	t.Helper()
	ledger, err := runledger.New(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	return ledger
}

func TestRunGoalStart_PersistsExactModelRequestContract(t *testing.T) {
	t.Setenv(envBuckleyDataDir, t.TempDir())
	previousModel := modelOverrideFlag
	previousProviderResolver := resolveGoalStartProviderFn
	previousWorkspace := goalStartWorkspaceFn
	modelOverrideFlag = ""
	workspace := t.TempDir()
	license := readBuckleyLicenseForTest(t)
	if err := os.WriteFile(filepath.Join(workspace, "LICENSE"), license, 0o644); err != nil {
		t.Fatalf("write test license: %v", err)
	}
	resolveGoalStartProviderFn = func(string) (string, error) { return "openrouter", nil }
	goalStartWorkspaceFn = func() (string, error) { return workspace, nil }
	t.Cleanup(func() {
		modelOverrideFlag = previousModel
		resolveGoalStartProviderFn = previousProviderResolver
		goalStartWorkspaceFn = previousWorkspace
	})

	err := runGoalStart([]string{
		"--model", "stealth/ox-alpha",
		"--reasoning-effort", "max",
		"--openrouter-zdr",
		"--openrouter-data-collection", "deny",
		"--task", "harmless probe",
		"probe the request shape",
	})
	if err != nil {
		t.Fatalf("runGoalStart: %v", err)
	}

	stores, cleanup, err := openGoalStores()
	if err != nil {
		t.Fatalf("openGoalStores: %v", err)
	}
	defer cleanup()
	runs, err := stores.ledger.ListRuns(context.Background(), runledger.RunQuery{SessionID: "goal-cli", Limit: 2})
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %+v, %v", runs, err)
	}
	events, err := stores.ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: runs[0].RunID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, event := range events {
		if event.Type != runledger.EventTaskCreated || event.Payload["kind"] != "goal" {
			continue
		}
		if event.Payload["model"] != "stealth/ox-alpha" || event.Payload["reasoning_effort"] != "max" || event.Payload["retention_mode"] != goalloop.GoalRetentionZDR || event.Payload["openrouter_zdr"] != true || event.Payload["data_collection"] != "deny" || event.Payload["model_policy"] != "strict_zdr" || event.Payload["model_policy_action"] != "allow" {
			t.Fatalf("goal request payload = %#v", event.Payload)
		}
		return
	}
	t.Fatal("goal.created payload not found")
}

func TestRunGoalStart_NonZDRBindsRecognizedLicense(t *testing.T) {
	t.Setenv(envBuckleyDataDir, t.TempDir())
	previousProviderResolver := resolveGoalStartProviderFn
	previousWorkspace := goalStartWorkspaceFn
	workspace := t.TempDir()
	license := readBuckleyLicenseForTest(t)
	if err := os.WriteFile(filepath.Join(workspace, "LICENSE"), license, 0o644); err != nil {
		t.Fatalf("write test license: %v", err)
	}
	resolveGoalStartProviderFn = func(string) (string, error) { return "openrouter", nil }
	goalStartWorkspaceFn = func() (string, error) { return workspace, nil }
	t.Cleanup(func() {
		resolveGoalStartProviderFn = previousProviderResolver
		goalStartWorkspaceFn = previousWorkspace
	})

	if err := runGoalStart([]string{
		"--model", "stealth/ox-alpha",
		"--reasoning-effort", "max",
		"--openrouter-no-zdr",
		"--openrouter-data-collection", "deny",
		"probe non-ZDR request shape",
	}); err != nil {
		t.Fatalf("runGoalStart: %v", err)
	}
	stores, cleanup, err := openGoalStores()
	if err != nil {
		t.Fatalf("openGoalStores: %v", err)
	}
	defer cleanup()
	runs, err := stores.ledger.ListRuns(context.Background(), runledger.RunQuery{SessionID: "goal-cli", Limit: 2})
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %+v, %v", runs, err)
	}
	loop, err := goalloop.New(goalloop.Config{Ledger: stores.ledger, Checkpoints: stores.checkpoints, SessionID: "goal-cli"})
	if err != nil {
		t.Fatalf("goalloop.New: %v", err)
	}
	goal, _, err := loop.LoadGoal(context.Background(), runs[0].RunID)
	if err != nil {
		t.Fatalf("LoadGoal: %v", err)
	}
	request := goal.ModelRequest
	if request.RetentionMode != goalloop.GoalRetentionNonZDR || request.OpenRouterZDR || request.OpenRouterDataCollection != "deny" || request.Policy != "oss_non_zdr" || request.PolicyReasonCode != "oss_license_verified" {
		t.Fatalf("model request = %+v", request)
	}
	if request.WorkspaceLicense.ID != goalloop.LicenseIDMIT || request.WorkspaceLicense.SHA256 == "" || request.WorkspaceLicense.ManifestSHA256 == "" {
		t.Fatalf("license evidence = %+v", request.WorkspaceLicense)
	}
}

func TestRunGoalAudit_RendersModelDataPolicyWithoutPrivateEvidence(t *testing.T) {
	t.Setenv(envBuckleyDataDir, t.TempDir())
	stores, cleanup, err := openGoalStores()
	if err != nil {
		t.Fatalf("openGoalStores: %v", err)
	}
	defer cleanup()
	const runID = "run-model-policy-audit"
	if _, err := stores.ledger.StartRun(context.Background(), runledger.AgentRun{RunID: runID, SessionID: "goal-cli"}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := stores.ledger.Append(context.Background(), runledger.Event{
		ID: runledger.NewEventID(), Type: runledger.EventControllerDecision, RunID: runID,
		Payload: map[string]any{
			"kind": "model_data_policy", "action": "allow", "policy": "strict_zdr", "reason_code": "zdr_enforced",
			"license_path": "/private/workspace/LICENSE", "evidence_body": "audit-secret-sentinel",
		},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var auditErr error
	output := captureStdout(t, func() { auditErr = runGoalAudit([]string{runID}) })
	if auditErr != nil {
		t.Fatalf("runGoalAudit: %v", auditErr)
	}
	for _, want := range []string{"decide model-data", "action=allow", "policy=strict_zdr", "reason=zdr_enforced"} {
		if !strings.Contains(output, want) {
			t.Fatalf("audit output missing %q:\n%s", want, output)
		}
	}
	for _, forbidden := range []string{"audit-secret-sentinel", "/private/workspace", "<nil>"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("audit output contains private/invalid value %q:\n%s", forbidden, output)
		}
	}
}

func TestRunGoalStart_ModelDataPolicyFailsClosedBeforeIntake(t *testing.T) {
	previousProviderResolver := resolveGoalStartProviderFn
	previousWorkspace := goalStartWorkspaceFn
	t.Cleanup(func() {
		resolveGoalStartProviderFn = previousProviderResolver
		goalStartWorkspaceFn = previousWorkspace
	})
	tests := []struct {
		name          string
		args          []string
		provider      string
		wantError     string
		wantProviders int
	}{
		{name: "ox retention omitted", args: []string{"--model", "stealth/ox-alpha", "probe"}, wantError: "requires either", wantProviders: 0},
		{name: "data policy without retention", args: []string{"--model", "stealth/ox-alpha", "--openrouter-data-collection", "deny", "probe"}, wantError: "requires --openrouter-zdr or --openrouter-no-zdr", wantProviders: 0},
		{name: "unqualified strict model", args: []string{"--model", "ox-alpha", "--openrouter-zdr", "probe"}, wantError: "canonical provider/model", wantProviders: 0},
		{name: "non zdr data policy omitted", args: []string{"--model", "stealth/ox-alpha", "--openrouter-no-zdr", "probe"}, provider: "openrouter", wantError: "data_collection_policy_missing", wantProviders: 1},
		{name: "legacy missing license", args: []string{"--model", "openai/gpt-5", "probe"}, wantError: "license_missing", wantProviders: 0},
		{name: "strict unsupported provider", args: []string{"--model", "stealth/ox-alpha", "--openrouter-zdr", "probe"}, provider: "anthropic", wantError: "zdr_unenforceable", wantProviders: 1},
		{name: "explicit false rejected", args: []string{"--model", "stealth/ox-alpha", "--openrouter-zdr=false", "probe"}, wantError: "without an explicit false", wantProviders: 0},
		{name: "strict does not require license", args: []string{"--model", "stealth/ox-alpha", "--openrouter-zdr", "probe"}, provider: "openrouter", wantProviders: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			t.Setenv(envBuckleyDataDir, dataDir)
			workspace := t.TempDir()
			goalStartWorkspaceFn = func() (string, error) { return workspace, nil }
			providerCalls := 0
			resolveGoalStartProviderFn = func(string) (string, error) {
				providerCalls++
				return tt.provider, nil
			}
			err := runGoalStart(tt.args)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("runGoalStart: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("runGoalStart error = %v, want %q", err, tt.wantError)
			}
			if providerCalls != tt.wantProviders {
				t.Fatalf("provider resolver calls = %d, want %d", providerCalls, tt.wantProviders)
			}
			if tt.wantError != "" {
				if _, statErr := os.Stat(filepath.Join(dataDir, "ledger.db")); !os.IsNotExist(statErr) {
					t.Fatalf("blocked intake created ledger state: %v", statErr)
				}
			}
		})
	}
}

func TestRunGoalStart_ModelDataPolicyIgnoresPermissiveUserOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	overrideDir := filepath.Join(home, ".buckley", "rules", "runtime")
	if err := os.MkdirAll(overrideDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overrideDir, "model_data_policy.arb"), []byte(permissiveModelDataPolicyOverride), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	dataDir := t.TempDir()
	t.Setenv(envBuckleyDataDir, dataDir)
	previousProviderResolver := resolveGoalStartProviderFn
	previousWorkspace := goalStartWorkspaceFn
	workspace := t.TempDir()
	providerCalls := 0
	resolveGoalStartProviderFn = func(string) (string, error) {
		providerCalls++
		return "openrouter", nil
	}
	goalStartWorkspaceFn = func() (string, error) { return workspace, nil }
	t.Cleanup(func() {
		resolveGoalStartProviderFn = previousProviderResolver
		goalStartWorkspaceFn = previousWorkspace
	})

	err := runGoalStart([]string{"--model", "openai/gpt-5", "must remain blocked"})
	if err == nil || !strings.Contains(err.Error(), "license_missing") {
		t.Fatalf("runGoalStart error = %v, embedded denial must beat override", err)
	}
	if providerCalls != 0 {
		t.Fatalf("provider resolver calls = %d, want zero for legacy gate", providerCalls)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "ledger.db")); !os.IsNotExist(statErr) {
		t.Fatalf("blocked override created ledger state: %v", statErr)
	}
}

func TestRunGoalRun_ExternalBackendPolicyBlocksBeforeBackendConstruction(t *testing.T) {
	previousBackendFor := goalBackendForFn
	previousWorkspace := goalRunWorkspaceFn
	t.Cleanup(func() {
		goalBackendForFn = previousBackendFor
		goalRunWorkspaceFn = previousWorkspace
	})
	licenseBytes := readBuckleyLicenseForTest(t)
	tests := []struct {
		name       string
		goal       func(*testing.T, string) goalloop.Goal
		mutate     func(*testing.T, string)
		wantReason string
	}{
		{
			name: "strict zdr explicit contract",
			goal: func(_ *testing.T, workspace string) goalloop.Goal {
				return goalloop.Goal{Statement: "strict", WorkspaceRoot: workspace, ModelRequest: goalloop.GoalModelRequest{
					PolicyVersion: goalloop.GoalModelPolicyVersionV1, Policy: "strict_zdr", PolicyAction: "allow", PolicyReasonCode: "zdr_enforced",
					Model: "stealth/ox-alpha", RetentionMode: goalloop.GoalRetentionZDR, OpenRouterZDR: true,
				}}
			},
			wantReason: "explicit_model_policy_unenforceable",
		},
		{
			name: "bound oss license changed",
			goal: func(t *testing.T, workspace string) goalloop.Goal {
				if err := os.WriteFile(filepath.Join(workspace, "LICENSE"), licenseBytes, 0o644); err != nil {
					t.Fatalf("write license: %v", err)
				}
				inspection, err := goalloop.InspectWorkspaceLicense(workspace)
				if err != nil {
					t.Fatalf("InspectWorkspaceLicense: %v", err)
				}
				return goalloop.Goal{Statement: "oss", WorkspaceRoot: workspace, ModelRequest: goalloop.GoalModelRequest{
					PolicyVersion: goalloop.GoalModelPolicyVersionV1, Policy: "oss_legacy", PolicyAction: "allow", PolicyReasonCode: "oss_license_verified",
					RetentionMode: goalloop.GoalRetentionLegacy, WorkspaceLicense: inspection.Evidence,
				}}
			},
			mutate: func(t *testing.T, workspace string) {
				if err := os.WriteFile(filepath.Join(workspace, "LICENSE"), []byte("Proprietary and confidential. All rights reserved.\n"), 0o644); err != nil {
					t.Fatalf("replace license: %v", err)
				}
			},
			wantReason: "explicit_model_policy_unenforceable",
		},
		{
			name: "legacy missing license",
			goal: func(_ *testing.T, workspace string) goalloop.Goal {
				return goalloop.Goal{Statement: "legacy", WorkspaceRoot: workspace}
			},
			wantReason: "license_missing",
		},
		{
			name: "legacy license changed to proprietary",
			goal: func(t *testing.T, workspace string) goalloop.Goal {
				if err := os.WriteFile(filepath.Join(workspace, "LICENSE"), licenseBytes, 0o644); err != nil {
					t.Fatalf("write license: %v", err)
				}
				return goalloop.Goal{Statement: "legacy", WorkspaceRoot: workspace}
			},
			mutate: func(t *testing.T, workspace string) {
				if err := os.WriteFile(filepath.Join(workspace, "LICENSE"), []byte("Proprietary and confidential. All rights reserved.\n"), 0o644); err != nil {
					t.Fatalf("replace license: %v", err)
				}
			},
			wantReason: "license_proprietary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			t.Setenv(envBuckleyDataDir, dataDir)
			workspace := t.TempDir()
			goalRunWorkspaceFn = func() (string, error) { return workspace, nil }
			stores, cleanup, err := openGoalStores()
			if err != nil {
				t.Fatalf("openGoalStores: %v", err)
			}
			loop, err := goalloop.New(goalloop.Config{Ledger: stores.ledger, Checkpoints: stores.checkpoints, SessionID: "goal-cli"})
			if err != nil {
				t.Fatalf("goalloop.New: %v", err)
			}
			intake, err := loop.Start(context.Background(), tt.goal(t, workspace))
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			cleanup()
			if tt.mutate != nil {
				tt.mutate(t, workspace)
			}
			backendConstructions := 0
			goalBackendForFn = func(string) (ralph.Backend, error) {
				backendConstructions++
				return nil, fmt.Errorf("must not construct backend")
			}
			err = runGoalRun([]string{"--backend", "claude", intake.RunID})
			if err == nil || !strings.Contains(err.Error(), tt.wantReason) {
				t.Fatalf("runGoalRun error = %v, want %q", err, tt.wantReason)
			}
			if backendConstructions != 0 {
				t.Fatalf("backend constructions = %d, want zero", backendConstructions)
			}
			checkStores, checkCleanup, err := openGoalStores()
			if err != nil {
				t.Fatalf("reopen stores: %v", err)
			}
			defer checkCleanup()
			events, err := checkStores.ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventControllerDecision}})
			if err != nil {
				t.Fatalf("ListEvents: %v", err)
			}
			if len(events) != 0 {
				t.Fatalf("blocked external preflight appended controller events: %+v", events)
			}
		})
	}
}

const permissiveModelDataPolicyOverride = `
outcome ModelDataPolicy {
    action: string
    reason_code: string
    policy: string
}

strategy model_data_policy returns ModelDataPolicy {
    else PermitEverything {
        action: "allow",
        reason_code: "override_allow",
        policy: "oss_legacy",
    }
}
`

func TestEnsureDurableGoalRunOpen_RejectsTerminalBeforeRuntimeMutation(t *testing.T) {
	ledger := newGoalTestLedger(t)
	run, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-terminal", SessionID: "goal-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := ledger.EndRun(context.Background(), run.RunID, "completed", time.Now().UTC(), nil); err != nil {
		t.Fatalf("EndRun: %v", err)
	}
	err = ensureDurableGoalRunOpen(context.Background(), ledger, run.RunID)
	if err == nil || !strings.Contains(err.Error(), "already finalized as completed") || !strings.Contains(err.Error(), "goal report") {
		t.Fatalf("ensureDurableGoalRunOpen error = %v", err)
	}
}

func TestDurableGoalResumeFence_UsesLatestIncompleteGeneration(t *testing.T) {
	ledger := newGoalTestLedger(t)
	run, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-fence", SessionID: "goal-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	appendGeneration := func(instanceID string, incomplete, failure bool) {
		t.Helper()
		generation, err := durableWorkflowGeneration(run.RunID, instanceID)
		if err != nil {
			t.Fatalf("durableWorkflowGeneration: %v", err)
		}
		if _, err := ledger.Append(context.Background(), runledger.Event{
			RunID: run.RunID,
			Type:  runledger.EventDurableGoalGeneration,
			Payload: map[string]any{
				"run_id":               run.RunID,
				"workflow_instance_id": instanceID,
				"generation":           generation,
				"incomplete":           incomplete,
				"failure":              failure,
			},
		}); err != nil {
			t.Fatalf("Append generation: %v", err)
		}
	}
	appendGeneration("goal-run-fence", true, false)
	appendGeneration("goal-run-fence::resume::1", true, false)
	appendGeneration("goal-run-fence::resume::2", false, false)

	fence, err := durableGoalResumeFence(context.Background(), ledger, run.RunID)
	if err != nil {
		t.Fatalf("durableGoalResumeFence: %v", err)
	}
	if fence != "goal-run-fence::resume::1" {
		t.Fatalf("fence = %q, want latest incomplete generation", fence)
	}
}

func TestDurableGoalResumeFence_RejectsNonCanonicalGenerationEvent(t *testing.T) {
	ledger := newGoalTestLedger(t)
	run, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-bad-fence", SessionID: "goal-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := ledger.Append(context.Background(), runledger.Event{
		RunID: run.RunID,
		Type:  runledger.EventDurableGoalGeneration,
		Payload: map[string]any{
			"run_id":               run.RunID,
			"workflow_instance_id": "goal-run-bad-fence::resume::01",
			"generation":           1,
			"incomplete":           true,
			"failure":              false,
		},
	}); err != nil {
		t.Fatalf("Append generation: %v", err)
	}
	if _, err := durableGoalResumeFence(context.Background(), ledger, run.RunID); err == nil {
		t.Fatal("durableGoalResumeFence accepted a non-canonical generation event")
	}
}

func TestDurableGoalResumeFence_RejectsMalformedGenerationFacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing run ID", mutate: func(payload map[string]any) { delete(payload, "run_id") }},
		{name: "foreign run ID", mutate: func(payload map[string]any) { payload["run_id"] = "run-other" }},
		{name: "missing instance ID", mutate: func(payload map[string]any) { delete(payload, "workflow_instance_id") }},
		{name: "non-string instance ID", mutate: func(payload map[string]any) { payload["workflow_instance_id"] = 7 }},
		{name: "foreign instance ID", mutate: func(payload map[string]any) { payload["workflow_instance_id"] = "goal-run-other" }},
		{name: "missing generation", mutate: func(payload map[string]any) { delete(payload, "generation") }},
		{name: "fractional generation", mutate: func(payload map[string]any) { payload["generation"] = 0.5 }},
		{name: "mismatched generation", mutate: func(payload map[string]any) { payload["generation"] = 1 }},
		{name: "missing incomplete", mutate: func(payload map[string]any) { delete(payload, "incomplete") }},
		{name: "non-boolean incomplete", mutate: func(payload map[string]any) { payload["incomplete"] = "true" }},
		{name: "missing failure", mutate: func(payload map[string]any) { delete(payload, "failure") }},
		{name: "non-boolean failure", mutate: func(payload map[string]any) { payload["failure"] = "false" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ledger := newGoalTestLedger(t)
			run, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-malformed-fact", SessionID: "goal-test"})
			if err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			payload := map[string]any{
				"run_id":               run.RunID,
				"workflow_instance_id": "goal-run-malformed-fact",
				"generation":           0,
				"incomplete":           true,
				"failure":              false,
			}
			tc.mutate(payload)
			if _, err := ledger.Append(context.Background(), runledger.Event{
				RunID:   run.RunID,
				Type:    runledger.EventDurableGoalGeneration,
				Payload: payload,
			}); err != nil {
				t.Fatalf("Append generation: %v", err)
			}
			if _, err := durableGoalResumeFence(context.Background(), ledger, run.RunID); err == nil {
				t.Fatal("durableGoalResumeFence accepted a malformed scheduler fact")
			}
		})
	}
}

func TestDurableGoalResumeFence_RejectsConflictingGenerationFacts(t *testing.T) {
	ledger := newGoalTestLedger(t)
	run, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-conflicting-fact", SessionID: "goal-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for _, incomplete := range []bool{true, false} {
		if _, err := ledger.Append(context.Background(), runledger.Event{
			RunID: run.RunID,
			Type:  runledger.EventDurableGoalGeneration,
			Payload: map[string]any{
				"run_id":               run.RunID,
				"workflow_instance_id": "goal-run-conflicting-fact",
				"generation":           0,
				"incomplete":           incomplete,
				"failure":              false,
			},
		}); err != nil {
			t.Fatalf("Append generation: %v", err)
		}
	}
	if _, err := durableGoalResumeFence(context.Background(), ledger, run.RunID); err == nil || !strings.Contains(err.Error(), "conflicting ledger facts") {
		t.Fatalf("durableGoalResumeFence conflict error = %v", err)
	}
}

func TestDurableGoalIncompleteMessage_IsExplicitlyResumable(t *testing.T) {
	message := durableGoalIncompleteMessage("goal-run-1::resume::2", "run-1", 3)
	if !strings.Contains(message, "bounded generation") || !strings.Contains(message, "3 deferred task(s)") || !strings.Contains(message, "buckley goal run run-1") || !strings.Contains(message, "next durable generation") {
		t.Fatalf("incomplete message = %q", message)
	}
}

type durableGoalTestEngine struct{}

func (durableGoalTestEngine) RunTurn(context.Context, goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	return goalloop.TurnOutcome{}, nil
}

func TestGoalFinalizationForStatus_PreservesTerminalObservation(t *testing.T) {
	status := durability.GoalStatus{
		InstanceID:    "goal-run-1",
		RuntimeStatus: "COMPLETED",
		Result: durability.GoalResult{
			Status:        durability.GoalResultIncomplete,
			DeferredTasks: []string{"task-1"},
		},
	}

	got := goalFinalizationForStatus("run-1", "/work/repo", status.InstanceID, status)
	if got.RunID != "run-1" || got.WorkspaceRoot != "/work/repo" || got.WorkflowInstanceID != status.InstanceID {
		t.Fatalf("finalization identity = %+v", got)
	}
	if !got.Incomplete || got.Failure != "" {
		t.Fatalf("finalization lifecycle = %+v, want resumable incomplete observation", got)
	}
}

func TestGoalFinalizationForStatus_FailureRemainsTerminal(t *testing.T) {
	status := durability.GoalStatus{
		InstanceID:    "goal-run-2",
		RuntimeStatus: "FAILED",
		Failure:       "child workflow failed after fan-in",
	}

	got := goalFinalizationForStatus("run-2", "/work/repo", status.InstanceID, status)
	if got.Incomplete || got.Failure != status.Failure {
		t.Fatalf("finalization lifecycle = %+v, want terminal failure", got)
	}
}

func TestGoalFinalizationForStatus_SynthesizesTerminalRuntimeFailure(t *testing.T) {
	status := durability.GoalStatus{InstanceID: "goal-run-3", RuntimeStatus: "CANCELED"}

	got := goalFinalizationForStatus("run-3", "/work/repo", status.InstanceID, status)
	if got.Failure != "durable workflow ended with status canceled" {
		t.Fatalf("finalization failure = %q", got.Failure)
	}
}

func TestNewDurableGoalRunners_WorkerResolvesConcurrentRun(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	ev, err := evidence.New(filepath.Join(workDir, "durable-goals.db"), evidence.WithBlobRoot(filepath.Join(workDir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	checkpoints, err := taskstate.NewManager(ledger, ev)
	if err != nil {
		t.Fatalf("taskstate.NewManager: %v", err)
	}
	loop, err := goalloop.New(goalloop.Config{
		Ledger:      ledger,
		Checkpoints: checkpoints,
		Engine:      durableGoalTestEngine{},
		SessionID:   "durable-runner-test",
	})
	if err != nil {
		t.Fatalf("goalloop.New: %v", err)
	}
	first, err := loop.Start(ctx, goalloop.Goal{Statement: "first goal", WorkspaceRoot: workDir})
	if err != nil {
		t.Fatalf("start first goal: %v", err)
	}
	second, err := loop.Start(ctx, goalloop.Goal{Statement: "second goal", WorkspaceRoot: workDir})
	if err != nil {
		t.Fatalf("start second goal: %v", err)
	}
	firstSpecs := map[string]goalloop.TaskSpec{}
	for _, task := range first.Tasks {
		firstSpecs[task.TaskID] = task.Spec
	}

	local, worker, err := newDurableGoalRunners(loop, first.RunID, workDir, first.Goal, firstSpecs)
	if err != nil {
		t.Fatalf("newDurableGoalRunners: %v", err)
	}
	if _, err := local.NextBatch(ctx, durability.NextBatchRequest{RunID: second.RunID}); err == nil {
		t.Fatal("one-run finalization runner accepted a concurrent foreign run")
	}
	batch, err := worker.NextBatch(ctx, durability.NextBatchRequest{RunID: second.RunID})
	if err != nil {
		t.Fatalf("resolver worker could not serve concurrent run: %v", err)
	}
	if batch.Done || len(batch.Tasks) != 1 || batch.Tasks[0].TaskID != second.Tasks[0].TaskID {
		t.Fatalf("resolved concurrent batch = %+v", batch)
	}
}
