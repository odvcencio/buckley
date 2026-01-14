package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/giturl"
	"github.com/odvcencio/buckley/pkg/personality"
	corev1 "k8s.io/api/core/v1"
)

const (
	defaultOpenRouterModel = "moonshotai/kimi-k2-thinking"
	defaultOpenAIModel     = "openai/gpt-5.2-codex-xhigh"
	defaultAnthropicModel  = "anthropic/claude-sonnet-4-5"
	defaultGoogleModel     = "google/gemini-3-pro"

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
)

var providerDefaultModels = map[string]string{
	"openrouter": defaultOpenRouterModel,
	"openai":     defaultOpenAIModel,
	"anthropic":  defaultAnthropicModel,
	"google":     defaultGoogleModel,
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
	DefaultProvider string              `yaml:"default_provider"` // Default provider (openrouter, openai, anthropic, google)
	Reasoning       string              `yaml:"reasoning"`        // Reasoning level: "off", "low", "medium", "high", or "" for auto-detect

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
	HighContrast    bool `yaml:"high_contrast"`    // Use high-contrast color scheme
	UseTextLabels   bool `yaml:"use_text_labels"`  // Add text labels to color-only indicators
	ReduceAnimation bool `yaml:"reduce_animation"` // Reduce or disable animations
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

func defaultACPStore() string {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return "nats"
	}
	return "sqlite"
}

func defaultNATSURL() string {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return "nats://nats:4222"
	}
	return "nats://127.0.0.1:4222"
}

// BatchConfig controls containerized task execution
type BatchConfig struct {
	Enabled           bool                    `yaml:"enabled"`
	Namespace         string                  `yaml:"namespace"`
	Kubeconfig        string                  `yaml:"kubeconfig"`
	WaitForCompletion bool                    `yaml:"wait_for_completion"`
	FollowLogs        bool                    `yaml:"follow_logs"`
	JobTemplate       BatchJobTemplateConfig  `yaml:"job_template"`
	RemoteBranch      BatchRemoteBranchConfig `yaml:"remote_branch"`
}

// BatchJobTemplateConfig defines the job template for each task container
type BatchJobTemplateConfig struct {
	Image                   string                      `yaml:"image"`
	ImagePullPolicy         string                      `yaml:"image_pull_policy"`
	ServiceAccount          string                      `yaml:"service_account"`
	Command                 []string                    `yaml:"command"`
	Args                    []string                    `yaml:"args"`
	Env                     map[string]string           `yaml:"env"`
	EnvFromSecrets          []string                    `yaml:"env_from_secrets"`
	EnvFromConfigMaps       []string                    `yaml:"env_from_configmaps"`
	WorkspaceClaim          string                      `yaml:"workspace_claim"`
	WorkspaceMountPath      string                      `yaml:"workspace_mount_path"`
	WorkspaceVolumeTemplate *BatchVolumeTemplateConfig  `yaml:"workspace_volume_template"`
	SharedConfigClaim       string                      `yaml:"shared_config_claim"`
	SharedConfigMountPath   string                      `yaml:"shared_config_mount_path"`
	TTLSecondsAfterFinished int32                       `yaml:"ttl_seconds_after_finished"`
	BackoffLimit            int32                       `yaml:"backoff_limit"`
	ImagePullSecrets        []string                    `yaml:"image_pull_secrets"`
	Resources               corev1.ResourceRequirements `yaml:"resources"`
	NodeSelector            map[string]string           `yaml:"node_selector"`
	Tolerations             []corev1.Toleration         `yaml:"tolerations"`
	Affinity                *corev1.Affinity            `yaml:"affinity"`
	ConfigMap               string                      `yaml:"config_map"`
	ConfigMapMountPath      string                      `yaml:"config_map_mount_path"`
}

// BatchVolumeTemplateConfig defines ephemeral PVC templates mounted per task.
type BatchVolumeTemplateConfig struct {
	StorageClass string   `yaml:"storage_class"`
	AccessModes  []string `yaml:"access_modes"`
	Size         string   `yaml:"size"`
}

// BatchRemoteBranchConfig describes how remote feature branches are generated
type BatchRemoteBranchConfig struct {
	Enabled    bool   `yaml:"enabled"`
	Prefix     string `yaml:"prefix"`
	RemoteName string `yaml:"remote_name"`
}

