package config

import (
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/giturl"
	"m31labs.dev/buckley/pkg/personality"
	"m31labs.dev/buckley/pkg/policy"
	"m31labs.dev/buckley/pkg/sandbox"
)

const (
	defaultOpenRouterModel        = defaultOpenRouterChatModel
	defaultOpenRouterChatModel    = "z-ai/glm-5.2"
	defaultOpenRouterUtilityModel = "qwen/qwen3.6-flash"
	defaultOpenRouterCommitModel  = "qwen/qwen3.7-flash"
	defaultOpenRouterBuckbotModel = "qwen/qwen3.7-plus"
	defaultOpenRouterKimiCode     = "moonshotai/kimi-k2.7-code"
	defaultOpenRouterQwenMax      = "qwen/qwen3.7-max"
	legacyOpenRouterChatModel     = "qwen/qwen3.6-max-preview"
	legacyOpenRouterModel         = "moonshotai/kimi-k2.5"
	defaultOpenAIPlanningModel    = "openai/gpt-5.5"
	defaultOpenAIExecutionModel   = "openai/gpt-5.4"
	defaultOpenAIReviewModel      = "openai/gpt-5.5"
	defaultOpenAIUtilityModel     = "openai/gpt-5.4-mini"
	defaultOpenAIReasoning        = "xhigh"
	defaultAnthropicModel         = "anthropic/claude-sonnet-4-5"
	defaultGoogleModel            = "google/gemini-3-pro"
	defaultCodexPlanningModel     = "codex/gpt-5.5"
	defaultCodexExecutionModel    = "codex/gpt-5.4"
	defaultCodexReviewModel       = "codex/gpt-5.5"
	defaultCodexModel             = "codex/gpt-5.4-mini"

	// MinTokenLength is the minimum recommended length for IPC authentication tokens
	MinTokenLength = 32
)

// Default configuration values exported for documentation and validation
const (
	DefaultPlanningModel    = defaultOpenRouterModel
	DefaultExecutionModel   = defaultOpenRouterModel
	DefaultReviewModel      = defaultOpenRouterModel
	DefaultProvider         = "openrouter"
	DefaultExecutionMode    = ExecutionModeClassic // RLM is experimental
	DefaultOneshotMode      = ExecutionModeClassic
	DefaultTrustLevel       = "balanced"
	DefaultApprovalMode     = "safe"
	DefaultSessionBudget    = 10.00
	DefaultDailyBudget      = 20.00
	DefaultMonthlyBudget    = 200.00
	DefaultIPCBind          = "127.0.0.1:4488"
	DefaultCompactThreshold = 0.75
	DefaultMaxSelfHeal      = 3
	DefaultMaxReviewCycles  = 3
	DefaultCodexModel       = defaultCodexModel
)

type providerModelDefaults struct {
	Planning          string
	Execution         string
	Review            string
	UtilityCommit     string
	UtilityPR         string
	UtilityCompaction string
	UtilityTodoPlan   string
}

var providerDefaultModels = map[string]providerModelDefaults{
	"openrouter": {
		Planning:          defaultOpenRouterChatModel,
		Execution:         defaultOpenRouterChatModel,
		Review:            defaultOpenRouterChatModel,
		UtilityCommit:     defaultOpenRouterCommitModel,
		UtilityPR:         defaultOpenRouterUtilityModel,
		UtilityCompaction: defaultOpenRouterUtilityModel,
		UtilityTodoPlan:   defaultOpenRouterUtilityModel,
	},
	"openai": {
		Planning:          defaultOpenAIPlanningModel,
		Execution:         defaultOpenAIExecutionModel,
		Review:            defaultOpenAIReviewModel,
		UtilityCommit:     defaultOpenAIUtilityModel,
		UtilityPR:         defaultOpenAIUtilityModel,
		UtilityCompaction: defaultOpenAIUtilityModel,
		UtilityTodoPlan:   defaultOpenAIUtilityModel,
	},
	"anthropic": providerDefaults(defaultAnthropicModel),
	"google":    providerDefaults(defaultGoogleModel),
	"codex": {
		Planning:          defaultCodexPlanningModel,
		Execution:         defaultCodexExecutionModel,
		Review:            defaultCodexReviewModel,
		UtilityCommit:     defaultCodexModel,
		UtilityPR:         defaultCodexModel,
		UtilityCompaction: defaultCodexModel,
		UtilityTodoPlan:   defaultCodexModel,
	},
}

var providerDefaultReasoning = map[string]string{
	"openai": defaultOpenAIReasoning,
	"codex":  defaultOpenAIReasoning,
}

func providerDefaults(modelID string) providerModelDefaults {
	return providerModelDefaults{
		Planning:          modelID,
		Execution:         modelID,
		Review:            modelID,
		UtilityCommit:     modelID,
		UtilityPR:         modelID,
		UtilityCompaction: modelID,
		UtilityTodoPlan:   modelID,
	}
}

