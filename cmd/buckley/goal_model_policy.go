package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/runledger"
)

type goalModelPolicyDecision struct {
	Action     string
	ReasonCode string
	Policy     string
}

var resolveGoalStartProviderFn = resolveGoalStartProvider
var goalStartWorkspaceFn = os.Getwd
var goalRunWorkspaceFn = os.Getwd

func resolveGoalStartProvider(modelID string) (string, error) {
	var (
		cfg *config.Config
		err error
	)
	if configPath != "" {
		cfg, err = config.LoadFromPath(configPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return "", fmt.Errorf("load goal model policy config: %w", err)
	}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		return "", fmt.Errorf("resolve goal model provider: %w", err)
	}
	route, err := mgr.ResolveModelRoute(modelID)
	if err != nil {
		return "", fmt.Errorf("resolve goal model provider: %w", err)
	}
	if route.SelectedModel != strings.TrimSpace(modelID) {
		return "", fmt.Errorf("resolve goal model provider: exact model route changed")
	}
	return route.ProviderID, nil
}

func bindGoalModelPolicy(workspaceRoot, providerID string, contract goalloop.GoalModelRequest) (goalloop.GoalModelRequest, error) {
	contract.PolicyVersion = goalloop.GoalModelPolicyVersionV1
	if contract.RetentionMode == "" {
		contract.RetentionMode = goalloop.GoalRetentionLegacy
	}
	contract.OpenRouterZDR = contract.RetentionMode == goalloop.GoalRetentionZDR

	inspection := goalloop.WorkspaceLicenseInspection{Status: goalloop.LicenseStatusNotRequired}
	digestMatch := true
	if contract.RetentionMode != goalloop.GoalRetentionZDR {
		var err error
		inspection, err = goalloop.InspectWorkspaceLicense(workspaceRoot)
		if err != nil {
			inspection.Status = goalloop.LicenseStatusUnreadable
		}
		digestMatch = inspection.Status == goalloop.LicenseStatusRecognizedOSS
		if digestMatch {
			contract.WorkspaceLicense = inspection.Evidence
		}
	}

	// Model-data governance is safety policy, not a user-tunable routing rule.
	// Compile it from Buckley's embedded domains only.
	engine, err := rules.NewEngine()
	if err != nil {
		return goalloop.GoalModelRequest{}, fmt.Errorf("goal model policy unavailable")
	}
	decision, err := evaluateGoalModelPolicy(engine, providerID, contract, inspection, digestMatch)
	if err != nil {
		return goalloop.GoalModelRequest{}, fmt.Errorf("goal model policy unavailable")
	}
	contract.Policy = decision.Policy
	contract.PolicyAction = decision.Action
	contract.PolicyReasonCode = decision.ReasonCode
	if decision.Action != "allow" {
		return goalloop.GoalModelRequest{}, fmt.Errorf("goal model policy blocked: %s", decision.ReasonCode)
	}
	if err := contract.Validate(); err != nil {
		return goalloop.GoalModelRequest{}, err
	}
	return contract, nil
}

func evaluateGoalModelPolicy(engine *rules.Engine, providerID string, contract goalloop.GoalModelRequest, inspection goalloop.WorkspaceLicenseInspection, digestMatch bool) (goalModelPolicyDecision, error) {
	if engine == nil {
		return goalModelPolicyDecision{}, fmt.Errorf("model data policy engine is unavailable")
	}
	result, err := engine.EvalStrategy("runtime/model_data_policy", "model_data_policy", map[string]any{
		"provider": map[string]any{
			"name": providerID,
		},
		"privacy": map[string]any{
			"retention_mode":  contract.EffectiveRetentionMode(),
			"zdr_enforceable": providerID == "openrouter",
			"data_collection": contract.OpenRouterDataCollection,
		},
		"workspace": map[string]any{
			"license_status":       inspection.Status,
			"license_id":           inspection.Evidence.ID,
			"license_digest_match": digestMatch,
		},
	})
	if err != nil {
		return goalModelPolicyDecision{}, err
	}
	decision := goalModelPolicyDecision{
		Action:     strings.TrimSpace(stringParam(result.Params, "action")),
		ReasonCode: strings.TrimSpace(stringParam(result.Params, "reason_code")),
		Policy:     strings.TrimSpace(stringParam(result.Params, "policy")),
	}
	if (decision.Action != "allow" && decision.Action != "block") || decision.ReasonCode == "" || decision.Policy == "" {
		return goalModelPolicyDecision{}, fmt.Errorf("model data policy returned an invalid decision")
	}
	return decision, nil
}