// DefaultConfig returns sensible defaults
func DefaultConfig() *Config {
	return &Config{
		Models: ModelConfig{
			Planning:  defaultOpenRouterModel,
			Execution: defaultOpenRouterModel,
			Review:    defaultOpenRouterModel,
			VisionFallback: []string{
				"openai/gpt-5.2-mini",
				"google/gemini-3-flash",
			},
			FallbackChains:  map[string][]string{},
			DefaultProvider: "openrouter",
			Utility: UtilityModelConfig{
				Commit:     DefaultUtilityModel,
				PR:         DefaultUtilityModel,
				Compaction: DefaultUtilityModel,
				TodoPlan:   DefaultUtilityModel,
			},
		},
		Providers: ProviderConfig{
			OpenRouter: ProviderSettings{
				Enabled: true,
				BaseURL: "https://openrouter.ai/api/v1",
			},
			OpenAI: ProviderSettings{
				Enabled: false,
				BaseURL: "https://api.openai.com/v1",
			},
			Anthropic: ProviderSettings{
				Enabled: false,
				BaseURL: "https://api.anthropic.com/v1",
			},
			Google: ProviderSettings{
				Enabled: false,
				BaseURL: "https://generativelanguage.googleapis.com/v1beta",
			},
			Ollama: ProviderSettings{
				Enabled: false,
				BaseURL: "http://localhost:11434",
			},
			LiteLLM: LiteLLMConfig{
				Enabled: false,
				BaseURL: "http://localhost:4000",
			},
			ModelRouting: map[string]string{
				"openai/":    "openai",
				"anthropic/": "anthropic",
				"google/":    "google",
				"ollama/":    "ollama",
				"litellm/":   "litellm",
				"gpt-":       "openai",
				"claude-":    "anthropic",
				"gemini-":    "google",
				"o1-":        "openai",
				"o3-":        "openai",
				"chatgpt-":   "openai",
			},
		},
		PromptCache: PromptCacheConfig{
			Enabled:        false,
			Providers:      []string{"anthropic"},
			SystemMessages: 1,
			TailMessages:   2,
		},
		Encoding: EncodingConfig{
			UseToon: true,
		},
		Diagnostics: DiagnosticsConfig{
			NetworkLogsEnabled: false,
		},
		Personality: PersonalityConfig{
			Enabled:          true,
			QuirkProbability: 0.15,
			Tone:             "friendly",
		},
		Memory: MemoryConfig{
			AutoCompactThreshold: 0.75,
			MaxCompactions:       0,  // 0 = unlimited
			SummaryTimeoutSecs:   30, // 30 second timeout for compaction
			RetrievalEnabled:     true,
			RetrievalLimit:       5,
			RetrievalMaxTokens:   1200,
		},
		Orchestrator: OrchestratorConfig{
			MaxSelfHealAttempts: 3,
			MaxReviewCycles:     3,
			TrustLevel:          "balanced",
			AutoWorkflow:        false,
			Planning: PlanningConfig{
				Enabled:             true, // Orchestrator-first: planning enabled by default
				ComplexityThreshold: 0.5,  // Lower threshold = more tasks get planned
				LongRunEnabled:      true, // Auto-decide when clear winner
				LongRunMaxMinutes:   30,
				LongRunLogDecisions: true,
				LongRunPauseOnRisk:  true,
			},
		},
		Execution: ExecutionModeConfig{
			Mode: DefaultExecutionMode,
		},
		Oneshot: OneshotModeConfig{
			Mode: DefaultOneshotMode,
		},
		RLM: RLMConfig{
			Coordinator: RLMCoordinatorConfig{
				Model:               "auto",
				MaxIterations:       10,
				MaxTokensBudget:     0, // 0 = unlimited
				MaxWallTime:         10 * time.Minute,
				ConfidenceThreshold: 0.95,
				StreamPartials:      true,
			},
			SubAgent: RLMSubAgentConfig{
				Model:         "",              // Empty = use execution model
				MaxConcurrent: 3,               // Parallel sub-agent limit
				Timeout:       5 * time.Minute, // Per-task timeout
			},
			Scratchpad: RLMScratchpadConfig{
				MaxEntriesMemory:  1000,
				MaxRawBytesMemory: 50 * 1024 * 1024,
				EvictionPolicy:    "lru",
				DefaultTTL:        time.Hour,
				PersistArtifacts:  true,
				PersistDecisions:  true,
			},
		},
		Approval: ApprovalConfig{
			Mode:         "safe", // Safe by default - workspace writes, read-only shell
			TrustedPaths: []string{},
			DeniedPaths: []string{
				"~/.ssh",
				"~/.gnupg",
				"~/.aws",
				"/etc",
				"/var",
			},
			AllowNetwork: false,
			AllowedTools: []string{
				"read_file",
				"list_files",
				"search_files",
				"semantic_search",
			},
			DeniedTools: []string{},
			AutoApprovePatterns: []string{
				"go test",
				"go build",
				"go fmt",
				"go vet",
				"npm test",
				"npm run build",
				"make test",
				"make build",
				"cargo test",
				"cargo build",
				"pytest",
			},
		},
		ToolMiddleware: ToolMiddlewareConfig{
			DefaultTimeout: 2 * time.Minute,
			MaxResultBytes: 100_000,
			Retry: ToolRetryConfig{
				MaxAttempts:  2,
				InitialDelay: 200 * time.Millisecond,
				MaxDelay:     2 * time.Second,
				Multiplier:   2,
				Jitter:       0.2,
			},
		},
		MCP: MCPConfig{
			Enabled: false,
			Servers: []MCPServerConfig{},
		},
		ACP: ACPConfig{
			EventStore:         defaultACPStore(),
			Listen:             "",
			AllowInsecureLocal: false,
			TLSCertFile:        "",
			TLSKeyFile:         "",
			TLSClientCAFile:    "",
			NATS: NATSConfig{
				URL:            defaultNATSURL(),
				StreamPrefix:   "acp",
				SnapshotBucket: "acp_snapshots",
				ConnectTimeout: 5 * time.Second,
				RequestTimeout: 5 * time.Second,
			},
		},
		Worktrees: WorktreeConfig{
			UseContainers:    false,
			RootPath:         "",
			ContainerService: "dev",
		},
		Experiment: ExperimentConfig{
			Enabled:         true,
			MaxConcurrent:   4,
			DefaultTimeout:  30 * time.Minute,
			WorktreeRoot:    ".buckley/experiments",
			CleanupOnDone:   true,
			MaxCostPerRun:   1.00,
			MaxTokensPerRun: 100000,
		},
		Batch: BatchConfig{
			Enabled:           false,
			Namespace:         "",
			Kubeconfig:        "",
			WaitForCompletion: true,
			FollowLogs:        true,
			JobTemplate: BatchJobTemplateConfig{
				Image:                   "", // Uses deployment image by default
				ImagePullPolicy:         "IfNotPresent",
				ServiceAccount:          "",
				Command:                 []string{"buckley"},
				Args:                    []string{"execute-task", "--plan", "{{PLAN_ID}}", "--task", "{{TASK_ID}}"},
				Env:                     map[string]string{"BUCKLEY_PLAIN_MODE": "1"},
				WorkspaceClaim:          "",
				WorkspaceMountPath:      "/workspace",
				SharedConfigClaim:       "",
				SharedConfigMountPath:   "/buckley/shared",
				TTLSecondsAfterFinished: 600,
				BackoffLimit:            1,
				ImagePullSecrets:        []string{},
			},
			RemoteBranch: BatchRemoteBranchConfig{
				Enabled:    true,
				Prefix:     "automation/",
				RemoteName: "origin",
			},
		},
		GitClone: giturl.ClonePolicy{
			AllowedSchemes:       []string{"https", "ssh"},
			AllowedHosts:         nil, // allow all
			DeniedHosts:          nil,
			DenyPrivateNetworks:  false,
			ResolveDNS:           true,
			DenySCPSyntax:        false,
			DNSResolveTimeoutSec: 2,
		},
		IPC: IPCConfig{
			Enabled:           false,
			Bind:              "127.0.0.1:4488",
			EnableBrowser:     false,
			AllowedOrigins:    []string{"http://localhost", "http://127.0.0.1"},
			PublicMetrics:     false,
			RequireToken:      false,
			BasicAuthEnabled:  false,
			BasicAuthUsername: "",
			BasicAuthPassword: "",
		},
		CostManagement: CostConfig{
			SessionBudget: 10.00,
			DailyBudget:   20.00,
			MonthlyBudget: 200.00,
			AutoStopAt:    50.00,
		},
		RetryPolicy: RetryPolicy{
			MaxRetries:     3,
			InitialBackoff: 1 * time.Second,
			MaxBackoff:     30 * time.Second,
			Multiplier:     2.0,
		},
		Artifacts: ArtifactsConfig{
			PlanningDir:          "docs/plans",
			ExecutionDir:         "docs/execution",
			ReviewDir:            "docs/reviews",
			ArchiveDir:           "docs/archive",
			ArchiveByMonth:       true,
			AutoArchiveOnPRMerge: true,
		},
		Workflow: WorkflowConfig{
			PlanningQuestionsMin:              5,
			PlanningQuestionsMax:              10,
			IncrementalApproval:               true,
			PauseOnBusinessAmbiguity:          true,
			PauseOnArchitecturalConflict:      true,
			PauseOnComplexityExplosion:        true,
			PauseOnEnvironmentMismatch:        true,
			ReviewIterationsMax:               5,
			AllowNitsInApproval:               true,
			GenerateOpportunisticImprovements: true,
			TaskPhaseLoop:                     []string{"builder", "verify", "review"},
			TaskPhases: []TaskPhaseConfig{
				{
					Stage:       "builder",
					Name:        "Builder",
					Description: "Generate and apply code changes for the current task.",
					Targets:     []string{"Translate plan pseudocode into code", "Run necessary tools/commands"},
				},
				{
					Stage:       "verify",
					Name:        "Verifier",
					Description: "Validate results locally before review.",
					Targets:     []string{"Run tests/linters", "Check for edge cases"},
				},
				{
					Stage:       "review",
					Name:        "Reviewer",
					Description: "Review artifacts and enforce quality gates.",
					Targets:     []string{"Catch regressions", "Ensure conventions/tests"},
				},
			},
		},
		Compaction: CompactionConfig{
			ContextThreshold: 0.80,
			RLMAutoTrigger:   0.85,
			CompactionRatio:  0.45,
			TaskInterval:     20,
			TokenThreshold:   15000,
			TargetReduction:  0.70,
			PreserveCommands: true,
			Models: []string{
				"moonshotai/kimi-k2-thinking",
				"qwen/qwen3-coder",
				"openai/gpt-5.2-mini",
			},
		},
		UI: UIConfig{
			ActivityPanelDefault:      "collapsed",
			DiffViewerDefault:         "collapsed",
			ToolGroupingWindowSeconds: 30,
			ShowToolCosts:             true,
			ShowIntentStatements:      true,
			SidebarWidth:              24,
			SidebarMinWidth:           16,
			SidebarMaxWidth:           60,
		},
		Commenting: CommentingConfig{
			RequireFunctionDocs:           true,
			RequireBlockCommentsOverLines: 10,
			CommentNonObviousOnly:         true,
		},
	}
}

