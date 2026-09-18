package main

import (
	"fmt"
	"os"
	"strings"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/transparency"
)

const (
	oneshotBackendAPI = "api"

	envOneshotBackend = "BUCKLEY_ONESHOT_BACKEND"
	envCommitBackend  = "BUCKLEY_COMMIT_BACKEND"
	envPRBackend      = "BUCKLEY_PR_BACKEND"
	envCodexCommand   = "BUCKLEY_CODEX_COMMAND"
	envClaudeCommand  = "BUCKLEY_CLAUDE_COMMAND"
)

func resolveOneshotBackend(commandName, flagValue string) (string, error) {
	value := strings.TrimSpace(flagValue)
	if value == "" {
		switch commandName {
		case "commit":
			value = strings.TrimSpace(os.Getenv(envCommitBackend))
		case "pr":
			value = strings.TrimSpace(os.Getenv(envPRBackend))
		}
	}
	if value == "" {
		value = strings.TrimSpace(os.Getenv(envOneshotBackend))
	}
	if value == "" {
		value = oneshotBackendAPI
	}

	value = strings.ToLower(value)
	switch value {
	case oneshotBackendAPI, oneshot.CLIBackendCodex, oneshot.CLIBackendClaude:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported backend %q (use api, codex, or claude)", value)
	}
}

func initOneshotDependencies(backend string) (*config.Config, *model.Manager, *storage.Store, error) {
	if backend == oneshotBackendAPI {
		return initDependenciesFn()
	}

	ensureBuckleyRuntimeIgnored()
	cfg, err := loadConfiguredConfig()
	if err != nil {
		return nil, nil, nil, withExitCode(fmt.Errorf("failed to load config: %w", err), 2)
	}
	if encodingOverrideFlag != "" {
		cfg.Encoding.UseToon = encodingOverrideFlag != "json"
	}
	tool.SetResultEncoding(cfg.Encoding.UseToon)

	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, nil, err
	}
	if _, _, err := ensureProjectTrust(cfg, cwd); err != nil {
		return nil, nil, nil, fmt.Errorf("applying project trust: %w", err)
	}

	return cfg, nil, nil, nil
}

func resolveCommitModelID(flagValue string, cfg *config.Config, backend string) string {
	if modelID := explicitModelID(flagValue, "BUCKLEY_MODEL_COMMIT"); modelID != "" {
		return normalizeCLIModelID(normalizeModelIDWithReasoning(cfg, modelID), backend)
	}
	if backend != oneshotBackendAPI {
		return defaultCLIModelID(cfg, backend, "commit")
	}
	if cfg != nil {
		return cfg.GetUtilityCommitModel()
	}
	return ""
}

func resolvePRModelID(flagValue string, cfg *config.Config, backend string) string {
	if modelID := explicitModelID(flagValue, "BUCKLEY_MODEL_PR"); modelID != "" {
		return normalizeCLIModelID(normalizeModelIDWithReasoning(cfg, modelID), backend)
	}
	if backend != oneshotBackendAPI {
		return defaultCLIModelID(cfg, backend, "pr")
	}
	if cfg != nil {
		return cfg.GetUtilityPRModel()
	}
	return ""
}

func explicitModelID(flagValue, envName string) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(envName))
}

func newOneshotToolInvoker(backend, commandName, modelID string, cfg *config.Config, mgr *model.Manager, ledger *transparency.CostLedger) (oneshot.ToolInvoker, error) {
	switch backend {
	case oneshotBackendAPI:
		if mgr == nil {
			return nil, fmt.Errorf("oneshot API backend requires model manager")
		}
		route, err := mgr.ResolveModelRoute(modelID)
		if err != nil {
			return nil, fmt.Errorf("resolve oneshot model route: %w", err)
		}
		providerID := route.ProviderID
		pricing, pricingUnknown := resolveOneshotToolPricing(route, mgr)
		requestProfile, diagnostic := resolveOneshotRequestProfileWithDiagnostic(commandName, cfg, routeReasoningChecker{manager: mgr, route: route}, route.SelectedModel)
		if diagnostic != "" {
			fmt.Fprintln(os.Stderr, diagnostic)
		}
		client, err := oneshotClientForProvider(mgr, modelID, providerID, cfg.OneshotDataPolicy())
		if err != nil {
			return nil, err
		}
		return oneshot.NewInvoker(oneshot.InvokerConfig{
			Client:         client,
			Model:          modelID,
			Provider:       providerID,
			Route:          route,
			RequestProfile: requestProfile,
			Pricing:        pricing,
			PricingUnknown: pricingUnknown,
			Ledger:         ledger,
		}), nil
	case oneshot.CLIBackendCodex, oneshot.CLIBackendClaude:
		return oneshot.NewCLIInvoker(oneshot.CLIInvokerConfig{
			Backend:         backend,
			Command:         cliCommandForBackend(backend),
			Model:           modelID,
			ReasoningEffort: cliReasoningEffort(cfg, backend),
		})
	default:
		return nil, fmt.Errorf("unsupported backend %q", backend)
	}
}

