package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/sandbox"
)

// Validate checks configuration values for correctness and returns an error for any invalid settings.
func (c *Config) Validate() error {
	// Validate trust level
	validTrustLevels := map[string]bool{
		"conservative": true,
		"balanced":     true,
		"autonomous":   true,
	}
	if !validTrustLevels[c.Orchestrator.TrustLevel] {
		return fmt.Errorf("invalid trust level: %s (must be conservative, balanced, or autonomous)", c.Orchestrator.TrustLevel)
	}

	validModes := map[string]bool{
		"classic": true,
		"rlm":     true,
	}
	if strings.TrimSpace(c.Execution.Mode) != "" && !validModes[strings.ToLower(c.Execution.Mode)] {
		return fmt.Errorf("invalid execution mode: %s (valid: classic, rlm)", c.Execution.Mode)
	}
	if strings.TrimSpace(c.Oneshot.Mode) != "" && !validModes[strings.ToLower(c.Oneshot.Mode)] {
		return fmt.Errorf("invalid oneshot mode: %s (valid: classic, rlm)", c.Oneshot.Mode)
	}

	// Validate approval mode
	validApprovalModes := map[string]bool{
		"ask": true, "explicit": true, "manual": true,
		"safe": true, "readonly": true,
		"auto": true, "automatic": true,
		"yolo": true, "full": true, "dangerous": true,
	}
	if c.Approval.Mode != "" && !validApprovalModes[strings.ToLower(c.Approval.Mode)] {
		return fmt.Errorf("invalid approval mode: %s (valid: ask, safe, auto, yolo)", c.Approval.Mode)
	}
	sandboxMode, err := parseSandboxMode(c.Sandbox.Mode)
	if err != nil {
		return err
	}
	if sandboxMode == sandbox.ModeDisabled && !c.Sandbox.AllowUnsafe {
		return fmt.Errorf("sandbox.mode disabled requires sandbox.allow_unsafe: true")
	}
	if c.Sandbox.Timeout < 0 {
		return fmt.Errorf("sandbox.timeout must be >= 0")
	}
	if c.Sandbox.MaxOutputBytes < 0 {
		return fmt.Errorf("sandbox.max_output_bytes must be >= 0")
	}
	if c.Sandbox.DockerSandbox.Enabled && strings.TrimSpace(c.Sandbox.DockerSandbox.Image) == "" {
		return fmt.Errorf("sandbox.docker.image is required when docker sandbox is enabled")
	}

	if c.ToolMiddleware.DefaultTimeout < 0 {
		return fmt.Errorf("tool_middleware.default_timeout must be >= 0")
	}
	if c.ToolMiddleware.MaxResultBytes < 0 {
		return fmt.Errorf("tool_middleware.max_result_bytes must be >= 0")
	}
	for name, timeout := range c.ToolMiddleware.PerToolTimeouts {
		if timeout < 0 {
			return fmt.Errorf("tool_middleware.per_tool_timeouts.%s must be >= 0", name)
		}
	}
	if c.ToolMiddleware.Retry.MaxAttempts < 0 {
		return fmt.Errorf("tool_middleware.retry.max_attempts must be >= 0")
	}
	if c.ToolMiddleware.Retry.InitialDelay < 0 {
		return fmt.Errorf("tool_middleware.retry.initial_delay must be >= 0")
	}
	if c.ToolMiddleware.Retry.MaxDelay < 0 {
		return fmt.Errorf("tool_middleware.retry.max_delay must be >= 0")
	}
	if c.ToolMiddleware.Retry.Multiplier < 0 {
		return fmt.Errorf("tool_middleware.retry.multiplier must be >= 0")
	}
	if c.ToolMiddleware.Retry.Jitter < 0 {
		return fmt.Errorf("tool_middleware.retry.jitter must be >= 0")
	}
	if c.PromptCache.SystemMessages < 0 {
		return fmt.Errorf("prompt_cache.system_messages must be >= 0")
	}
	if c.PromptCache.TailMessages < 0 {
		return fmt.Errorf("prompt_cache.tail_messages must be >= 0")
	}
	if retention := strings.ToLower(strings.TrimSpace(c.PromptCache.Retention)); retention != "" {
		if retention != "in-memory" && retention != "24h" {
			return fmt.Errorf("prompt_cache.retention must be in-memory or 24h")
		}
	}

	// Validate quirk probability
	if c.Personality.QuirkProbability < 0 || c.Personality.QuirkProbability > 1 {
		return fmt.Errorf("quirk probability must be between 0 and 1, got %f", c.Personality.QuirkProbability)
	}

	// Validate compaction threshold
	if c.Memory.AutoCompactThreshold < 0 || c.Memory.AutoCompactThreshold > 1 {
		return fmt.Errorf("auto compact threshold must be between 0 and 1, got %f", c.Memory.AutoCompactThreshold)
	}
	if c.Compaction.RLMAutoTrigger < 0 || c.Compaction.RLMAutoTrigger > 1 {
		return fmt.Errorf("rlm auto trigger must be between 0 and 1, got %f", c.Compaction.RLMAutoTrigger)
	}
	if c.Compaction.CompactionRatio < 0 || c.Compaction.CompactionRatio > 1 {
		return fmt.Errorf("compaction ratio must be between 0 and 1, got %f", c.Compaction.CompactionRatio)
	}

	// Validate batch config
	if c.Batch.Enabled {
		if strings.TrimSpace(c.Batch.JobTemplate.Image) == "" {
			return fmt.Errorf("batch.job_template.image is required when batch execution is enabled")
		}
		if len(c.Batch.JobTemplate.Command) == 0 {
			return fmt.Errorf("batch.job_template.command must include at least one element")
		}
		if len(c.Batch.JobTemplate.Args) == 0 {
			return fmt.Errorf("batch.job_template.args must include at least one element containing placeholders for plan/task IDs")
		}
		if strings.TrimSpace(c.Batch.JobTemplate.WorkspaceMountPath) == "" {
			return fmt.Errorf("batch.job_template.workspace_mount_path cannot be empty")
		}
		if c.Batch.RemoteBranch.Enabled && strings.TrimSpace(c.Batch.RemoteBranch.Prefix) == "" {
			return fmt.Errorf("batch.remote_branch.prefix cannot be empty when remote branches are enabled")
		}
		if c.Batch.RemoteBranch.Enabled && strings.TrimSpace(c.Batch.RemoteBranch.RemoteName) == "" {
			c.Batch.RemoteBranch.RemoteName = "origin"
		}
	}

	if c.IPC.BasicAuthEnabled {
		if strings.TrimSpace(c.IPC.BasicAuthUsername) == "" {
			return fmt.Errorf("ipc.basic_auth_username is required when basic auth is enabled")
		}
		if strings.TrimSpace(c.IPC.BasicAuthPassword) == "" {
			return fmt.Errorf("ipc.basic_auth_password is required when basic auth is enabled")
		}
	}
	if c.IPC.Enabled && strings.TrimSpace(c.IPC.Bind) != "" && !isLoopbackBindAddress(c.IPC.Bind) {
		if !c.IPC.RequireToken && !c.IPC.BasicAuthEnabled {
			return fmt.Errorf("ipc.bind %q is not loopback: enable ipc.require_token or ipc.basic_auth_enabled", c.IPC.Bind)
		}
	}

	// Validate worktree path writability hint
	if c.Worktrees.RootPath != "" && c.Worktrees.UseContainers {
		expanded := expandHomeDir(c.Worktrees.RootPath)
		if !filepath.IsAbs(expanded) {
			return fmt.Errorf("worktrees.root_path should be an absolute path when use_containers is enabled, got: %s", c.Worktrees.RootPath)
		}
	}

	// Validate max compactions
	if c.Memory.MaxCompactions < 0 {
		return fmt.Errorf("max compactions must be >= 0, got %d", c.Memory.MaxCompactions)
	}
	if c.Memory.RetrievalLimit < 0 {
		return fmt.Errorf("retrieval_limit must be >= 0, got %d", c.Memory.RetrievalLimit)
	}
	if c.Memory.RetrievalMaxTokens < 0 {
		return fmt.Errorf("retrieval_max_tokens must be >= 0, got %d", c.Memory.RetrievalMaxTokens)
	}

	return nil
}

