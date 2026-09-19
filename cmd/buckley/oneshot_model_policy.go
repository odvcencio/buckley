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

// oneshotDataPolicyMode selects how strictly a one-shot OpenRouter request
// is governed. See Config.OneshotDataPolicy.
type oneshotDataPolicyMode string

const (
	// oneshotDataPolicyNone is the default: the request passes through
	// unmodified, with no provider.zdr/data_collection field and no
	// goal-engine model-data-policy contract enforced.
	//
	// Prior to 2026-09-04 every OpenRouter one-shot request (buckley commit,
	// buckley pr) was unconditionally wrapped in governedOpenRouterClient,
	// which forced provider.zdr=true unless the workspace matched a
	// recognized OSS license. That license check is meaningful for durable
	// goals running against Buckley's own source, but a `buckley commit` can
	// run in any repository, so the forced ZDR baseline routinely broke
	// ordinary commits with OpenRouter's policy-filtered 404 ("No endpoints
	// found matching your data policy (Zero data retention)") or, when the
	// bound contract failed local validation, "invalid_policy_contract".
	// Owner decision: this gate is opt-in for one-shot, not default-on.
	oneshotDataPolicyNone oneshotDataPolicyMode = "none"
	// oneshotDataPolicyZDR opts back into the pre-2026-09-04 default
	// behavior: force zero data retention, still relaxing to non-ZDR
	// data_collection=deny for a recognized OSS-licensed workspace exactly
	// as bindGoalModelPolicy does for durable goals.
	oneshotDataPolicyZDR oneshotDataPolicyMode = "zdr"
	// oneshotDataPolicyDeny opts into requesting non-ZDR retention with
	// provider.data_collection=deny unconditionally, without requiring OSS
	// license evidence.
	oneshotDataPolicyDeny oneshotDataPolicyMode = "deny"
)

// normalizeOneshotDataPolicyMode maps a config/env string to a mode,
// defaulting unknown or empty values to the permissive "none". Config
// validation (validateOneshotDataPolicy) rejects unsupported values before
// they reach here, so the default case is a defensive fallback, not the
// primary validation path.
func normalizeOneshotDataPolicyMode(value string) oneshotDataPolicyMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(oneshotDataPolicyZDR):
		return oneshotDataPolicyZDR
	case string(oneshotDataPolicyDeny):
		return oneshotDataPolicyDeny
	default:
		return oneshotDataPolicyNone
	}
}

// governedOpenRouterClient wraps a completion client so an OpenRouter
// one-shot request opted into "zdr" or "deny" (see oneshotDataPolicyMode)
// runs under the same runtime/model_data_policy gate as durable goals. The
// contract is bound once against the canonical current workspace; each
// request re-matches the bound OSS license evidence, re-evaluates the
// embedded Arbiter strategy, and blocks fail-closed before injecting exactly
// the governed provider fields.
type governedOpenRouterClient struct {
	client   model.CompletionClient
	engine   *rules.Engine
	contract goalloop.GoalModelRequest
	root     string
	// bindErr is non-nil when construction-time binding failed; every
	// request then fails closed without reaching the provider.
	bindErr error
}

// routedOneshotCompletionClient mirrors the private route port used by the
// one-shot invoker. Keeping this local structural interface avoids widening
// model.CompletionClient while allowing the governed wrapper to preserve both
// data-policy enforcement and Manager route-drift protection.
type routedOneshotCompletionClient interface {
	model.CompletionClient
	OfferToolsForRoute(model.ModelRoute) bool
	ToolsCatalogConfirmedUnavailableForRoute(model.ModelRoute) bool
	GetContextLengthForRoute(model.ModelRoute) (int, error)
	ChatCompletionForRoute(context.Context, model.ChatRequest, model.ModelRoute) (*model.ChatResponse, error)
}

type routedOneshotStreamingClient interface {
	routedOneshotCompletionClient
	ChatCompletionStreamForRoute(context.Context, model.ChatRequest, model.ModelRoute) (<-chan model.StreamChunk, <-chan error)
}

// oneshotClientForProvider wraps the completion client for OpenRouter-backed
// one-shot dispatch. With the default data policy ("none"), the client
// passes through unmodified: no provider.zdr/data_collection field is
// attached and the goal-engine model-data-policy contract is not enforced.
// "zdr" or "deny" opt back into the Arbiter-backed model-data policy gate.
// Non-OpenRouter providers are always returned unchanged.
func oneshotClientForProvider(client model.CompletionClient, modelID, providerID, dataPolicy string) (model.CompletionClient, error) {
	if strings.TrimSpace(providerID) != "openrouter" {
		return client, nil
	}
	if client == nil {
		return nil, fmt.Errorf("oneshot model data policy unavailable: no completion client")
	}
	mode := normalizeOneshotDataPolicyMode(dataPolicy)
	if mode == oneshotDataPolicyNone {
		return client, nil
	}
	wrapped, err := newGovernedOpenRouterClient(client, modelID, mode)
	if err != nil {
		return nil, err
	}
	return wrapped, nil
}