func resolveOneshotToolPricing(route model.ModelRoute, mgr *model.Manager) (transparency.ModelPricing, bool) {
	if route.ProviderID == "codex" {
		return transparency.ModelPricing{}, false
	}
	if mgr == nil {
		return transparency.ModelPricing{}, true
	}
	info, err := mgr.GetModelInfoForRoute(route)
	if err != nil || info == nil || !info.PricingKnown {
		return transparency.ModelPricing{}, true
	}
	return transparency.ModelPricing{
		InputPerMillion:  info.Pricing.Prompt,
		OutputPerMillion: info.Pricing.Completion,
	}, false
}

func resolveOneshotRequestProfile(commandName string, cfg *config.Config, checker model.ReasoningChecker, modelID string) oneshot.RequestProfile {
	profile, _ := resolveOneshotRequestProfileWithDiagnostic(commandName, cfg, checker, modelID)
	return profile
}

func resolveOneshotRequestProfileWithDiagnostic(commandName string, cfg *config.Config, checker model.ReasoningChecker, modelID string) (oneshot.RequestProfile, string) {
	commandName = strings.ToLower(strings.TrimSpace(commandName))
	engine, engineErr := rules.NewEngine()
	profile := fallbackOneshotRequestProfile()
	if engineErr == nil {
		if resolved, err := resolveOneshotTransportProfile(engine, commandName); err == nil {
			profile = resolved
		}
	}

	if modelID == "" || checker == nil {
		return profile, ""
	}
	if oneshotReasoningDisabled(cfg) {
		if checker.SupportsReasoning(modelID) {
			profile.Reasoning = &model.ReasoningConfig{Enabled: boolPointer(false)}
		}
		return profile, ""
	}

	var capability model.CapabilityResolution
	if resolver, ok := checker.(interface {
		ResolveReasoningCapability(string) model.CapabilityResolution
	}); ok {
		capability = resolver.ResolveReasoningCapability(modelID)
		if capability.State == model.CapabilityUnknown {
			if effort := counterfactualOneshotReasoningEffort(cfg, engine, modelID, commandName); effort != "" {
				return profile, unknownReasoningDiagnostic(capability, modelID)
			}
			return profile, ""
		}
		if !capability.Supported() {
			return profile, ""
		}
	} else if !checker.SupportsReasoning(modelID) {
		return profile, ""
	}

	if effort := model.ResolveReasoningEffortForTask(cfg, checker, engine, modelID, "execution", commandName); effort != "" {
		profile.Reasoning = &model.ReasoningConfig{Effort: effort}
	}
	return applyOneshotReasoningCompatibility(engine, profile, capability)
}

func fallbackOneshotRequestProfile() oneshot.RequestProfile {
	return oneshot.RequestProfile{
		MaxOutputTokens: 4096,
		RequireTool:     true,
		Temperature:     float64Pointer(0.2),
	}
}

func resolveOneshotTransportProfile(engine *rules.Engine, commandName string) (oneshot.RequestProfile, error) {
	if engine == nil {
		return oneshot.RequestProfile{}, fmt.Errorf("oneshot policy unavailable")
	}
	result, err := engine.EvalStrategy("oneshot", "oneshot_policy", map[string]any{
		"command": commandName,
	})
	if err != nil {
		return oneshot.RequestProfile{}, err
	}
	profile := oneshot.RequestProfile{
		MaxOutputTokens: policyInt(result.Params["max_output_tokens"]),
		RequireTool:     policyBool(result.Params["require_tool"]),
	}
	if temperature := policyFloat(result.Params["temperature"]); temperature > 0 {
		profile.Temperature = float64Pointer(temperature)
	}
	return profile, nil
}

func oneshotReasoningDisabled(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Models.Reasoning)) {
	case "off", "none":
		return true
	default:
		return false
	}
}

type supportedReasoningChecker struct{}

func (supportedReasoningChecker) SupportsReasoning(string) bool { return true }

// routeReasoningChecker projects reasoning metadata from one already-selected
// route. The input model ID is intentionally ignored: aliases must not cause
// request policy to consult a different catalog entry than dispatch will use.
type routeReasoningChecker struct {
	manager *model.Manager
	route   model.ModelRoute
}