// Config represents the complete Buckley configuration
type Config struct {
	Models         ModelConfig          `yaml:"models"`
	Providers      ProviderConfig       `yaml:"providers"`
	PromptCache    PromptCacheConfig    `yaml:"prompt_cache"`
	Encoding       EncodingConfig       `yaml:"encoding"`
	Personality    PersonalityConfig    `yaml:"personality"`
	Memory         MemoryConfig         `yaml:"memory"`
	Orchestrator   OrchestratorConfig   `yaml:"orchestrator"`
	Execution      ExecutionModeConfig  `yaml:"execution"`
	Oneshot        OneshotModeConfig    `yaml:"oneshot"`
	RLM            RLMConfig            `yaml:"rlm"`
	Approval       ApprovalConfig       `yaml:"approval"`
	Sandbox        SandboxConfig        `yaml:"sandbox"`
	Permissions    PermissionsConfig    `yaml:"permissions"`
	Postures       PosturesConfig       `yaml:"postures"`
	ToolMiddleware ToolMiddlewareConfig `yaml:"tool_middleware"`
	Tools          ToolsConfig          `yaml:"tools"`
	Hooks          PluginHooksConfig    `yaml:"hooks"`
	MCP            MCPConfig            `yaml:"mcp"`
	ACP            ACPConfig            `yaml:"acp"`
	Worktrees      WorktreeConfig       `yaml:"worktrees"`
	Experiment     ExperimentConfig     `yaml:"experiment"`
	Batch          BatchConfig          `yaml:"batch"`
	// GitClone is pkg/giturl.ClonePolicy, outside pkg/config, so its
	// fields can't carry an env struct tag; envGitClone (config_env.go)
	// owns all seven of its env vars instead.
	GitClone       giturl.ClonePolicy `yaml:"git_clone"`
	IPC            IPCConfig          `yaml:"ipc"`
	CostManagement CostConfig         `yaml:"cost_management"`
	RetryPolicy    RetryPolicy        `yaml:"retry_policy"`
	Artifacts      ArtifactsConfig    `yaml:"artifacts"`
	Workflow       WorkflowConfig     `yaml:"workflow"`
	Compaction     CompactionConfig   `yaml:"compaction"`
	UI             UIConfig           `yaml:"ui"`
	WebUI          WebUIConfig        `yaml:"web_ui"`
	Commenting     CommentingConfig   `yaml:"commenting"`
	GitEvents      GitEventsConfig    `yaml:"git_events"`
	Buckbot        BuckbotConfig      `yaml:"buckbot"`
	Input          InputConfig        `yaml:"input"`
	Diagnostics    DiagnosticsConfig  `yaml:"diagnostics"`
	Notify         NotifyConfig       `yaml:"notify"`

	// Context Fabric / durable agent runtime scaffolding. All flags default
	// off or to current (legacy) behavior; no runtime code reads these yet.
	ContextFabric   ContextFabricConfig   `yaml:"context_fabric"`
	AgentController AgentControllerConfig `yaml:"agent_controller"`
	AgentOperations AgentOperationsConfig `yaml:"agent_operations"`
	Metrics         MetricsConfig         `yaml:"metrics"`
}

// NotifyConfig controls async notifications for human-in-the-loop workflows
type NotifyConfig struct {
	Enabled  bool           `yaml:"enabled" env:"BUCKLEY_NOTIFY_ENABLED"`
	Telegram TelegramConfig `yaml:"telegram"`
	Slack    SlackConfig    `yaml:"slack"`
}

// TelegramConfig configures Telegram notifications. BotToken implicitly
// enables Telegram when set (see the envTelegram hook in config_env.go),
// so this whole struct is hook-owned rather than tag-dispatched.
type TelegramConfig struct {
	Enabled  bool   `yaml:"enabled"`   // BUCKLEY_TELEGRAM_ENABLED
	BotToken string `yaml:"bot_token"` // BUCKLEY_TELEGRAM_BOT_TOKEN, from @BotFather
	ChatID   string `yaml:"chat_id"`   // BUCKLEY_TELEGRAM_CHAT_ID, user or group chat ID
}

// SlackConfig configures Slack notifications. WebhookURL implicitly
// enables Slack when set (see the envSlack hook in config_env.go), so
// this whole struct is hook-owned rather than tag-dispatched.
type SlackConfig struct {
	Enabled    bool   `yaml:"enabled"`     // BUCKLEY_SLACK_ENABLED
	WebhookURL string `yaml:"webhook_url"` // BUCKLEY_SLACK_WEBHOOK_URL, incoming webhook URL
	Channel    string `yaml:"channel"`     // BUCKLEY_SLACK_CHANNEL, optional channel override
}

// ModelConfig defines model preferences
type ModelConfig struct {
	Planning        string              `yaml:"planning" env:"BUCKLEY_MODEL_PLANNING"`
	Execution       string              `yaml:"execution" env:"BUCKLEY_MODEL_EXECUTION"`
	Review          string              `yaml:"review" env:"BUCKLEY_MODEL_REVIEW"`
	Curated         []string            `yaml:"curated"`
	VisionFallback  []string            `yaml:"vision_fallback"` // Ordered list of vision models to try
	FallbackChains  map[string][]string `yaml:"fallback_chains"`
	DefaultProvider string              `yaml:"default_provider"` // Default provider (openrouter, openai, anthropic, google, codex)
	// Reasoning level: "off", "minimal", "low", "medium", "high", "xhigh",
	// or "" for auto-detect. BUCKLEY_MODEL_REASONING and its legacy
	// fallback BUCKLEY_REASONING are handled by the envReasoning hook
	// (config_env.go), not the generic env-tag dispatcher, because a
	// fallback var name needs hook logic.
	Reasoning string `yaml:"reasoning"`

	// ProviderContinuation opts into provider-native continuation state
	// (decision 0001) for models/providers that support it. Off by default.
	ProviderContinuation bool `yaml:"provider_continuation"`

	// Utility models for utility tasks.
	Utility UtilityModelConfig `yaml:"utility"`
}

// UtilityModelConfig defines models for utility tasks.
type UtilityModelConfig struct {
	Commit     string `yaml:"commit"`     // Model for generating commit messages
	PR         string `yaml:"pr"`         // Model for generating PR descriptions
	Compaction string `yaml:"compaction"` // Model for conversation compaction/summarization
	TodoPlan   string `yaml:"todo_plan"`  // Model for TODO planning
}

// DefaultUtilityModel is the default model for utility tasks.
const DefaultUtilityModel = defaultOpenRouterUtilityModel

// DefaultCommitModel is the default model for commit message generation.
const DefaultCommitModel = defaultOpenRouterCommitModel

// GetUtilityCommitModel returns the model for commit message generation
func (c *Config) GetUtilityCommitModel() string {
	if c.Models.Utility.Commit != "" {
		return c.Models.Utility.Commit
	}
	return DefaultCommitModel
}

// GetUtilityPRModel returns the model for PR description generation
func (c *Config) GetUtilityPRModel() string {
	if c.Models.Utility.PR != "" {
		return c.Models.Utility.PR
	}
	return DefaultUtilityModel
}

// GetUtilityCompactionModel returns the model for conversation compaction
func (c *Config) GetUtilityCompactionModel() string {
	if c.Models.Utility.Compaction != "" {
		return c.Models.Utility.Compaction
	}
	return DefaultUtilityModel
}

