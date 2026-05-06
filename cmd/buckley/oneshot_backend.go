package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/transparency"
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
	cfg, err := config.Load()
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

func newOneshotToolInvoker(backend, modelID string, cfg *config.Config, mgr *model.Manager, pricing transparency.ModelPricing, ledger *transparency.CostLedger) (oneshot.ToolInvoker, error) {
	switch backend {
	case oneshotBackendAPI:
		providerID := "openrouter"
		if mgr != nil {
			if routed := mgr.ProviderIDForModel(modelID); routed != "" {
				providerID = routed
			}
		}
		reasoning := ""
		if cfg != nil && mgr != nil {
			reasoning = model.ResolveReasoningEffort(cfg, mgr, nil, modelID, "execution")
		}
		return oneshot.NewInvoker(oneshot.InvokerConfig{
			Client:          mgr,
			Model:           modelID,
			Provider:        providerID,
			ReasoningEffort: reasoning,
			Pricing:         pricing,
			Ledger:          ledger,
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
	case "low", "medium", "high", "xhigh":
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