func newGovernedOpenRouterClient(client model.CompletionClient, modelID string, mode oneshotDataPolicyMode) (*governedOpenRouterClient, error) {
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
	contract, bindErr := bindOneshotModelPolicy(root, engine, modelID, mode)
	return &governedOpenRouterClient{
		client:   client,
		engine:   engine,
		contract: contract,
		root:     root,
		bindErr:  bindErr,
	}, nil
}

// bindOneshotModelPolicy compiles the one-shot model contract against the
// embedded Arbiter domain at construction time. In "deny" mode the contract
// always requests non_zdr retention with data_collection=deny. In "zdr" mode
// a recognized root OSS license licenses non_zdr retention with
// data_collection=deny bound to that exact evidence; anything else requires
// strict zdr. This function only runs for those two opt-in modes — the
// default "none" mode never constructs a contract (see
// oneshotClientForProvider).
func bindOneshotModelPolicy(workspaceRoot string, engine *rules.Engine, modelID string, mode oneshotDataPolicyMode) (goalloop.GoalModelRequest, error) {
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
	switch mode {
	case oneshotDataPolicyDeny:
		contract.RetentionMode = goalloop.GoalRetentionNonZDR
		contract.OpenRouterZDR = false
		contract.OpenRouterDataCollection = "deny"
		if inspection.Status == goalloop.LicenseStatusRecognizedOSS {
			contract.WorkspaceLicense = inspection.Evidence
		}
	case oneshotDataPolicyZDR:
		if inspection.Status == goalloop.LicenseStatusRecognizedOSS {
			contract.RetentionMode = goalloop.GoalRetentionNonZDR
			contract.OpenRouterZDR = false
			contract.OpenRouterDataCollection = "deny"
			contract.WorkspaceLicense = inspection.Evidence
		}
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

// ChatCompletionForRoute applies the same bound policy and exact provider
// fields before preserving the inner client's route-bound dispatch.
func (c *governedOpenRouterClient) ChatCompletionForRoute(ctx context.Context, req model.ChatRequest, route model.ModelRoute) (*model.ChatResponse, error) {
	if err := c.enforceOneshotModelPolicy(); err != nil {
		return nil, err
	}
	if err := c.applyProviderFields(&req); err != nil {
		return nil, err
	}
	client, ok := c.client.(routedOneshotCompletionClient)
	if !ok {
		return nil, fmt.Errorf("oneshot route contract unavailable: wrapped client requires route-capable model client")
	}
	return client.ChatCompletionForRoute(ctx, req, route)
}

func (c *governedOpenRouterClient) OfferToolsForRoute(route model.ModelRoute) bool {
	client, ok := c.client.(routedOneshotCompletionClient)
	return ok && client.OfferToolsForRoute(route)
}

func (c *governedOpenRouterClient) ToolsCatalogConfirmedUnavailableForRoute(route model.ModelRoute) bool {
	client, ok := c.client.(routedOneshotCompletionClient)
	return ok && client.ToolsCatalogConfirmedUnavailableForRoute(route)
}

func (c *governedOpenRouterClient) GetContextLengthForRoute(route model.ModelRoute) (int, error) {
	client, ok := c.client.(routedOneshotCompletionClient)
	if !ok {
		return 0, fmt.Errorf("oneshot route contract unavailable: wrapped client requires route-capable model client")
	}
	return client.GetContextLengthForRoute(route)
}

func (c *governedOpenRouterClient) ChatCompletionStreamForRoute(ctx context.Context, req model.ChatRequest, route model.ModelRoute) (<-chan model.StreamChunk, <-chan error) {
	if err := c.enforceOneshotModelPolicy(); err != nil {
		return failedOneshotRouteStream(err)
	}
	if err := c.applyProviderFields(&req); err != nil {
		return failedOneshotRouteStream(err)
	}
	client, ok := c.client.(routedOneshotStreamingClient)
	if !ok {
		return failedOneshotRouteStream(fmt.Errorf("oneshot route contract unavailable: wrapped client requires route-capable streaming model client"))
	}
	return client.ChatCompletionStreamForRoute(ctx, req, route)
}

func failedOneshotRouteStream(err error) (<-chan model.StreamChunk, <-chan error) {
	chunks := make(chan model.StreamChunk)
	close(chunks)
	errs := make(chan error, 1)
	errs <- err
	close(errs)
	return chunks, errs
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