// GetUtilityTodoPlanModel returns the model for TODO planning
func (c *Config) GetUtilityTodoPlanModel() string {
	if c.Models.Utility.TodoPlan != "" {
		return c.Models.Utility.TodoPlan
	}
	return DefaultUtilityModel
}

// ProviderConfig defines provider settings and API keys. Every section
// below is hook-owned in config_env.go (envOpenRouterProvider,
// envOpenAIProvider, envAnthropicProvider, envGoogleProvider,
// envOllamaProvider, envLiteLLMProvider, envCodexProvider) instead of
// tag-dispatched: ProviderSettings is reused across five providers with
// different env var names and different implicit-enable rules per
// provider, so a struct tag on the shared type can't express it.
type ProviderConfig struct {
	OpenRouter   ProviderSettings  `yaml:"openrouter"`
	OpenAI       ProviderSettings  `yaml:"openai"`
	Anthropic    ProviderSettings  `yaml:"anthropic"`
	Google       ProviderSettings  `yaml:"google"`
	Ollama       ProviderSettings  `yaml:"ollama"`
	LiteLLM      LiteLLMConfig     `yaml:"litellm"`
	Codex        CodexConfig       `yaml:"codex"`
	ModelRouting map[string]string `yaml:"model_routing"` // Maps model prefix to provider
}

// ProviderSettings contains settings for a specific provider
type ProviderSettings struct {
	Enabled bool   `yaml:"enabled"`
	APIKey  string `yaml:"api_key"`  // Can be set here or via env var
	BaseURL string `yaml:"base_url"` // Optional custom base URL
}

// LiteLLMConfig configures the LiteLLM proxy provider.
type LiteLLMConfig struct {
	Enabled   bool                 `yaml:"enabled"`
	BaseURL   string               `yaml:"base_url"`
	APIKey    string               `yaml:"api_key"`
	Models    []string             `yaml:"models"`
	Fallbacks map[string][]string  `yaml:"fallbacks"`
	Router    *LiteLLMRouterConfig `yaml:"router"`
}

// CodexConfig configures Codex CLI as a chat provider.
type CodexConfig struct {
	Enabled bool     `yaml:"enabled"`
	Command string   `yaml:"command"`
	Models  []string `yaml:"models"`
}

// LiteLLMRouterConfig defines routing behavior for LiteLLM proxies.
type LiteLLMRouterConfig struct {
	Strategy       string   `yaml:"strategy"`
	NumRetries     int      `yaml:"num_retries"`
	TimeoutSeconds int      `yaml:"timeout_seconds"`
	FallbackModels []string `yaml:"fallback_models"`
}

// PromptCacheConfig controls provider prompt caching options.
type PromptCacheConfig struct {
	Enabled        bool     `yaml:"enabled"`
	Providers      []string `yaml:"providers"`
	SystemMessages int      `yaml:"system_messages"`
	TailMessages   int      `yaml:"tail_messages"`
	Key            string   `yaml:"key"`
	Retention      string   `yaml:"retention"`
}

// EncodingConfig controls serialization preferences. UseToon has two env
// vars -- BUCKLEY_USE_TOON (symmetric) and BUCKLEY_DISABLE_TOON
// (one-directional: only ever turns it off) -- so it's handled by the
// envUseToon hook (config_env.go), not the generic dispatcher.
type EncodingConfig struct {
	UseToon bool `yaml:"use_toon"`
}

// PersonalityConfig controls personality behavior
type PersonalityConfig struct {
	Enabled          bool                                     `yaml:"enabled"`
	QuirkProbability float64                                  `yaml:"quirk_probability"`
	Tone             string                                   `yaml:"tone"` // professional, friendly, quirky
	Categories       map[string]bool                          `yaml:"categories,omitempty"`
	DefaultPersona   string                                   `yaml:"default_persona"`
	PhaseOverrides   map[string]string                        `yaml:"phase_overrides"`
	Personas         map[string]personality.PersonaDefinition `yaml:"personas"`
}

// MemoryConfig controls conversation memory
type MemoryConfig struct {
	AutoCompactThreshold float64 `yaml:"auto_compact_threshold"`
	MaxCompactions       int     `yaml:"max_compactions"`
	SummaryTimeoutSecs   int     `yaml:"summary_timeout_secs"` // Timeout for compaction summarization (default: 30)
	RetrievalEnabled     bool    `yaml:"retrieval_enabled"`
	RetrievalLimit       int     `yaml:"retrieval_limit"`
	RetrievalMaxTokens   int     `yaml:"retrieval_max_tokens"`

	// M31 memory adapter scaffolding (Context Fabric spec section 27). Every
	// external adapter is optional; a missing binary or service MUST NOT
	// prevent local Buckley operation.
	RalphCompatibility bool `yaml:"ralph_compatibility"`
	HyphaeRecall       bool `yaml:"hyphae_recall"`
	HyphaePromotion    bool `yaml:"hyphae_promotion"`
	TillerInterchange  bool `yaml:"tiller_interchange"`
	GraftVCS           bool `yaml:"graft_vcs"`
}

// OrchestratorConfig controls feature orchestration
type OrchestratorConfig struct {
	MaxSelfHealAttempts int    `yaml:"max_self_heal_attempts"`
	MaxReviewCycles     int    `yaml:"max_review_cycles"`
	TrustLevel          string `yaml:"trust_level" env:"BUCKLEY_TRUST_LEVEL"` // conservative, balanced, autonomous
	AutoWorkflow        bool   `yaml:"auto_workflow"`

	// Planning mode configuration
	Planning PlanningConfig `yaml:"planning"`
}

const (
	ExecutionModeClassic = "classic"
	ExecutionModeRLM     = "rlm"
)

// ExecutionModeConfig controls the default execution strategy.
type ExecutionModeConfig struct {
	Mode string `yaml:"mode" env:"BUCKLEY_EXECUTION_MODE"`
}

// OneshotModeConfig controls the strategy for one-shot commands.
type OneshotModeConfig struct {
	Mode string `yaml:"mode" env:"BUCKLEY_ONESHOT_MODE"`
}