// Load loads configuration from default locations with proper precedence
func Load() (*Config, error) {
	// Start with defaults
	cfg := DefaultConfig()

	configEnv := loadConfigEnvVars()

	// Load user config (~/.buckley/config.yaml)
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to HOME env var if UserHomeDir fails
		home = os.Getenv("HOME")
	}
	if home != "" {
		userConfigPath := filepath.Join(home, ".buckley", "config.yaml")
		if err := loadAndMerge(cfg, userConfigPath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("loading user config: %w", err)
		}
	}

	// Load project config (./.buckley/config.yaml)
	projectConfigPath := filepath.Join(".", ".buckley", "config.yaml")
	if err := loadAndMerge(cfg, projectConfigPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("loading project config: %w", err)
	}

	// Apply environment variable overrides
	applyEnvOverrides(cfg, configEnv)
	cfg.alignModelDefaultsWithProviders()

	// Validate
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return cfg, nil
}

// LoadFromPath loads configuration from a specific file path
func LoadFromPath(path string) (*Config, error) {
	cfg := DefaultConfig()

	configEnv := loadConfigEnvVars()

	// Load from the specified path
	if err := loadAndMerge(cfg, path); err != nil {
		return nil, fmt.Errorf("loading config from %s: %w", path, err)
	}

	// Apply environment variable overrides
	applyEnvOverrides(cfg, configEnv)
	cfg.alignModelDefaultsWithProviders()

	// Validate
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation: %w", err)
	}

	return cfg, nil
}

