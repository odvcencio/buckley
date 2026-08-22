package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/rules"
)

var oneshotWorkspaceFn = os.Getwd

// governedOpenRouterClient wraps a completion client so every OpenRouter
// one-shot request runs under the same runtime/model_data_policy gate as
// durable goals. The contract is bound once against the canonical current
// workspace; each request re-matches the bound OSS license evidence,
// re-evaluates the embedded Arbiter strategy, and blocks fail-closed before
// injecting exactly the governed provider fields.
type governedOpenRouterClient struct {
	client   model.CompletionClient
	engine   *rules.Engine
	contract goalloop.GoalModelRequest
	root     string
	// bindErr is non-nil when construction-time binding failed; every
	// request then fails closed without reaching the provider.
	bindErr error
}

// oneshotClientForProvider wraps the completion client for OpenRouter-backed
// one-shot dispatch with the Arbiter-backed model-data policy. Non-OpenRouter
// providers are returned unchanged.
func oneshotClientForProvider(client model.CompletionClient, modelID, providerID string) (model.CompletionClient, error) {
	if strings.TrimSpace(providerID) != "openrouter" {
		return client, nil
	}
	if client == nil {
		return nil, fmt.Errorf("oneshot model data policy unavailable: no completion client")
	}
	wrapped, err := newGovernedOpenRouterClient(client, modelID)
	if err != nil {
		return nil, err
	}
	return wrapped, nil
}

func newGovernedOpenRouterClient(client model.CompletionClient, modelID string) (*governedOpenRouterClient, error) {
	cwd, err := oneshotWorkspaceFn()
	if err != nil {
		return nil, fmt.Errorf("oneshot model data policy unavailable: %w", err)
	}
	root, rootErr := goalloop.NormalizeWorkspaceRoot(cwd)
	if rootErr != nil {
		return nil, fmt.Errorf("oneshot model data policy blocked: workspace_root_invalid")
	}
	engine, err := rules.NewEngine()
	if err != nil {
		return nil, fmt.Errorf("oneshot model data policy unavailable")
	}
	contract, bindErr := bindOneshotModelPolicy(root, engine, modelID)
	return &governedOpenRouterClient{
		client:   client,
		engine:   engine,
		contract: contract,
		root:     root,
		bindErr:  bindErr,
	}, nil
}

// bindOneshotModelPolicy compiles the one-shot model contract against the
// embedded Arbiter domain at construction time. A recognized root OSS
// license licenses non_zdr retention with data_collection=deny bound to that
// exact evidence; anything else requires strict zdr.
func bindOneshotModelPolicy(workspaceRoot string, engine *rules.Engine, modelID string) (goalloop.GoalModelRequest, error) {
	inspection, err := goalloop.InspectWorkspaceLicense(workspaceRoot)
	if err != nil {
		inspection.Status = goalloop.LicenseStatusUnreadable
	}

	contract := goalloop.GoalModelRequest{
		PolicyVersion: goalloop.GoalModelPolicyVersionV1,
		Model:         modelID,
		RetentionMode: goalloop.GoalRetentionZDR,
		OpenRouterZDR: true,
	}
	digestMatch := true
	if inspection.Status == goalloop.LicenseStatusRecognizedOSS {
		contract.RetentionMode = goalloop.GoalRetentionNonZDR
		contract.OpenRouterZDR = false
		contract.OpenRouterDataCollection = "deny"
		contract.WorkspaceLicense = inspection.Evidence
	}

	decision, evalErr := evaluateGoalModelPolicy(engine, "openrouter", contract, inspection, digestMatch)
	if evalErr != nil {
		return goalloop.GoalModelRequest{}, fmt.Errorf("policy_unavailable")
	}
	contract.Policy = decision.Policy
	contract.PolicyAction = decision.Action
	contract.PolicyReasonCode = decision.ReasonCode
	if decision.Action != "allow" {
		return goalloop.GoalModelRequest{}, fmt.Errorf("model_data_policy_blocked_%s", sanitizeOneshotPolicyCode(decision.ReasonCode))
	}
	if err := contract.Validate(); err != nil {
		return goalloop.GoalModelRequest{}, fmt.Errorf("invalid_policy_contract")
	}
	return contract, nil
}

// ChatCompletion enforces the bound model-data policy immediately before the
// underlying client sees the request.
func (c *governedOpenRouterClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if err := c.enforceOneshotModelPolicy(); err != nil {
		return nil, err
	}
	if err := c.applyProviderFields(&req); err != nil {
		return nil, err
	}
	return c.client.ChatCompletion(ctx, req)
}

func (c *governedOpenRouterClient) enforceOneshotModelPolicy() error {
	if c.bindErr != nil {
		return fmt.Errorf("oneshot model data policy blocked: %s", c.bindErr.Error())
	}
	contract := c.contract
	if err := contract.Validate(); err != nil {
		return fmt.Errorf("oneshot model data policy blocked: invalid_policy_contract")
	}
	retention := contract.EffectiveRetentionMode()
	inspection := goalloop.WorkspaceLicenseInspection{Status: goalloop.LicenseStatusNotRequired}
	digestMatch := true
	if retention != goalloop.GoalRetentionZDR {
		var err error
		inspection, digestMatch, err = goalloop.MatchWorkspaceLicense(c.root, contract.WorkspaceLicense)
		if err != nil {
			inspection.Status = goalloop.LicenseStatusUnreadable
			digestMatch = false
		}
	}
	decision, err := evaluateGoalModelPolicy(c.engine, "openrouter", contract, inspection, digestMatch)
	if err != nil {
		return fmt.Errorf("oneshot model data policy unavailable")
	}
	if decision.Action != "allow" || decision.Policy != contract.Policy || decision.ReasonCode != contract.PolicyReasonCode {
		return fmt.Errorf("oneshot model policy blocked: %s", decision.ReasonCode)
	}
	return nil
}

// applyProviderFields projects exactly the governed provider controls onto
// the outgoing request: exact model routing without provider fallbacks plus
// the retention mode's privacy fields.
func (c *governedOpenRouterClient) applyProviderFields(req *model.ChatRequest) error {
	if req == nil {
		return fmt.Errorf("oneshot model data policy blocked: nil request")
	}
	if strings.TrimSpace(req.Model) != c.contract.Model {
		return fmt.Errorf("oneshot model policy blocked: exact_model_required")
	}
	provider := make(map[string]any, len(req.Provider)+2)
	for key, value := range req.Provider {
		provider[key] = value
	}
	req.Provider = provider
	provider["allow_fallbacks"] = false
	switch c.contract.EffectiveRetentionMode() {
	case goalloop.GoalRetentionZDR:
		delete(provider, "data_collection")
		provider["zdr"] = true
	case goalloop.GoalRetentionNonZDR:
		delete(provider, "zdr")
		provider["data_collection"] = c.contract.OpenRouterDataCollection
	default:
		return fmt.Errorf("oneshot model policy blocked: unsupported_retention_mode")
	}
	return nil
}

// GetContextLength forwards the inner client's context-window capability so
// governed one-shot tool loops manage context exactly as the unwrapped
// manager did.
func (c *governedOpenRouterClient) GetContextLength(modelID string) (int, error) {
	provider, ok := c.client.(model.ContextWindowProvider)
	if !ok {
		return 0, fmt.Errorf("oneshot model client does not expose context windows")
	}
	return provider.GetContextLength(modelID)
}

func sanitizeOneshotPolicyCode(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "policy_unavailable"
	}
	var b []byte
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || r == '_' {
			b = append(b, byte(r))
			continue
		}
		b = append(b, '_')
	}
	out := string(b)
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}