// RLMConfig controls the Recursive Language Model runtime.
type RLMConfig struct {
	Coordinator RLMCoordinatorConfig `yaml:"coordinator"`
	SubAgent    RLMSubAgentConfig    `yaml:"sub_agent"`
	Scratchpad  RLMScratchpadConfig  `yaml:"scratchpad"`
}

// RLMCoordinatorConfig controls coordinator behavior.
type RLMCoordinatorConfig struct {
	Model               string        `yaml:"model"`
	MaxIterations       int           `yaml:"max_iterations"`
	MaxTokensBudget     int           `yaml:"max_tokens_budget"`
	MaxWallTime         time.Duration `yaml:"max_wall_time"`
	ConfidenceThreshold float64       `yaml:"confidence_threshold"`
	StreamPartials      bool          `yaml:"stream_partials"`
}

// RLMSubAgentConfig controls sub-agent behavior.
type RLMSubAgentConfig struct {
	Model         string        `yaml:"model"`          // Model for all sub-agents (default: execution model)
	MaxConcurrent int           `yaml:"max_concurrent"` // Parallel execution limit
	Timeout       time.Duration `yaml:"timeout"`        // Per-task timeout
}

// RLMScratchpadConfig controls scratchpad retention.
type RLMScratchpadConfig struct {
	MaxEntriesMemory  int           `yaml:"max_entries_memory"`
	MaxRawBytesMemory int64         `yaml:"max_raw_bytes_memory"`
	EvictionPolicy    string        `yaml:"eviction_policy"`
	DefaultTTL        time.Duration `yaml:"default_ttl"`
	PersistArtifacts  bool          `yaml:"persist_artifacts"`
	PersistDecisions  bool          `yaml:"persist_decisions"`
}

// IsZero reports whether the RLM config is entirely unset.
func (c RLMConfig) IsZero() bool {
	return c.Coordinator.Model == "" &&
		c.Coordinator.MaxIterations == 0 &&
		c.Coordinator.MaxTokensBudget == 0 &&
		c.Coordinator.MaxWallTime == 0 &&
		c.Coordinator.ConfidenceThreshold == 0 &&
		!c.Coordinator.StreamPartials &&
		c.SubAgent.Model == "" &&
		c.SubAgent.MaxConcurrent == 0 &&
		c.SubAgent.Timeout == 0 &&
		c.Scratchpad.MaxEntriesMemory == 0 &&
		c.Scratchpad.MaxRawBytesMemory == 0 &&
		c.Scratchpad.EvictionPolicy == "" &&
		c.Scratchpad.DefaultTTL == 0 &&
		!c.Scratchpad.PersistArtifacts &&
		!c.Scratchpad.PersistDecisions
}

// PlanningConfig controls intelligent planning behavior
type PlanningConfig struct {
	Enabled             bool    `yaml:"enabled"`              // Enable automatic planning mode detection
	ComplexityThreshold float64 `yaml:"complexity_threshold"` // Score above this triggers planning (default: 0.6)
	PlanningModel       string  `yaml:"planning_model"`       // Model for brainstorming (default: execution model)

	// Long-run mode settings
	LongRunEnabled      bool `yaml:"long_run_enabled"`       // Enable autonomous decision-making
	LongRunMaxMinutes   int  `yaml:"long_run_max_minutes"`   // Auto-pause for check-in (default: 30)
	LongRunLogDecisions bool `yaml:"long_run_log_decisions"` // Persist decision trail
	LongRunPauseOnRisk  bool `yaml:"long_run_pause_on_risk"` // Pause for high-risk operations
}

// InputConfig controls multimodal input processing
type InputConfig struct {
	Transcription TranscriptionConfig `yaml:"transcription"`
	Video         VideoConfig         `yaml:"video"`
}

// DiagnosticsConfig controls diagnostic logging and debugging behavior.
type DiagnosticsConfig struct {
	// NetworkLogsEnabled has two env vars -- BUCKLEY_NETWORK_LOGS_ENABLED
	// (symmetric) and BUCKLEY_DISABLE_NETWORK_LOGS (one-directional: only
	// ever turns it off) -- so it's handled by the envNetworkLogs hook
	// (config_env.go), not the generic dispatcher.
	NetworkLogsEnabled bool `yaml:"network_logs_enabled"`
	// TelemetryPayloadsOverNetwork controls whether full tool call arguments
	// and results are included in telemetry events sent over network
	// transports (IPC WebSocket, ACP). Tool output can contain file
	// contents and other sensitive data that key-name redaction can't
	// protect, so this defaults to false; the in-process TUI always
	// receives full payloads regardless of this setting.
	TelemetryPayloadsOverNetwork bool `yaml:"telemetry_payloads_over_network"`
}

// TranscriptionConfig controls audio-to-text conversion
type TranscriptionConfig struct {
	Provider     string `yaml:"provider"`      // api, system, hybrid (default: api)
	WhisperModel string `yaml:"whisper_model"` // Model for API transcription (default: whisper-1)
	APIEndpoint  string `yaml:"api_endpoint"`  // Custom API endpoint (optional)
	Timeout      int    `yaml:"timeout"`       // Timeout in seconds (default: 60)
}

// VideoConfig controls video processing
type VideoConfig struct {
	Enabled      bool   `yaml:"enabled"`       // Enable video frame extraction
	MaxFrames    int    `yaml:"max_frames"`    // Maximum frames to extract (default: 5)
	ExtractAudio bool   `yaml:"extract_audio"` // Extract and transcribe audio track
	FFmpegPath   string `yaml:"ffmpeg_path"`   // Path to ffmpeg binary (optional)
}

