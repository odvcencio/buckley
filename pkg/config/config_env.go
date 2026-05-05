package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// applyEnvOverrides applies environment variable overrides
func applyEnvOverrides(cfg *Config, configEnv map[string]string) {
	// Model selection
	if v := os.Getenv("BUCKLEY_MODEL_PLANNING"); v != "" {
		cfg.Models.Planning = v
	}
	if v := os.Getenv("BUCKLEY_MODEL_EXECUTION"); v != "" {
		cfg.Models.Execution = v
	}
	if v := os.Getenv("BUCKLEY_MODEL_REVIEW"); v != "" {
		cfg.Models.Review = v
	}
	if v := os.Getenv("BUCKLEY_MODEL_REASONING"); v != "" {
		cfg.Models.Reasoning = v
	} else if v := os.Getenv("BUCKLEY_REASONING"); v != "" {
		cfg.Models.Reasoning = v
	}
	if v := os.Getenv("BUCKLEY_TRUST_LEVEL"); v != "" {
		cfg.Orchestrator.TrustLevel = v
	}
	if v := os.Getenv("BUCKLEY_APPROVAL_MODE"); v != "" {
		cfg.Approval.Mode = v
	}
	if v := os.Getenv("BUCKLEY_TOOL_SANDBOX_MODE"); v != "" {
		cfg.Sandbox.Mode = v
	}
	if val, ok := envBool("BUCKLEY_UNSAFE"); ok && val {
		cfg.Sandbox.AllowUnsafe = true
	}
	if val, ok := envBool("BUCKLEY_TOOL_SANDBOX_ALLOW_NETWORK"); ok {
		cfg.Sandbox.AllowNetwork = val
	}
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_TOOL_SANDBOX_TIMEOUT")); v != "" {
		if dur, err := time.ParseDuration(v); err == nil {
			cfg.Sandbox.Timeout = dur
		}
	}
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_TOOL_SANDBOX_MAX_OUTPUT_BYTES")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			cfg.Sandbox.MaxOutputBytes = n
		}
	}
	if val, ok := envBool("BUCKLEY_DOCKER_SANDBOX_ENABLED"); ok {
		cfg.Sandbox.DockerSandbox.Enabled = val
	}
	if v := os.Getenv("BUCKLEY_DOCKER_SANDBOX_IMAGE"); v != "" {
		cfg.Sandbox.DockerSandbox.Image = v
	}
	if val, ok := envBool("BUCKLEY_DOCKER_SANDBOX_NETWORK"); ok {
		cfg.Sandbox.DockerSandbox.NetworkEnabled = &val
	}
	if v := os.Getenv("BUCKLEY_EXECUTION_MODE"); v != "" {
		cfg.Execution.Mode = v
	}
	if v := os.Getenv("BUCKLEY_ONESHOT_MODE"); v != "" {
		cfg.Oneshot.Mode = v
	}

	if val, ok := envBool("BUCKLEY_USE_TOON"); ok {
		cfg.Encoding.UseToon = val
	} else if val, ok := envBool("BUCKLEY_DISABLE_TOON"); ok {
		if val {
			cfg.Encoding.UseToon = false
		}
	}

	if val, ok := envBool("BUCKLEY_NETWORK_LOGS_ENABLED"); ok {
		cfg.Diagnostics.NetworkLogsEnabled = val
	} else if val, ok := envBool("BUCKLEY_DISABLE_NETWORK_LOGS"); ok && val {
		cfg.Diagnostics.NetworkLogsEnabled = false
	}

	// Provider API keys
	if v := os.Getenv("OPENROUTER_API_KEY"); v != "" {
		cfg.Providers.OpenRouter.APIKey = v
	} else if cfg.Providers.OpenRouter.APIKey == "" {
		if v := configEnv["OPENROUTER_API_KEY"]; v != "" {
			cfg.Providers.OpenRouter.APIKey = v
		}
	}
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		cfg.Providers.OpenAI.APIKey = v
		cfg.Providers.OpenAI.Enabled = true
	}
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		cfg.Providers.Anthropic.APIKey = v
		cfg.Providers.Anthropic.Enabled = true
	}
	if v := os.Getenv("GOOGLE_API_KEY"); v != "" {
		cfg.Providers.Google.APIKey = v
		cfg.Providers.Google.Enabled = true
	}

	if v, ok := envBool("BUCKLEY_OLLAMA_ENABLED"); ok {
		cfg.Providers.Ollama.Enabled = v
	}
	if v := os.Getenv("BUCKLEY_OLLAMA_BASE_URL"); v != "" {
		cfg.Providers.Ollama.BaseURL = v
		cfg.Providers.Ollama.Enabled = true
	}

	if v, ok := envBool("BUCKLEY_LITELLM_ENABLED"); ok {
		cfg.Providers.LiteLLM.Enabled = v
	}
	if v := os.Getenv("BUCKLEY_LITELLM_BASE_URL"); v != "" {
		cfg.Providers.LiteLLM.BaseURL = v
		cfg.Providers.LiteLLM.Enabled = true
	} else if v := os.Getenv("LITELLM_BASE_URL"); v != "" && cfg.Providers.LiteLLM.BaseURL == "" {
		cfg.Providers.LiteLLM.BaseURL = v
	}
	if v := os.Getenv("BUCKLEY_LITELLM_API_KEY"); v != "" {
		cfg.Providers.LiteLLM.APIKey = v
		cfg.Providers.LiteLLM.Enabled = true
	} else if v := os.Getenv("LITELLM_API_KEY"); v != "" && cfg.Providers.LiteLLM.APIKey == "" {
		cfg.Providers.LiteLLM.APIKey = v
		cfg.Providers.LiteLLM.Enabled = true
	}

	codexModel := normalizeCodexModel(os.Getenv("BUCKLEY_CODEX_MODEL"))
	if v, ok := envBool("BUCKLEY_CODEX_ENABLED"); ok {
		cfg.Providers.Codex.Enabled = v
	}
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_CODEX_COMMAND")); v != "" {
		cfg.Providers.Codex.Command = v
		cfg.Providers.Codex.Enabled = true
	}
	if codexModel != "" {
		cfg.Providers.Codex.Models = []string{codexModel}
		cfg.Providers.Codex.Enabled = true
		if cfg.Models.Execution == "" || cfg.Models.Execution == defaultOpenRouterModel {
			cfg.Models.Execution = codexModel
		}
		if cfg.Models.Planning == "" || cfg.Models.Planning == defaultOpenRouterModel {
			cfg.Models.Planning = codexModel
		}
		if cfg.Models.Review == "" || cfg.Models.Review == defaultOpenRouterModel {
			cfg.Models.Review = codexModel
		}
	}

	if v, ok := envBool("BUCKLEY_EXPERIMENT_ENABLED"); ok {
		cfg.Experiment.Enabled = v
	}
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_EXPERIMENT_MAX_CONCURRENT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Experiment.MaxConcurrent = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_EXPERIMENT_DEFAULT_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Experiment.DefaultTimeout = d
		}
	}
	if v := os.Getenv("BUCKLEY_EXPERIMENT_WORKTREE_ROOT"); v != "" {
		cfg.Experiment.WorktreeRoot = v
	}
	if v, ok := envBool("BUCKLEY_EXPERIMENT_CLEANUP_ON_DONE"); ok {
		cfg.Experiment.CleanupOnDone = v
	}
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_EXPERIMENT_MAX_COST_PER_RUN")); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			cfg.Experiment.MaxCostPerRun = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_EXPERIMENT_MAX_TOKENS_PER_RUN")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Experiment.MaxTokensPerRun = n
		}
	}

	if v, ok := envBool("BUCKLEY_BASIC_AUTH_ENABLED"); ok {
		cfg.IPC.BasicAuthEnabled = v
	}
	if v := os.Getenv("BUCKLEY_BASIC_AUTH_USER"); v != "" {
		cfg.IPC.BasicAuthUsername = v
	}
	if v := os.Getenv("BUCKLEY_BASIC_AUTH_PASSWORD"); v != "" {
		cfg.IPC.BasicAuthPassword = v
	}
	if v, ok := envBool("BUCKLEY_PUBLIC_METRICS"); ok {
		cfg.IPC.PublicMetrics = v
	}
	if cfg.IPC.BasicAuthUsername != "" && cfg.IPC.BasicAuthPassword != "" && !cfg.IPC.BasicAuthEnabled {
		cfg.IPC.BasicAuthEnabled = true
	}
	if v := os.Getenv("BUCKLEY_PUSH_SUBJECT"); v != "" {
		cfg.IPC.PushSubject = v
	}
	if v := os.Getenv("BUCKLEY_WEB_URL"); v != "" {
		cfg.WebUI.BaseURL = v
	}

	if v, ok := envBool("BUCKLEY_BATCH_ENABLED"); ok {
		cfg.Batch.Enabled = v
	}

	// Notify config
	if v, ok := envBool("BUCKLEY_NOTIFY_ENABLED"); ok {
		cfg.Notify.Enabled = v
	}
	if v, ok := envBool("BUCKLEY_TELEGRAM_ENABLED"); ok {
		cfg.Notify.Telegram.Enabled = v
	}
	if v := os.Getenv("BUCKLEY_TELEGRAM_BOT_TOKEN"); v != "" {
		cfg.Notify.Telegram.BotToken = v
		if !cfg.Notify.Telegram.Enabled {
			cfg.Notify.Telegram.Enabled = true
		}
	}
	if v := os.Getenv("BUCKLEY_TELEGRAM_CHAT_ID"); v != "" {
		cfg.Notify.Telegram.ChatID = v
	}
	if v, ok := envBool("BUCKLEY_SLACK_ENABLED"); ok {
		cfg.Notify.Slack.Enabled = v
	}
	if v := os.Getenv("BUCKLEY_SLACK_WEBHOOK_URL"); v != "" {
		cfg.Notify.Slack.WebhookURL = v
		if !cfg.Notify.Slack.Enabled {
			cfg.Notify.Slack.Enabled = true
		}
	}
	if v := os.Getenv("BUCKLEY_SLACK_CHANNEL"); v != "" {
		cfg.Notify.Slack.Channel = v
	}

	if v := os.Getenv("BUCKLEY_GIT_ALLOWED_SCHEMES"); v != "" {
		cfg.GitClone.AllowedSchemes = splitCommaList(v)
	}
	if v := os.Getenv("BUCKLEY_GIT_ALLOWED_HOSTS"); v != "" {
		cfg.GitClone.AllowedHosts = splitCommaList(v)
	}
	if v := os.Getenv("BUCKLEY_GIT_DENIED_HOSTS"); v != "" {
		cfg.GitClone.DeniedHosts = splitCommaList(v)
	}
	if v, ok := envBool("BUCKLEY_GIT_DENY_PRIVATE_NETWORKS"); ok {
		cfg.GitClone.DenyPrivateNetworks = v
	}
	if v, ok := envBool("BUCKLEY_GIT_RESOLVE_DNS"); ok {
		cfg.GitClone.ResolveDNS = v
	}
	if v, ok := envBool("BUCKLEY_GIT_DENY_SCP_SYNTAX"); ok {
		cfg.GitClone.DenySCPSyntax = v
	}
	if v := strings.TrimSpace(os.Getenv("BUCKLEY_GIT_DNS_TIMEOUT_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.GitClone.DNSResolveTimeoutSec = n
		}
	}
}

func normalizeCodexModel(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return ""
	}
	if strings.HasPrefix(modelID, "codex/") {
		return modelID
	}
	return "codex/" + modelID
}

func splitCommaList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func envBool(key string) (bool, bool) {
	val := os.Getenv(key)
	if val == "" {
		return false, false
	}
	switch strings.ToLower(val) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}