// ApplyEnvOverridesForTest exposes env override logic for tests without file I/O.
func ApplyEnvOverridesForTest(cfg *Config) {
	applyEnvOverrides(cfg, nil)
	cfg.alignModelDefaultsWithProviders()
}

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
	if v := os.Getenv("BUCKLEY_TRUST_LEVEL"); v != "" {
		cfg.Orchestrator.TrustLevel = v
	}
	if v := os.Getenv("BUCKLEY_APPROVAL_MODE"); v != "" {
		cfg.Approval.Mode = v
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

func isLoopbackBindAddress(addr string) bool {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return false
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	switch strings.ToLower(host) {
	case "localhost":
		return true
	case "0.0.0.0", "::":
		return false
	default:
		ip := net.ParseIP(host)
		if ip == nil {
			return false
		}
		return ip.IsLoopback()
	}
}

// ExecutionMode returns the normalized execution mode.
func (c *Config) ExecutionMode() string {
	if c == nil {
		return DefaultExecutionMode
	}
	return normalizeMode(c.Execution.Mode, DefaultExecutionMode)
}

// OneshotMode returns the normalized oneshot mode.
func (c *Config) OneshotMode() string {
	if c == nil {
		return DefaultOneshotMode
	}
	return normalizeMode(c.Oneshot.Mode, DefaultOneshotMode)
}

func normalizeMode(mode, fallback string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return fallback
	}
	return mode
}