// ApprovalConfig controls agent permission levels and safety boundaries.
type ApprovalConfig struct {
	// Mode determines the default approval level: ask, safe, auto, yolo
	// - ask: Explicit approval for all writes and commands
	// - safe: Read anything, write to workspace only, no shell/network without approval
	// - auto: Full workspace access, approval for external operations
	// - yolo: Full autonomy (dangerous, use with caution)
	Mode string `yaml:"mode" env:"BUCKLEY_APPROVAL_MODE"`

	// TrustedPaths are additional paths with write access (beyond workspace)
	TrustedPaths []string `yaml:"trusted_paths"`

	// DeniedPaths are paths that are never writable (even in yolo mode)
	DeniedPaths []string `yaml:"denied_paths"`

	// AllowNetwork permits network access in auto mode without prompting
	AllowNetwork bool `yaml:"allow_network"`

	// AllowedTools lists tools that can run without approval (in ask mode)
	AllowedTools []string `yaml:"allowed_tools"`

	// DeniedTools lists tools that always require approval (even in yolo mode)
	DeniedTools []string `yaml:"denied_tools"`

	// AutoApprovePatterns are shell command patterns that auto-approve
	AutoApprovePatterns []string `yaml:"auto_approve_patterns"`
}

// SandboxConfig controls command sandboxing for tool execution.
type SandboxConfig struct {
	// Mode sets the sandbox level: disabled, readonly, workspace, strict
	Mode string `yaml:"mode" env:"BUCKLEY_TOOL_SANDBOX_MODE"`

	// AllowUnsafe must be true to allow mode=disabled. BUCKLEY_UNSAFE is
	// handled by the envSandboxAllowUnsafe hook (config_env.go), not the
	// generic dispatcher, because it only ever sets true (an unset or
	// false env var must not clear an operator's explicit yaml opt-in).
	AllowUnsafe bool `yaml:"allow_unsafe"`

	// WorkspacePath is the default working directory for sandbox checks.
	WorkspacePath string `yaml:"workspace_path"`

	// AllowedPaths are additional allowed paths (overrides default when set).
	AllowedPaths []string `yaml:"allowed_paths"`

	// DeniedPaths are paths that are never allowed.
	DeniedPaths []string `yaml:"denied_paths"`

	// AllowedCommands are explicit allowlist entries for strict mode.
	AllowedCommands []string `yaml:"allowed_commands"`

	// DeniedCommands are explicit denylist entries.
	DeniedCommands []string `yaml:"denied_commands"`

	// AllowNetwork permits network access when true.
	AllowNetwork bool `yaml:"allow_network" env:"BUCKLEY_TOOL_SANDBOX_ALLOW_NETWORK"`

	// Timeout caps command runtime (0 = no timeout).
	Timeout time.Duration `yaml:"timeout" env:"BUCKLEY_TOOL_SANDBOX_TIMEOUT"`

	// MaxOutputBytes caps command output (0 = unlimited).
	MaxOutputBytes int64 `yaml:"max_output_bytes" env:"BUCKLEY_TOOL_SANDBOX_MAX_OUTPUT_BYTES"`

	// DockerSandbox configures OS-level Docker container isolation.
	DockerSandbox DockerSandboxConfig `yaml:"docker"`
}

// DockerSandboxConfig controls Docker-based OS-level sandboxing for tool execution.
type DockerSandboxConfig struct {
	Enabled        bool   `yaml:"enabled" env:"BUCKLEY_DOCKER_SANDBOX_ENABLED"`
	Image          string `yaml:"image" env:"BUCKLEY_DOCKER_SANDBOX_IMAGE"`
	WorkspaceMount string `yaml:"workspace_mount"`
	ReadOnlyRoot   bool   `yaml:"read_only_root"`
	// NetworkEnabled (BUCKLEY_DOCKER_SANDBOX_NETWORK) is a *bool so a
	// project config can distinguish "not set" from "set to false"; the
	// generic dispatcher only supports value types, so this is handled by
	// the envDockerNetwork hook (config_env.go).
	NetworkEnabled   *bool                `yaml:"network_enabled,omitempty"`
	Resources        ResourceLimitsConfig `yaml:"resources"`
	Security         SecurityConfig       `yaml:"security"`
	KeepAlive        bool                 `yaml:"keep_alive"`
	KeepAliveTimeout time.Duration        `yaml:"keep_alive_timeout"`
}

// ResourceLimitsConfig defines container resource constraints.
type ResourceLimitsConfig struct {
	CPUs      string `yaml:"cpus"`
	Memory    string `yaml:"memory"`
	PidsLimit int    `yaml:"pids_limit"`
	TmpfsSize string `yaml:"tmpfs_size"`
}

// SecurityConfig defines container security settings.
type SecurityConfig struct {
	NoNewPrivileges  bool     `yaml:"no_new_privileges"`
	DropCapabilities []string `yaml:"drop_capabilities"`
	AddCapabilities  []string `yaml:"add_capabilities"`
	SeccompProfile   string   `yaml:"seccomp_profile"`
	AppArmorProfile  string   `yaml:"apparmor_profile"`
}

// ToSandboxConfig converts the config into a runtime sandbox configuration.
func (c SandboxConfig) ToSandboxConfig(workDir string) sandbox.Config {
	mode, err := parseSandboxMode(c.Mode)
	if err != nil {
		mode = sandbox.ModeWorkspace
	}

	cfg := sandbox.Config{
		Mode:            mode,
		WorkspacePath:   strings.TrimSpace(c.WorkspacePath),
		AllowedPaths:    append([]string{}, c.AllowedPaths...),
		DeniedPaths:     append([]string{}, c.DeniedPaths...),
		AllowedCommands: append([]string{}, c.AllowedCommands...),
		DeniedCommands:  append([]string{}, c.DeniedCommands...),
		AllowNetwork:    c.AllowNetwork,
		Timeout:         c.Timeout,
		MaxOutputSize:   c.MaxOutputBytes,
	}

	if cfg.WorkspacePath == "" && strings.TrimSpace(workDir) != "" {
		cfg.WorkspacePath = strings.TrimSpace(workDir)
	}
	if cfg.WorkspacePath != "" {
		if len(cfg.AllowedPaths) == 0 {
			cfg.AllowedPaths = []string{cfg.WorkspacePath}
		} else if !containsString(cfg.AllowedPaths, cfg.WorkspacePath) {
			cfg.AllowedPaths = append(cfg.AllowedPaths, cfg.WorkspacePath)
		}
	}

	return cfg
}