func (c routeReasoningChecker) SupportsReasoning(string) bool {
	return c.manager != nil && c.manager.SupportsReasoningForRoute(c.route)
}

func (c routeReasoningChecker) ResolveReasoningCapability(string) model.CapabilityResolution {
	if c.manager == nil {
		return model.CapabilityResolution{}
	}
	return c.manager.ResolveReasoningCapabilityForRoute(c.route)
}

func counterfactualOneshotReasoningEffort(cfg *config.Config, engine *rules.Engine, modelID, commandName string) string {
	return model.ResolveReasoningEffortForTask(cfg, supportedReasoningChecker{}, engine, modelID, "execution", commandName)
}

func unknownReasoningDiagnostic(capability model.CapabilityResolution, requestedModel string) string {
	modelLabel := boundedDiagnosticLabel(capability.Model)
	if modelLabel == "" {
		modelLabel = boundedDiagnosticLabel(requestedModel)
	}
	providerLabel := boundedDiagnosticLabel(capability.ProviderID)
	switch {
	case modelLabel != "" && providerLabel != "":
		return fmt.Sprintf("buckley: reasoning disabled for %s on %s; capability metadata is unavailable", modelLabel, providerLabel)
	case modelLabel != "":
		return fmt.Sprintf("buckley: reasoning disabled for %s; capability metadata is unavailable", modelLabel)
	default:
		return "buckley: reasoning disabled; capability metadata is unavailable"
	}
}

func boundedDiagnosticLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.' || r == '/':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
		if b.Len() >= 80 {
			break
		}
	}
	return b.String()
}

func float64Pointer(value float64) *float64 { return &value }

func boolPointer(value bool) *bool { return &value }

func policyInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func policyFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}

func policyBool(value any) bool {
	v, _ := value.(bool)
	return v
}

func defaultCLIModelID(cfg *config.Config, backend, utility string) string {
	switch backend {
	case oneshot.CLIBackendCodex:
		if cfg != nil {
			var utilityModel string
			switch utility {
			case "pr":
				utilityModel = cfg.GetUtilityPRModel()
			default:
				utilityModel = cfg.GetUtilityCommitModel()
			}
			if strings.HasPrefix(strings.TrimSpace(utilityModel), "codex/") {
				return normalizeCLIModelID(utilityModel, backend)
			}
			if strings.EqualFold(strings.TrimSpace(cfg.Models.DefaultProvider), "codex") && strings.TrimSpace(utilityModel) != "" && !strings.Contains(utilityModel, "/") {
				return normalizeCLIModelID(utilityModel, backend)
			}
			for _, modelID := range cfg.Providers.Codex.Models {
				if modelID = normalizeCLIModelID(modelID, backend); strings.TrimSpace(modelID) != "" && modelID != "default" {
					return modelID
				}
			}
		}
		return normalizeCLIModelID(config.DefaultCodexModel, backend)
	default:
		return ""
	}
}

func cliReasoningEffort(cfg *config.Config, backend string) string {
	if backend != oneshot.CLIBackendCodex {
		return ""
	}
	if cfg == nil {
		return "xhigh"
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Models.Reasoning)) {
	case "off", "none":
		return ""
	case "minimal", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(cfg.Models.Reasoning))
	default:
		return "xhigh"
	}
}

func cliCommandForBackend(backend string) string {
	switch backend {
	case oneshot.CLIBackendCodex:
		if command := strings.TrimSpace(os.Getenv(envCodexCommand)); command != "" {
			return command
		}
	case oneshot.CLIBackendClaude:
		if command := strings.TrimSpace(os.Getenv(envClaudeCommand)); command != "" {
			return command
		}
	}
	return backend
}

func normalizeCLIModelID(modelID, backend string) string {
	if normalized, _ := config.SplitReasoningSuffix(modelID); normalized != "" {
		modelID = normalized
	}
	switch backend {
	case oneshot.CLIBackendCodex:
		modelID = strings.TrimPrefix(modelID, "openai/")
		return strings.TrimPrefix(modelID, "codex/")
	case oneshot.CLIBackendClaude:
		return strings.TrimPrefix(modelID, "anthropic/")
	default:
		return modelID
	}
}

func describeOneshotBackend(backend, modelID string) string {
	if backend == oneshotBackendAPI {
		return fmt.Sprintf("model: %s", modelID)
	}
	if modelID != "" {
		return fmt.Sprintf("backend: %s (%s)", backend, modelID)
	}
	return fmt.Sprintf("backend: %s", backend)
}
