package goalloop

import (
	"context"
	"fmt"
	"time"

	"m31labs.dev/buckley/pkg/runledger"
)

// LoadGoal reconstructs a goal and its task specs from the run's events —
// the inverse of Start, and the entry point for driving a previously
// recorded goal (buckley goal run) or resuming after a restart. It reads
// only task.created payloads, so it works on any machine that has the
// ledger, with no other state.
func (l *Loop) LoadGoal(ctx context.Context, runID string) (Goal, map[string]TaskSpec, error) {
	events, err := l.ledger.ListEvents(ctx, runledger.EventQuery{RunID: runID})
	if err != nil {
		return Goal{}, nil, fmt.Errorf("goalloop: list events: %w", err)
	}

	goal := Goal{}
	specs := map[string]TaskSpec{}
	sawGoal := false
	for _, ev := range events {
		if ev.Type != runledger.EventTaskCreated {
			continue
		}
		switch payloadString(ev.Payload, "kind") {
		case "goal":
			sawGoal = true
			goal.Statement = payloadString(ev.Payload, "statement")
			goal.AcceptanceCriteria = payloadStrings(ev.Payload, "acceptance_criteria")
			goal.Constraints = payloadStrings(ev.Payload, "constraints")
			goal.Posture = payloadString(ev.Payload, "posture")
			goal.ApprovalMode = payloadString(ev.Payload, "approval_mode")
			modelRequest, err := goalModelRequestFromPayload(ev.Payload)
			if err != nil {
				return Goal{}, nil, fmt.Errorf("goalloop: invalid recorded goal model policy: %w", err)
			}
			goal.ModelRequest = modelRequest
			workspaceRoot, workspaceRootPresent, workspaceRootErr := payloadOptionalString(ev.Payload, "workspace_root")
			if modelRequest.PolicyVersion == GoalModelPolicyVersionV1 {
				if workspaceRootErr != nil || !workspaceRootPresent || workspaceRoot == "" {
					return Goal{}, nil, fmt.Errorf("goalloop: invalid recorded goal workspace_root")
				}
				goal.WorkspaceRoot = workspaceRoot
			} else {
				goal.WorkspaceRoot = payloadString(ev.Payload, "workspace_root")
			}
		case "task":
			specs[ev.TaskID] = TaskSpec{
				Title:              payloadString(ev.Payload, "title"),
				Description:        payloadString(ev.Payload, "description"),
				AcceptanceCriteria: payloadStrings(ev.Payload, "acceptance_criteria"),
				Priority:           payloadInt(ev.Payload, "priority"),
				Claims:             payloadStrings(ev.Payload, "claims"),
			}
		}
	}
	if !sawGoal {
		return Goal{}, nil, fmt.Errorf("goalloop: run %s has no goal record", runID)
	}
	if err := goal.Validate(); err != nil {
		return Goal{}, nil, fmt.Errorf("goalloop: invalid recorded goal: %w", err)
	}

	if run, err := l.ledger.GetRun(ctx, runID); err == nil && run.Budget != nil {
		switch v := run.Budget["usd"].(type) {
		case float64:
			goal.BudgetUSD = v
		case int:
			goal.BudgetUSD = float64(v)
		}
		if deadline := payloadString(run.Budget, "deadline"); deadline != "" {
			if t, err := time.Parse(time.RFC3339, deadline); err == nil {
				goal.Deadline = t
			}
		}
	}
	return goal, specs, nil
}

func goalModelRequestFromPayload(payload map[string]any) (GoalModelRequest, error) {
	readString := func(key string) (string, error) {
		value, _, err := payloadOptionalString(payload, key)
		return value, err
	}
	policyVersion, err := readString("model_policy_version")
	if err != nil {
		return GoalModelRequest{}, err
	}
	policy, err := readString("model_policy")
	if err != nil {
		return GoalModelRequest{}, err
	}
	policyAction, err := readString("model_policy_action")
	if err != nil {
		return GoalModelRequest{}, err
	}
	policyReason, err := readString("model_policy_reason_code")
	if err != nil {
		return GoalModelRequest{}, err
	}
	modelID, err := readString("model")
	if err != nil {
		return GoalModelRequest{}, err
	}
	reasoning, err := readString("reasoning_effort")
	if err != nil {
		return GoalModelRequest{}, err
	}
	retention, err := readString("retention_mode")
	if err != nil {
		return GoalModelRequest{}, err
	}
	collection, err := readString("data_collection")
	if err != nil {
		return GoalModelRequest{}, err
	}
	licenseFile, err := readString("workspace_license_file")
	if err != nil {
		return GoalModelRequest{}, err
	}
	licenseID, err := readString("workspace_license_id")
	if err != nil {
		return GoalModelRequest{}, err
	}
	licenseDigest, err := readString("workspace_license_sha256")
	if err != nil {
		return GoalModelRequest{}, err
	}
	licenseManifest, err := readString("workspace_license_manifest_sha256")
	if err != nil {
		return GoalModelRequest{}, err
	}
	zdr, zdrPresent, err := payloadOptionalBool(payload, "openrouter_zdr")
	if err != nil {
		return GoalModelRequest{}, err
	}
	if policyVersion == GoalModelPolicyVersionV1 && !zdrPresent {
		return GoalModelRequest{}, fmt.Errorf("openrouter_zdr is required by %s", GoalModelPolicyVersionV1)
	}
	return GoalModelRequest{
		PolicyVersion:            policyVersion,
		Policy:                   policy,
		PolicyAction:             policyAction,
		PolicyReasonCode:         policyReason,
		Model:                    modelID,
		ReasoningEffort:          reasoning,
		RetentionMode:            retention,
		OpenRouterZDR:            zdr,
		OpenRouterDataCollection: collection,
		WorkspaceLicense: WorkspaceLicenseEvidence{
			File:           licenseFile,
			ID:             licenseID,
			SHA256:         licenseDigest,
			ManifestSHA256: licenseManifest,
		},
	}, nil
}

func payloadOptionalString(payload map[string]any, key string) (string, bool, error) {
	if payload == nil {
		return "", false, nil
	}
	value, exists := payload[key]
	if !exists {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", true, fmt.Errorf("%s must be a string", key)
	}
	return text, true, nil
}

func payloadOptionalBool(payload map[string]any, key string) (bool, bool, error) {
	if payload == nil {
		return false, false, nil
	}
	value, exists := payload[key]
	if !exists {
		return false, false, nil
	}
	boolean, ok := value.(bool)
	if !ok {
		return false, true, fmt.Errorf("%s must be a boolean", key)
	}
	return boolean, true, nil
}

func payloadStrings(payload map[string]any, key string) []string {
	if payload == nil {
		return nil
	}
	switch v := payload[key].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