// PermissionsConfig holds glob-granular allow/ask/deny rules for tool
// arguments (ADR 0006, pkg/policy). Project and User layers compose with
// the active posture's layer (see PosturesConfig) and the built-in
// defaults (policy.BuiltinDefaultRules); a deny in any layer wins
// regardless of layer order. Project rules load from ./.buckley/config.yaml
// and User rules from ~/.buckley/config.yaml, matching the existing config
// hierarchy (pkg/config/config_load.go).
type PermissionsConfig struct {
	Project []policy.PermissionRule `yaml:"project"`
	User    []policy.PermissionRule `yaml:"user"`
}

// PostureConfig is a named permission layer selectable via
// BUCKLEY_POSTURE or postures.default. Built-in defaults ship
// "interactive" (empty layer, today's behavior) and "unattended" (flags
// outward-facing bash as "ask" and parks those decisions instead of
// blocking on human approval).
type PostureConfig struct {
	Rules []policy.PermissionRule `yaml:"rules"`
	// ParkAskDecisions routes "ask" decisions to a ParkedDecision instead of
	// blocking on human approval, for postures with nobody present to answer.
	ParkAskDecisions bool `yaml:"park_ask_decisions"`
}

// PosturesConfig configures named posture layers and posture selection.
type PosturesConfig struct {
	// Default selects the active posture when BUCKLEY_POSTURE is unset.
	// BUCKLEY_POSTURE is trimmed before the empty check (unlike the
	// generic dispatcher's untrimmed string fields), so it's handled by
	// the envPosture hook (config_env.go).
	Default string                   `yaml:"default"`
	Layers  map[string]PostureConfig `yaml:"layers"`
}

// WorktreeConfig controls git worktree behavior
type WorktreeConfig struct {
	UseContainers    bool   `yaml:"use_containers"`
	RootPath         string `yaml:"root_path"`
	ContainerService string `yaml:"container_service"`
}

// ExperimentConfig controls experiment execution defaults. MaxConcurrent,
// DefaultTimeout, MaxCostPerRun, and MaxTokensPerRun only apply their env
// var when the parsed value is positive (an explicit 0 or a negative
// value is treated as unset, not as "disable"), so those four are
// handled by the envExperimentPositive* hooks (config_env.go) rather than
// the generic dispatcher.
type ExperimentConfig struct {
	Enabled         bool          `yaml:"enabled" env:"BUCKLEY_EXPERIMENT_ENABLED"`
	MaxConcurrent   int           `yaml:"max_concurrent"`  // BUCKLEY_EXPERIMENT_MAX_CONCURRENT (positive only)
	DefaultTimeout  time.Duration `yaml:"default_timeout"` // BUCKLEY_EXPERIMENT_DEFAULT_TIMEOUT (positive only)
	WorktreeRoot    string        `yaml:"worktree_root" env:"BUCKLEY_EXPERIMENT_WORKTREE_ROOT"`
	CleanupOnDone   bool          `yaml:"cleanup_on_done" env:"BUCKLEY_EXPERIMENT_CLEANUP_ON_DONE"`
	MaxCostPerRun   float64       `yaml:"max_cost_per_run"`   // BUCKLEY_EXPERIMENT_MAX_COST_PER_RUN (positive only)
	MaxTokensPerRun int           `yaml:"max_tokens_per_run"` // BUCKLEY_EXPERIMENT_MAX_TOKENS_PER_RUN (positive only)
}

// ACPConfig controls ACP services and event storage.
type ACPConfig struct {
	EventStore         string     `yaml:"event_store"` // sqlite | nats
	Listen             string     `yaml:"listen"`
	AllowInsecureLocal bool       `yaml:"allow_insecure_local"`
	TLSCertFile        string     `yaml:"tls_cert_file"`
	TLSKeyFile         string     `yaml:"tls_key_file"`
	TLSClientCAFile    string     `yaml:"tls_client_ca_file"`
	NATS               NATSConfig `yaml:"nats"`
}