// Validate checks configuration validity
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

// ReadyProviders returns identifiers for providers that have usable configuration.
func (p *ProviderConfig) ReadyProviders() []string {
	var providers []string
	for _, providerID := range []string{"openrouter", "openai", "anthropic", "google", "ollama", "litellm"} {
		if p.ready(providerID) {
			providers = append(providers, providerID)
		}
	}
	return providers
}

// HasReadyProvider returns true when at least one provider can be used.
func (p *ProviderConfig) HasReadyProvider() bool {
	return len(p.ReadyProviders()) > 0
}

func (p *ProviderConfig) ready(providerID string) bool {
	switch providerID {
	case "openrouter":
		return p.OpenRouter.Enabled && p.OpenRouter.APIKey != ""
	case "openai":
		return p.OpenAI.Enabled && p.OpenAI.APIKey != ""
	case "anthropic":
		return p.Anthropic.Enabled && p.Anthropic.APIKey != ""
	case "google":
		return p.Google.Enabled && p.Google.APIKey != ""
	case "ollama":
		return p.Ollama.Enabled
	case "litellm":
		return p.LiteLLM.Enabled
	default:
		return false
	}
}

func (c *Config) alignModelDefaultsWithProviders() {
	if c.Providers.ready("openrouter") {
		if c.Models.DefaultProvider == "" {
			c.Models.DefaultProvider = "openrouter"
		}
		return
	}

	fallbackProvider := c.preferredReadyProvider()
	if fallbackProvider == "" {
		return
	}

	if c.Models.DefaultProvider == "" || c.Models.DefaultProvider == "openrouter" {
		c.Models.DefaultProvider = fallbackProvider
	}

	fallbackModel := providerDefaultModels[fallbackProvider]
	if fallbackModel == "" {
		return
	}

	c.replaceModelIfDefault(&c.Models.Planning, fallbackModel)
	c.replaceModelIfDefault(&c.Models.Execution, fallbackModel)
	c.replaceModelIfDefault(&c.Models.Review, fallbackModel)
}

func (c *Config) preferredReadyProvider() string {
	if c.Providers.ready(c.Models.DefaultProvider) {
		return c.Models.DefaultProvider
	}

	for _, providerID := range []string{"openai", "anthropic", "google", "litellm", "ollama"} {
		if c.Providers.ready(providerID) {
			return providerID
		}
	}

	return ""
}

func (c *Config) replaceModelIfDefault(field *string, fallback string) {
	if *field == "" || *field == defaultOpenRouterModel {
		*field = fallback
	}
}

func loadConfigEnvVars() map[string]string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}

	path := filepath.Join(home, ".buckley", "config.env")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	vars := make(map[string]string)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "" {
			continue
		}
		value = strings.Trim(value, "\"'")
		vars[key] = value
	}
	return vars
}