// ValidationWarnings returns non-fatal warnings about the configuration.
// These don't prevent operation but indicate potential security or usability issues.
func (c *Config) ValidationWarnings() []string {
	var warnings []string

	// Warn about API keys stored in config (prefer env vars)
	if c.Providers.OpenRouter.APIKey != "" && os.Getenv("OPENROUTER_API_KEY") == "" {
		warnings = append(warnings, "SECURITY: OpenRouter API key is stored in config file. Consider using OPENROUTER_API_KEY environment variable instead.")
	}
	if c.Providers.OpenAI.APIKey != "" && os.Getenv("OPENAI_API_KEY") == "" {
		warnings = append(warnings, "SECURITY: OpenAI API key is stored in config file. Consider using OPENAI_API_KEY environment variable instead.")
	}
	if c.Providers.Anthropic.APIKey != "" && os.Getenv("ANTHROPIC_API_KEY") == "" {
		warnings = append(warnings, "SECURITY: Anthropic API key is stored in config file. Consider using ANTHROPIC_API_KEY environment variable instead.")
	}
	if c.Providers.Google.APIKey != "" && os.Getenv("GOOGLE_API_KEY") == "" {
		warnings = append(warnings, "SECURITY: Google API key is stored in config file. Consider using GOOGLE_API_KEY environment variable instead.")
	}
	if c.Providers.LiteLLM.APIKey != "" && os.Getenv("BUCKLEY_LITELLM_API_KEY") == "" && os.Getenv("LITELLM_API_KEY") == "" {
		warnings = append(warnings, "SECURITY: LiteLLM API key is stored in config file. Consider using BUCKLEY_LITELLM_API_KEY or LITELLM_API_KEY environment variables instead.")
	}

	// Warn about basic auth password in config
	if c.IPC.BasicAuthPassword != "" && os.Getenv("BUCKLEY_BASIC_AUTH_PASSWORD") == "" {
		warnings = append(warnings, "SECURITY: IPC basic auth password is stored in config file. Consider using BUCKLEY_BASIC_AUTH_PASSWORD environment variable instead.")
	}

	// Warn about NATS credentials in config
	if c.ACP.NATS.Password != "" {
		warnings = append(warnings, "SECURITY: NATS password is stored in config file. Consider using environment variables for sensitive credentials.")
	}
	if c.ACP.NATS.Token != "" {
		warnings = append(warnings, "SECURITY: NATS token is stored in config file. Consider using environment variables for sensitive credentials.")
	}

	// Warn about webhook secret in config
	if c.GitEvents.Secret != "" {
		warnings = append(warnings, "SECURITY: Git webhook secret is stored in config file. Consider using environment variables for sensitive credentials.")
	}

	// Warn about Telegram bot token in config
	if c.Notify.Telegram.BotToken != "" && os.Getenv("BUCKLEY_TELEGRAM_BOT_TOKEN") == "" {
		warnings = append(warnings, "SECURITY: Telegram bot token is stored in config file. Consider using BUCKLEY_TELEGRAM_BOT_TOKEN environment variable instead.")
	}

	// Warn about Slack webhook URL in config
	if c.Notify.Slack.WebhookURL != "" && os.Getenv("BUCKLEY_SLACK_WEBHOOK_URL") == "" {
		warnings = append(warnings, "SECURITY: Slack webhook URL is stored in config file. Consider using BUCKLEY_SLACK_WEBHOOK_URL environment variable instead.")
	}

	// Warn about short IPC tokens
	if token := os.Getenv("BUCKLEY_IPC_TOKEN"); c.IPC.RequireToken && token != "" && len(token) < MinTokenLength {
		warnings = append(warnings, fmt.Sprintf("SECURITY: IPC token is shorter than recommended minimum (%d characters). Consider using a longer token for better security.", MinTokenLength))
	}

	// Warn about yolo mode
	if strings.ToLower(c.Approval.Mode) == "yolo" {
		warnings = append(warnings, "WARNING: Approval mode is set to 'yolo'. This grants full autonomy and should only be used in controlled environments.")
	}

	// Warn about network request/response logging
	if c.Diagnostics.NetworkLogsEnabled {
		warnings = append(warnings, "SECURITY: Network request/response logging is enabled. This may capture prompts and code in network.jsonl under BUCKLEY_LOG_DIR (default: .buckley/logs/network.jsonl); disable it when not actively debugging.")
	}

	return warnings
}