// NATSConfig contains JetStream connection settings.
type NATSConfig struct {
	URL            string        `yaml:"url"`
	Username       string        `yaml:"username"`
	Password       string        `yaml:"password"`
	Token          string        `yaml:"token"`
	TLS            bool          `yaml:"tls"`
	StreamPrefix   string        `yaml:"stream_prefix"`
	SnapshotBucket string        `yaml:"snapshot_bucket"`
	ConnectTimeout time.Duration `yaml:"connect_timeout"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
}

// IPCConfig controls Buckley's HTTP/WebSocket server. BasicAuthEnabled is
// also implicitly set to true, after every env var below applies, when
// BasicAuthUsername and BasicAuthPassword are both non-empty and it isn't
// already true -- regardless of whether those two values came from env
// vars or from a config.yaml file. See applyEnvOverrides's post-check
// (config_env.go); that one rule isn't tied to a single env var, so it
// isn't a per-field hook.
type IPCConfig struct {
	Enabled           bool     `yaml:"enabled"`
	Bind              string   `yaml:"bind"`
	EnableBrowser     bool     `yaml:"enable_browser"`
	AllowedOrigins    []string `yaml:"allowed_origins"`
	PublicMetrics     bool     `yaml:"public_metrics" env:"BUCKLEY_PUBLIC_METRICS"`
	RequireToken      bool     `yaml:"require_token"`
	BasicAuthEnabled  bool     `yaml:"basic_auth_enabled" env:"BUCKLEY_BASIC_AUTH_ENABLED"`
	BasicAuthUsername string   `yaml:"basic_auth_username" env:"BUCKLEY_BASIC_AUTH_USER"`
	BasicAuthPassword string   `yaml:"basic_auth_password" env:"BUCKLEY_BASIC_AUTH_PASSWORD"`
	PushSubject       string   `yaml:"push_subject" env:"BUCKLEY_PUSH_SUBJECT"` // mailto: or https: URL for VAPID (e.g., mailto:admin@example.com)
}

// CostConfig defines budget limits
type CostConfig struct {
	SessionBudget float64 `yaml:"session_budget"`
	DailyBudget   float64 `yaml:"daily_budget"`
	MonthlyBudget float64 `yaml:"monthly_budget"`
	AutoStopAt    float64 `yaml:"auto_stop_at"`
}

// RetryPolicy defines retry behavior for transient errors
type RetryPolicy struct {
	MaxRetries     int           `yaml:"max_retries"`
	InitialBackoff time.Duration `yaml:"initial_backoff"`
	MaxBackoff     time.Duration `yaml:"max_backoff"`
	Multiplier     float64       `yaml:"multiplier"`
}

// ToolRetryConfig defines retry behavior for tool execution.
type ToolRetryConfig struct {
	MaxAttempts  int           `yaml:"max_attempts"`
	InitialDelay time.Duration `yaml:"initial_delay"`
	MaxDelay     time.Duration `yaml:"max_delay"`
	Multiplier   float64       `yaml:"multiplier"`
	Jitter       float64       `yaml:"jitter"`
}

// ToolMiddlewareConfig defines middleware defaults for tool execution.
type ToolMiddlewareConfig struct {
	DefaultTimeout  time.Duration            `yaml:"default_timeout"`
	PerToolTimeouts map[string]time.Duration `yaml:"per_tool_timeouts"`
	MaxResultBytes  int                      `yaml:"max_result_bytes"`
	Retry           ToolRetryConfig          `yaml:"retry"`
}

// ToolsConfig defines defaults for the tool pool exposed to models.
type ToolsConfig struct {
	// DefaultPoolMode is the tool pool mode used when no policy evaluator
	// resolves one (evaluator is nil or its lookup fails). Valid values:
	// "full" (all tools, default), "standard", "read_only", "simple".
	DefaultPoolMode string `yaml:"default_pool_mode"`
}

// PluginHooksConfig gates the plugin hook contract (pkg/tool/external's
// hook_process.go and hook_runner.go): a long-lived "hook mode" process,
// spawned per session for any discovered plugin whose manifest declares a
// hooks: section, that receives telemetry events and can veto tool calls.
//
// This section is a global switch layered on top of each plugin's own
// opt-in: even when Enabled is true, a plugin only receives events or
// pre-tool veto requests if its own manifest's hooks: section asks for
// them (see external.ToolManifest.HasHooks).
type PluginHooksConfig struct {
	// Enabled gates whether Buckley spawns any plugin hook process at
	// all. Defaults to false: hook processes are extra long-lived
	// subprocesses with their own event/veto surface, so -- like MCP --
	// a project or user must opt in explicitly.
	Enabled bool `yaml:"enabled"`

	// DefaultTimeoutMs is used for a plugin's pre-tool veto requests when
	// its own manifest's hooks.pre_tool.timeout_ms is unset. Zero uses
	// external.DefaultPreToolTimeoutMs (3000ms); kept as a plain int here
	// rather than importing pkg/tool/external, matching this package's
	// existing dependency direction (config is cross-cutting and doesn't
	// import tool subpackages).
	DefaultTimeoutMs int `yaml:"default_timeout_ms"`
}

// Validate checks that hooks.default_timeout_ms, when set, is
// non-negative.
func (c PluginHooksConfig) Validate() error {
	if c.DefaultTimeoutMs < 0 {
		return fmt.Errorf("hooks.default_timeout_ms must be >= 0")
	}
	return nil
}

// MCPConfig defines MCP client settings: which external stdio tool servers
// Buckley connects to, and the schema budget applied when bridging their
// tools into the tool registry (see pkg/tool/mcp_tools.go).
type MCPConfig struct {
	Enabled bool              `yaml:"enabled"`
	Servers []MCPServerConfig `yaml:"servers"`
	// MaxTools caps how many tools a single server may contribute to the
	// registry. Servers offering more than MaxTools tools have the excess
	// dropped, with a log message naming the server and the drop count.
	// Zero uses DefaultMCPMaxTools.
	MaxTools int `yaml:"max_tools"`
}

// DefaultMCPMaxTools is the default per-server tool budget (see
// MCPConfig.MaxTools).
const DefaultMCPMaxTools = 20

// MaxToolsOrDefault returns MaxTools if set, otherwise DefaultMCPMaxTools.
func (c MCPConfig) MaxToolsOrDefault() int {
	if c.MaxTools > 0 {
		return c.MaxTools
	}
	return DefaultMCPMaxTools
}

// MCPServerConfig describes a single MCP stdio server. Command must be an
// absolute path or resolvable via PATH (see Validate). Env values support
// ${VAR} expansion against the ambient process environment.
type MCPServerConfig struct {
	Name    string            `yaml:"name"`
	Command string            `yaml:"command"`
	Args    []string          `yaml:"args"`
	Env     map[string]string `yaml:"env"`
	Timeout time.Duration     `yaml:"timeout"`
	// Enabled gates whether Buckley connects to this server. Defaults to
	// false (zero value) so a server config must opt in explicitly, matching
	// the conservative posture toward external, untrusted tool servers.
	Enabled bool `yaml:"enabled"`
}

// ArtifactsConfig defines artifact storage locations
type ArtifactsConfig struct {
	PlanningDir          string `yaml:"planning_dir"`
	ExecutionDir         string `yaml:"execution_dir"`
	ReviewDir            string `yaml:"review_dir"`
	ArchiveDir           string `yaml:"archive_dir"`
	ArchiveByMonth       bool   `yaml:"archive_by_month"`
	AutoArchiveOnPRMerge bool   `yaml:"auto_archive_on_pr_merge"`
}

// WorkflowConfig defines workflow behavior
type WorkflowConfig struct {
	PlanningQuestionsMin              int               `yaml:"planning_questions_min"`
	PlanningQuestionsMax              int               `yaml:"planning_questions_max"`
	IncrementalApproval               bool              `yaml:"incremental_approval"`
	PauseOnBusinessAmbiguity          bool              `yaml:"pause_on_business_ambiguity"`
	PauseOnArchitecturalConflict      bool              `yaml:"pause_on_architectural_conflict"`
	PauseOnComplexityExplosion        bool              `yaml:"pause_on_complexity_explosion"`
	PauseOnEnvironmentMismatch        bool              `yaml:"pause_on_environment_mismatch"`
	ReviewIterationsMax               int               `yaml:"review_iterations_max"`
	AllowNitsInApproval               bool              `yaml:"allow_nits_in_approval"`
	GenerateOpportunisticImprovements bool              `yaml:"generate_opportunistic_improvements"`
	TaskPhaseLoop                     []string          `yaml:"task_phase_loop"`
	TaskPhases                        []TaskPhaseConfig `yaml:"task_phases"`
}

// TaskPhaseConfig describes a task-level phase in the execution loop.
type TaskPhaseConfig struct {
	Stage       string   `yaml:"stage"`       // builder|verify|review
	Name        string   `yaml:"name"`        // Display name
	Description string   `yaml:"description"` // Short description of purpose
	Targets     []string `yaml:"targets"`     // Bulleted focus areas
}

// CompactionConfig defines artifact compaction behavior
type CompactionConfig struct {
	ContextThreshold float64  `yaml:"context_threshold"`
	RLMAutoTrigger   float64  `yaml:"rlm_auto_trigger"`
	CompactionRatio  float64  `yaml:"compaction_ratio"`
	TaskInterval     int      `yaml:"task_interval"`
	TokenThreshold   int      `yaml:"token_threshold"`
	TargetReduction  float64  `yaml:"target_reduction"`
	PreserveCommands bool     `yaml:"preserve_commands"`
	Models           []string `yaml:"models"`
}

// UIAudioConfig defines audio settings for the TUI.
type UIAudioConfig struct {
	Enabled      bool   `yaml:"enabled"`
	AssetsPath   string `yaml:"assets_path"`
	MasterVolume int    `yaml:"master_volume"`
	SFXVolume    int    `yaml:"sfx_volume"`
	MusicVolume  int    `yaml:"music_volume"`
	Muted        bool   `yaml:"muted"`
}

// UIConfig defines UI behavior
type UIConfig struct {
	ActivityPanelDefault      string `yaml:"activity_panel_default"` // "collapsed" or "expanded"
	DiffViewerDefault         string `yaml:"diff_viewer_default"`    // "collapsed" or "expanded"
	ToolGroupingWindowSeconds int    `yaml:"tool_grouping_window_seconds"`
	ShowToolCosts             bool   `yaml:"show_tool_costs"`
	ShowIntentStatements      bool   `yaml:"show_intent_statements"`
	// Sidebar settings
	SidebarWidth    int `yaml:"sidebar_width"`     // Sidebar width in characters (16-60, default 24)
	SidebarMinWidth int `yaml:"sidebar_min_width"` // Minimum sidebar width (default 16)
	SidebarMaxWidth int `yaml:"sidebar_max_width"` // Maximum sidebar width (default 60)
	// Accessibility settings
	HighContrast    bool          `yaml:"high_contrast"`    // Use high-contrast color scheme
	UseTextLabels   bool          `yaml:"use_text_labels"`  // Add text labels to color-only indicators
	ReduceAnimation bool          `yaml:"reduce_animation"` // Reduce or disable animations
	MessageMetadata string        `yaml:"message_metadata"` // "always", "hover", or "never"
	Audio           UIAudioConfig `yaml:"audio"`
}

// WebUIConfig defines web UI integration settings.
type WebUIConfig struct {
	BaseURL string `yaml:"base_url" env:"BUCKLEY_WEB_URL"`
}

// CommentingConfig defines code commenting requirements
type CommentingConfig struct {
	RequireFunctionDocs           bool `yaml:"require_function_docs"`
	RequireBlockCommentsOverLines int  `yaml:"require_block_comments_over_lines"`
	CommentNonObviousOnly         bool `yaml:"comment_non_obvious_only"`
}

type GitEventsConfig struct {
	Enabled            bool   `yaml:"enabled"`
	Secret             string `yaml:"secret"`
	AutoRegressionPlan bool   `yaml:"auto_regression_plan"`
	WebhookBind        string `yaml:"webhook_bind"`
	RegressionCommand  string `yaml:"regression_command"`
	ReleaseCommand     string `yaml:"release_command"`
	FailureCommand     string `yaml:"failure_command"`
}

// BuckbotConfig controls on-demand pull-request reviews. Enabled, Secret, and
// WebhookBind remain for configuration compatibility with the retired daemon.
type BuckbotConfig struct {
	Enabled                    bool    `yaml:"enabled"`
	Secret                     string  `yaml:"secret"`
	WebhookBind                string  `yaml:"webhook_bind"`
	Model                      string  `yaml:"model"`
	CriticModel                string  `yaml:"critic_model"`
	Reasoning                  string  `yaml:"reasoning"`
	PerReviewBudgetUSD         float64 `yaml:"per_review_budget_usd"`
	MonthlyBudgetUSD           float64 `yaml:"monthly_budget_usd"`
	MaxReviewIterations        int     `yaml:"max_review_iterations"`
	MaxValidationAttempts      int     `yaml:"max_validation_attempts"`
	MaxDiffBytes               int     `yaml:"max_diff_bytes"`
	MaxSupportingContextTokens int     `yaml:"max_supporting_context_tokens"`

	// PostingCoreAssociations lists the GitHub authorAssociation values
	// treated as core maintainer/owner for the posted-review size gate
	// (see pkg/oneshot/commands.PostingGateConfig). Empty uses the default
	// (OWNER, MEMBER, COLLABORATOR).
	PostingCoreAssociations []string `yaml:"posting_core_associations"`

	// PostingAllowlist names authors treated as core regardless of their
	// GitHub association, for maintainers whose fork PRs GitHub reports as
	// CONTRIBUTOR.
	PostingAllowlist []string `yaml:"posting_allowlist"`

	// PostingSizeThresholdBytes is the HighSignalBytes ceiling above which
	// a posted review requires a core author (see
	// pkg/oneshot/commands.PostingGateConfig.HighSignalByteThreshold). 0
	// uses diffsignal.ReviewShardBudget.
	PostingSizeThresholdBytes int `yaml:"posting_size_threshold_bytes"`
}
