package config

import (
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/giturl"
	"github.com/odvcencio/buckley/pkg/personality"
	"github.com/odvcencio/buckley/pkg/sandbox"
)

const (
	defaultOpenRouterModel      = "moonshotai/kimi-k2.5"
	defaultOpenAIPlanningModel  = "openai/gpt-5.5"
	defaultOpenAIExecutionModel = "openai/gpt-5.4"
	defaultOpenAIReviewModel    = "openai/gpt-5.5"
	defaultOpenAIUtilityModel   = "openai/gpt-5.4-mini"
	defaultOpenAIReasoning      = "xhigh"
	defaultAnthropicModel       = "anthropic/claude-sonnet-4-5"
	defaultGoogleModel          = "google/gemini-3-pro"
	defaultCodexPlanningModel   = "codex/gpt-5.5"
	defaultCodexExecutionModel  = "codex/gpt-5.4"
	defaultCodexReviewModel     = "codex/gpt-5.5"
	defaultCodexModel           = "codex/gpt-5.4-mini"

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
	"openrouter": providerDefaults(defaultOpenRouterModel),
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
	ToolMiddleware ToolMiddlewareConfig `yaml:"tool_middleware"`
	MCP            MCPConfig            `yaml:"mcp"`
	ACP            ACPConfig            `yaml:"acp"`
	Worktrees      WorktreeConfig       `yaml:"worktrees"`
	Experiment     ExperimentConfig     `yaml:"experiment"`
	Batch          BatchConfig          `yaml:"batch"`
	GitClone       giturl.ClonePolicy   `yaml:"git_clone"`
	IPC            IPCConfig            `yaml:"ipc"`
	CostManagement CostConfig           `yaml:"cost_management"`
	RetryPolicy    RetryPolicy          `yaml:"retry_policy"`
	Artifacts      ArtifactsConfig      `yaml:"artifacts"`
	Workflow       WorkflowConfig       `yaml:"workflow"`
	Compaction     CompactionConfig     `yaml:"compaction"`
	UI             UIConfig             `yaml:"ui"`
	WebUI          WebUIConfig          `yaml:"web_ui"`
	Commenting     CommentingConfig     `yaml:"commenting"`
	GitEvents      GitEventsConfig      `yaml:"git_events"`
	Input          InputConfig          `yaml:"input"`
	Diagnostics    DiagnosticsConfig    `yaml:"diagnostics"`
	Notify         NotifyConfig         `yaml:"notify"`
}

// NotifyConfig controls async notifications for human-in-the-loop workflows
type NotifyConfig struct {
	Enabled  bool           `yaml:"enabled"`
	Telegram TelegramConfig `yaml:"telegram"`
	Slack    SlackConfig    `yaml:"slack"`
}

// TelegramConfig configures Telegram notifications
type TelegramConfig struct {
	Enabled  bool   `yaml:"enabled"`
	BotToken string `yaml:"bot_token"` // From @BotFather
	ChatID   string `yaml:"chat_id"`   // User or group chat ID
}

// SlackConfig configures Slack notifications
type SlackConfig struct {
	Enabled    bool   `yaml:"enabled"`
	WebhookURL string `yaml:"webhook_url"` // Incoming webhook URL
	Channel    string `yaml:"channel"`     // Optional channel override
}

