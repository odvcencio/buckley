package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
)

const commitModelHealthTimeout = 1500 * time.Millisecond

type localCommitModelTarget struct {
	baseURL    string
	apiKey     string
	providerID string
	modelID    string
}

func selectAvailableDefaultCommitModelBeforeInit(opts commitCommandOptions) (string, bool) {
	if opts.backend != oneshotBackendAPI || explicitModelID(opts.model, "BUCKLEY_MODEL_COMMIT") != "" {
		return "", false
	}
	cfg, err := loadConfiguredConfig()
	if err != nil {
		return "", false
	}
	selectedModel := cfg.GetUtilityCommitModel()
	providerID := configuredProviderIDForModel(cfg, selectedModel)
	return selectAvailableDefaultCommitModelWithClient(
		opts, cfg, providerID, selectedModel, newCommitModelHealthClient(),
	)
}

func selectAvailableDefaultCommitModel(opts commitCommandOptions, cfg *config.Config, mgr *model.Manager, selectedModel string) (string, bool) {
	providerID := ""
	if mgr != nil {
		providerID = mgr.ProviderIDForModel(selectedModel)
	}
	return selectAvailableDefaultCommitModelWithClient(opts, cfg, providerID, selectedModel, newCommitModelHealthClient())
}

func newCommitModelHealthClient() *http.Client {
	return &http.Client{
		Timeout: commitModelHealthTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func configuredProviderIDForModel(cfg *config.Config, modelID string) string {
	if cfg == nil {
		return ""
	}
	modelID = strings.TrimSpace(modelID)
	providerID := ""
	matchedPrefixLength := 0
	for prefix, candidate := range cfg.Providers.ModelRouting {
		if len(prefix) > matchedPrefixLength && strings.HasPrefix(modelID, prefix) {
			providerID = strings.TrimSpace(candidate)
			matchedPrefixLength = len(prefix)
		}
	}
	if providerID != "" {
		return providerID
	}
	prefix, _, _ := strings.Cut(modelID, "/")
	if prefix == "litellm" || prefix == "openai_compatible" {
		return prefix
	}
	return ""
}

func selectAvailableDefaultCommitModelWithClient(opts commitCommandOptions, cfg *config.Config, providerID, selectedModel string, client *http.Client) (string, bool) {
	if opts.backend != oneshotBackendAPI || explicitModelID(opts.model, "BUCKLEY_MODEL_COMMIT") != "" || cfg == nil {
		return selectedModel, false
	}
	if strings.TrimSpace(selectedModel) != strings.TrimSpace(cfg.GetUtilityCommitModel()) {
		return selectedModel, false
	}

	target, ok := configuredLocalCommitModelTarget(cfg, providerID, selectedModel)
	if !ok {
		return selectedModel, false
	}
	if err := probeLocalCommitModel(client, target); err != nil {
		return configuredOpenRouterCommitFallbackModel(cfg), true
	}
	return selectedModel, false
}

func configuredOpenRouterCommitFallbackModel(cfg *config.Config) string {
	if cfg == nil || !strings.EqualFold(strings.TrimSpace(cfg.Models.DefaultProvider), "openrouter") {
		return config.DefaultCommitModel
	}
	candidate := strings.TrimSpace(cfg.Models.Execution)
	if candidate == "" {
		return config.DefaultCommitModel
	}

	// These prefixes name Buckley-local/direct transports rather than model
	// namespaces accepted by OpenRouter. Vendor namespaces such as openai/,
	// anthropic/, and google/ remain valid OpenRouter model IDs.
	prefix, _, _ := strings.Cut(candidate, "/")
	switch strings.ToLower(prefix) {
	case "litellm", "openai_compatible", "ollama", "codex":
		return config.DefaultCommitModel
	case "openai", "anthropic", "google":
		return candidate
	}
	if providerID := configuredProviderIDForModel(cfg, candidate); providerID != "" && providerID != "openrouter" {
		return config.DefaultCommitModel
	}
	return candidate
}

func configuredLocalCommitModelTarget(cfg *config.Config, providerID, selectedModel string) (localCommitModelTarget, bool) {
	var provider config.OpenAICompatibleConfig
	switch strings.TrimSpace(providerID) {
	case "openai_compatible":
		provider = cfg.Providers.OpenAICompatible
	case "litellm":
		provider = cfg.Providers.LiteLLM
	default:
		return localCommitModelTarget{}, false
	}
	if !provider.Enabled || !isLoopbackHTTPURL(provider.BaseURL) {
		return localCommitModelTarget{}, false
	}

	providerID = strings.TrimSpace(providerID)
	modelID := strings.TrimSpace(selectedModel)
	modelID = strings.TrimPrefix(modelID, providerID+"/")
	return localCommitModelTarget{
		baseURL:    strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/"),
		apiKey:     strings.TrimSpace(provider.APIKey),
		providerID: providerID,
		modelID:    modelID,
	}, true
}

func isLoopbackHTTPURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func probeLocalCommitModel(client *http.Client, target localCommitModelTarget) error {
	if client == nil {
		return fmt.Errorf("health client unavailable")
	}
	req, err := http.NewRequest(http.MethodGet, target.baseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("build models request: %w", err)
	}
	if target.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+target.apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("list models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("list models returned %s", resp.Status)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil {
		return fmt.Errorf("decode models: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("decode models: trailing content")
	}

	for _, advertised := range payload.Data {
		modelID := strings.TrimSpace(advertised.ID)
		modelID = strings.TrimPrefix(modelID, target.providerID+"/")
		if modelID != "" && modelID == target.modelID {
			return nil
		}
	}
	return fmt.Errorf("selected model not advertised")
}