func stringParam(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func (e *goalTurnEngine) enforceGoalModelPolicy(ctx context.Context, task goalloop.TaskContext, providerID string) error {
	return enforceGoalModelPolicy(ctx, e.ledger, e.policyEngine, e.workDir, task, providerID)
}

func enforceGoalModelPolicy(ctx context.Context, ledger runledger.Store, policyEngine *rules.Engine, workDir string, task goalloop.TaskContext, providerID string) error {
	contract := task.Goal.ModelRequest
	if err := contract.Validate(); err != nil {
		return fmt.Errorf("goal engine: invalid durable model request: %w", err)
	}
	goalWorkspace := task.Goal.WorkspaceRoot
	if strings.TrimSpace(goalWorkspace) == "" && contract.PolicyVersion == "" {
		// Histories predating the workspace binding migrate against the worker's
		// current canonical root. New v1 contracts never receive this fallback.
		goalWorkspace = workDir
	}
	goalRoot, err := goalloop.NormalizeWorkspaceRoot(goalWorkspace)
	if err != nil {
		return fmt.Errorf("goal model policy blocked: workspace_root_invalid")
	}
	engineRoot, err := goalloop.NormalizeWorkspaceRoot(workDir)
	if err != nil || engineRoot != goalRoot {
		return fmt.Errorf("goal model policy blocked: workspace_root_mismatch")
	}

	inspection := goalloop.WorkspaceLicenseInspection{Status: goalloop.LicenseStatusNotRequired}
	digestMatch := true
	if contract.EffectiveRetentionMode() != goalloop.GoalRetentionZDR {
		inspection, digestMatch, err = goalloop.MatchWorkspaceLicense(goalRoot, contract.WorkspaceLicense)
		if err != nil {
			inspection.Status = goalloop.LicenseStatusUnreadable
			digestMatch = false
		}
	}
	decision, err := evaluateGoalModelPolicy(policyEngine, providerID, contract, inspection, digestMatch)
	if err != nil {
		return fmt.Errorf("goal model policy unavailable")
	}
	payload := map[string]any{
		"schema":          goalloop.GoalModelPolicyVersionV1,
		"kind":            "model_data_policy",
		"turn_id":         task.TurnID,
		"action":          decision.Action,
		"reason_code":     decision.ReasonCode,
		"policy":          decision.Policy,
		"retention_mode":  contract.EffectiveRetentionMode(),
		"license_status":  inspection.Status,
		"license_id":      inspection.Evidence.ID,
		"license_sha256":  inspection.Evidence.SHA256,
		"manifest_sha256": inspection.Evidence.ManifestSHA256,
	}
	eventID := runledger.StableEventID(
		"goal-model-data-policy",
		task.RunID,
		task.TaskID,
		task.TurnID,
		decision.Policy,
		decision.Action,
		decision.ReasonCode,
		inspection.Evidence.ManifestSHA256,
	)
	if _, err := ledger.Append(ctx, runledger.Event{
		ID:         eventID,
		Type:       runledger.EventControllerDecision,
		RunID:      task.RunID,
		TaskID:     task.TaskID,
		ModelID:    contract.Model,
		ProviderID: providerID,
		Payload:    payload,
	}); err != nil {
		return fmt.Errorf("goal model policy audit: %w", err)
	}
	if decision.Action != "allow" {
		return fmt.Errorf("goal model policy blocked: %s", decision.ReasonCode)
	}
	return nil
}

func explicitExternalGoalPolicy(contract goalloop.GoalModelRequest) bool {
	return contract.PolicyVersion != "" || contract.RetentionMode != "" || contract.Model != "" ||
		contract.ReasoningEffort != "" || contract.OpenRouterZDR || contract.OpenRouterDataCollection != "" ||
		!contract.WorkspaceLicense.IsZero()
}

func preflightExternalGoalPolicy(goal goalloop.Goal, workDir string) (*rules.Engine, error) {
	if explicitExternalGoalPolicy(goal.ModelRequest) {
		return nil, fmt.Errorf("goal external backend policy blocked: explicit_model_policy_unenforceable")
	}
	goalWorkspace := goal.WorkspaceRoot
	if strings.TrimSpace(goalWorkspace) == "" {
		goalWorkspace = workDir
	}
	goalRoot, err := goalloop.NormalizeWorkspaceRoot(goalWorkspace)
	if err != nil {
		return nil, fmt.Errorf("goal external backend policy blocked: workspace_root_invalid")
	}
	engineRoot, err := goalloop.NormalizeWorkspaceRoot(workDir)
	if err != nil || engineRoot != goalRoot {
		return nil, fmt.Errorf("goal external backend policy blocked: workspace_root_mismatch")
	}
	inspection, digestMatch, inspectErr := goalloop.MatchWorkspaceLicense(goalRoot, goalloop.WorkspaceLicenseEvidence{})
	if inspectErr != nil {
		inspection.Status = goalloop.LicenseStatusUnreadable
		digestMatch = false
	}
	policyEngine, err := rules.NewEngine()
	if err != nil {
		return nil, fmt.Errorf("goal external backend policy unavailable")
	}
	decision, err := evaluateGoalModelPolicy(policyEngine, "external", goal.ModelRequest, inspection, digestMatch)
	if err != nil {
		return nil, fmt.Errorf("goal external backend policy unavailable")
	}
	if decision.Action != "allow" {
		return nil, fmt.Errorf("goal external backend policy blocked: %s", decision.ReasonCode)
	}
	return policyEngine, nil
}

type externalGoalPolicyEngine struct {
	inner        goalloop.TurnEngine
	ledger       runledger.Store
	policyEngine *rules.Engine
	workDir      string
	providerID   string
}

func (e *externalGoalPolicyEngine) RunTurn(ctx context.Context, task goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	if e == nil || e.inner == nil || e.ledger == nil || e.policyEngine == nil {
		return goalloop.TurnOutcome{}, fmt.Errorf("goal external backend policy unavailable")
	}
	if explicitExternalGoalPolicy(task.Goal.ModelRequest) {
		return goalloop.TurnOutcome{}, fmt.Errorf("goal external backend policy blocked: explicit_model_policy_unenforceable")
	}
	if err := enforceGoalModelPolicy(ctx, e.ledger, e.policyEngine, e.workDir, task, e.providerID); err != nil {
		return goalloop.TurnOutcome{}, err
	}
	return e.inner.RunTurn(ctx, task)
}