// ModelConfig defines model preferences
type ModelConfig struct {
	Planning        string              `yaml:"planning"`
	Execution       string              `yaml:"execution"`
	Review          string              `yaml:"review"`
	Curated         []string            `yaml:"curated"`
	VisionFallback  []string            `yaml:"vision_fallback"` // Ordered list of vision models to try
	FallbackChains  map[string][]string `yaml:"fallback_chains"`
	DefaultProvider string              `yaml:"default_provider"` // Default provider (openrouter, openai, anthropic, google, codex)
	Reasoning       string              `yaml:"reasoning"`        // Reasoning level: "off", "low", "medium", "high", "xhigh", or "" for auto-detect

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
const DefaultUtilityModel = defaultOpenRouterModel

// GetUtilityCommitModel returns the model for commit message generation
func (c *Config) GetUtilityCommitModel() string {
	if c.Models.Utility.Commit != "" {
		return c.Models.Utility.Commit
	}
	return DefaultUtilityModel
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

// ProviderConfig defines provider settings and API keys
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

// EncodingConfig controls serialization preferences.
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
}

// OrchestratorConfig controls feature orchestration
type OrchestratorConfig struct {
	MaxSelfHealAttempts int    `yaml:"max_self_heal_attempts"`
	MaxReviewCycles     int    `yaml:"max_review_cycles"`
	TrustLevel          string `yaml:"trust_level"` // conservative, balanced, autonomous
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
	Mode string `yaml:"mode"`
}

// OneshotModeConfig controls the strategy for one-shot commands.
type OneshotModeConfig struct {
	Mode string `yaml:"mode"`
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
	NetworkLogsEnabled bool `yaml:"network_logs_enabled"`
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
	Mode string `yaml:"mode"`

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
	Mode string `yaml:"mode"`

	// AllowUnsafe must be true to allow mode=disabled.
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
	AllowNetwork bool `yaml:"allow_network"`

	// Timeout caps command runtime (0 = no timeout).
	Timeout time.Duration `yaml:"timeout"`

	// MaxOutputBytes caps command output (0 = unlimited).
	MaxOutputBytes int64 `yaml:"max_output_bytes"`

	// DockerSandbox configures OS-level Docker container isolation.
	DockerSandbox DockerSandboxConfig `yaml:"docker"`
}

// DockerSandboxConfig controls Docker-based OS-level sandboxing for tool execution.
type DockerSandboxConfig struct {
	Enabled          bool                 `yaml:"enabled"`
	Image            string               `yaml:"image"`
	WorkspaceMount   string               `yaml:"workspace_mount"`
	ReadOnlyRoot     bool                 `yaml:"read_only_root"`
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

// WorktreeConfig controls git worktree behavior
type WorktreeConfig struct {
	UseContainers    bool   `yaml:"use_containers"`
	RootPath         string `yaml:"root_path"`
	ContainerService string `yaml:"container_service"`
}

// ExperimentConfig controls experiment execution defaults.
type ExperimentConfig struct {
	Enabled         bool          `yaml:"enabled"`
	MaxConcurrent   int           `yaml:"max_concurrent"`
	DefaultTimeout  time.Duration `yaml:"default_timeout"`
	WorktreeRoot    string        `yaml:"worktree_root"`
	CleanupOnDone   bool          `yaml:"cleanup_on_done"`
	MaxCostPerRun   float64       `yaml:"max_cost_per_run"`
	MaxTokensPerRun int           `yaml:"max_tokens_per_run"`
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

// IPCConfig controls Buckley's HTTP/WebSocket server.
type IPCConfig struct {
	Enabled           bool     `yaml:"enabled"`
	Bind              string   `yaml:"bind"`
	EnableBrowser     bool     `yaml:"enable_browser"`
	AllowedOrigins    []string `yaml:"allowed_origins"`
	PublicMetrics     bool     `yaml:"public_metrics"`
	RequireToken      bool     `yaml:"require_token"`
	BasicAuthEnabled  bool     `yaml:"basic_auth_enabled"`
	BasicAuthUsername string   `yaml:"basic_auth_username"`
	BasicAuthPassword string   `yaml:"basic_auth_password"`
	PushSubject       string   `yaml:"push_subject"` // mailto: or https: URL for VAPID (e.g., mailto:admin@example.com)
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

// MCPConfig defines MCP server settings for tool integration.
type MCPConfig struct {
	Enabled bool              `yaml:"enabled"`
	Servers []MCPServerConfig `yaml:"servers"`
}

// MCPServerConfig describes a single MCP server.
type MCPServerConfig struct {
	Name     string            `yaml:"name"`
	Command  string            `yaml:"command"`
	Args     []string          `yaml:"args"`
	Env      map[string]string `yaml:"env"`
	Timeout  time.Duration     `yaml:"timeout"`
	Disabled bool              `yaml:"disabled"`
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
	BaseURL string `yaml:"base_url"`
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
