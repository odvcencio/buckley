package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/giturl"
	"m31labs.dev/buckley/pkg/policy"
)

func defaultACPStore() string {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return "nats"
	}
	return "sqlite"
}

func defaultSandboxConfig() SandboxConfig {
	cfg := SandboxConfig{
		Mode:           "workspace",
		AllowUnsafe:    false,
		AllowNetwork:   false,
		Timeout:        5 * time.Minute,
		MaxOutputBytes: 10 * 1024 * 1024,
		DockerSandbox: DockerSandboxConfig{
			Enabled:          false,
			Image:            "ubuntu:24.04",
			WorkspaceMount:   "/workspace",
			ReadOnlyRoot:     true,
			KeepAlive:        true,
			KeepAliveTimeout: 10 * time.Minute,
			Resources: ResourceLimitsConfig{
				CPUs:      "1.0",
				Memory:    "512m",
				PidsLimit: 256,
				TmpfsSize: "64m",
			},
			Security: SecurityConfig{
				NoNewPrivileges:  true,
				DropCapabilities: []string{"ALL"},
			},
		},
	}

	cwd, err := os.Getwd()
	if err != nil || strings.TrimSpace(cwd) == "" {
		cwd = "."
	}
	cfg.WorkspacePath = cwd
	cfg.AllowedPaths = []string{cwd}

	home, homeErr := os.UserHomeDir()
	if homeErr != nil || strings.TrimSpace(home) == "" {
		home = filepath.Join(string(os.PathSeparator), "root")
	}
	cfg.DeniedPaths = append(cfg.DeniedPaths,
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".aws"),
	)
	cfg.DeniedPaths = append(cfg.DeniedPaths, "/etc", "/var", "/usr", "/bin", "/sbin")
	cfg.DeniedCommands = []string{
		"rm -rf /",
		"rm -rf ~",
		"sudo rm",
		"chmod 777",
		"curl | sh",
		"curl | bash",
		"wget | sh",
		"wget | bash",
	}

	return cfg
}

func defaultDeniedPaths() []string {
	paths := []string{"/etc", "/var"}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = filepath.Join(string(os.PathSeparator), "root")
	}
	paths = append(paths,
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".aws"),
	)
	return paths
}

// defaultPosturesConfig ships the two built-in postures: "interactive"
// (empty layer, today's behavior) and "unattended" (flags outward-facing
// bash as "ask" and parks those decisions instead of blocking on human
// approval). The active posture defaults to "interactive" unless
// overridden by postures.default or the BUCKLEY_POSTURE env var
// (pkg/policy.SelectPosture).
func defaultPosturesConfig() PosturesConfig {
	return PosturesConfig{
		Default: policy.PostureInteractive,
		Layers: map[string]PostureConfig{
			policy.PostureInteractive: {
				Rules:            nil,
				ParkAskDecisions: false,
			},
			policy.PostureUnattended: {
				Rules:            policy.UnattendedPostureRules(),
				ParkAskDecisions: true,
			},
		},
	}
}

func defaultNATSURL() string {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return "nats://nats:4222"
	}
	return "nats://127.0.0.1:4222"
}

// DefaultConfig returns sensible defaults
func DefaultConfig() *Config {
	cfg := &Config{
		Buckbot: BuckbotConfig{
			Model:                      defaultBuckbotModel,
			CriticModel:                defaultBuckbotCriticModel,
			Reasoning:                  "auto",
			OpenRouterPrivacyFallback:  "",
			PerReviewBudgetUSD:         0,
			MonthlyBudgetUSD:           0,
			MaxReviewIterations:        0,
			MaxToolCalls:               0,
			MaxValidationAttempts:      2,
			MaxDiffBytes:               240_000,
			MaxSupportingContextTokens: 12_000,
			PostingCoreAssociations:    []string{"OWNER", "MEMBER", "COLLABORATOR"},
			PostingAllowlist:           nil,
			PostingSizeThresholdBytes:  0,
		},
		Models: ModelConfig{
			Planning:  defaultOpenRouterModel,
			Execution: defaultOpenRouterModel,
			Review:    defaultOpenRouterModel,
			Curated: []string{
				defaultOpenRouterChatModel,
				defaultOpenRouterKimiCode,
				defaultOpenRouterQwenMax,
			},
			VisionFallback: []string{
				"openai/gpt-5.4-mini",
				"google/gemini-3-flash",
			},
			FallbackChains: map[string][]string{
				defaultOpenRouterChatModel: {
					defaultOpenRouterKimiCode,
					defaultOpenRouterQwenMax,
					defaultOpenRouterUtilityModel,
				},
				defaultOpenRouterKimiCode: {
					defaultOpenRouterQwenMax,
					defaultOpenRouterChatModel,
					defaultOpenRouterUtilityModel,
				},
				defaultOpenRouterQwenMax: {
					defaultOpenRouterChatModel,
					defaultOpenRouterKimiCode,
					defaultOpenRouterUtilityModel,
				},
			},
			DefaultProvider:      "openrouter",
			ProviderContinuation: false,
			Utility: UtilityModelConfig{
				Commit:     DefaultCommitModel,
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
			Codex: CodexConfig{
				Enabled: false,
				Command: "codex",
				Models:  []string{defaultCodexModel},
			},
			ModelRouting: map[string]string{
				"openai/":    "openai",
				"anthropic/": "anthropic",
				"google/":    "google",
				"deepseek/":  "openrouter",
				"qwen/":      "openrouter",
				"ollama/":    "ollama",
				"litellm/":   "litellm",
				"codex/":     "codex",
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
			Providers:      []string{"anthropic", "openrouter", "litellm", "openai"},
			SystemMessages: 1,
			TailMessages:   2,
			Key:            "",
			Retention:      "",
		},
		Encoding: EncodingConfig{
			UseToon: true,
		},
		Diagnostics: DiagnosticsConfig{
			NetworkLogsEnabled:           false,
			TelemetryPayloadsOverNetwork: false,
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
			RalphCompatibility:   true,
			HyphaeRecall:         true,
			HyphaeSpace:          "",
			HyphaePromotion:      false,
			TillerInterchange:    false,
			GraftVCS:             true,
		},
		ContextFabric: ContextFabricConfig{
			Enabled:         false,
			Shadow:          true,
			PolicyVersion:   "context-selection-v1",
			Renderer:        "lx",
			OutputFormat:    "markdown",
			BudgetTolerance: 0.02,
			ReceiptReuse:    true,
			AutoExpand:      false,
			RemoteInputs:    false,
			Pressure: ContextFabricPressureConfig{
				Dedupe:     0.45,
				Checkpoint: 0.65,
				Compact:    0.70,
				Emergency:  0.85,
			},
			Evidence: ContextFabricEvidenceConfig{
				StoreEnabled:  true,
				InlineBytes:   8192,
				RetentionDays: 30,
				Compress:      true,
			},
			Checkpoint: ContextFabricCheckpointConfig{
				Enabled:     false,
				ModelPolish: false,
			},
		},
		AgentController: AgentControllerConfig{
			Mode:          "legacy",
			PolicyVersion: "agent-next-action-v1",
			Critic: AgentControllerCriticConfig{
				Enabled:             false,
				Model:               "",
				ConfidenceThreshold: 0.65,
			},
			EmergencyFuse: AgentControllerEmergencyFuseConfig{
				ModelRequests:  500,
				ToolExecutions: 2000,
				WallTime:       6 * time.Hour,
			},
		},
		AgentOperations: AgentOperationsConfig{
			ReviewChanges:     true,
			ReviewPullRequest: true,
			PostReview:        false,
			PrepareCommit:     true,
			CommitChanges:     false,
			PushChanges:       false,
		},
		Metrics: MetricsConfig{
			Enabled:           true,
			Export:            false,
			IncludeRawContent: false,
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
			Mode:           DefaultExecutionMode,
			DurableBackend: DefaultDurableBackend,
		},
		Oneshot: OneshotModeConfig{
			Mode: DefaultOneshotMode,
		},
	}
	applyDefaultRuntimeConfig(cfg)
	return cfg
}

func applyDefaultRuntimeConfig(cfg *Config) {
	cfg.RLM = RLMConfig{
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
	}
	cfg.Approval = ApprovalConfig{
		Mode:         "safe", // Safe by default - workspace writes, read-only shell
		TrustedPaths: []string{},
		DeniedPaths:  defaultDeniedPaths(),
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
	}
	cfg.Sandbox = defaultSandboxConfig()
	cfg.Permissions = PermissionsConfig{}
	cfg.Postures = defaultPosturesConfig()
	cfg.ToolMiddleware = ToolMiddlewareConfig{
		DefaultTimeout: 2 * time.Minute,
		MaxResultBytes: 100_000,
		Retry: ToolRetryConfig{
			MaxAttempts:  2,
			InitialDelay: 200 * time.Millisecond,
			MaxDelay:     2 * time.Second,
			Multiplier:   2,
			Jitter:       0.2,
		},
	}
	cfg.Tools = ToolsConfig{DefaultPoolMode: "full"}
	cfg.Hooks = PluginHooksConfig{Enabled: false, DefaultTimeoutMs: 3000}
	cfg.MCP = MCPConfig{Enabled: false, Servers: []MCPServerConfig{}, MaxTools: DefaultMCPMaxTools}
	cfg.ACP = ACPConfig{
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
	}
	cfg.Worktrees = WorktreeConfig{
		UseContainers:    false,
		RootPath:         "",
		ContainerService: "dev",
	}
	cfg.Experiment = ExperimentConfig{
		Enabled:         true,
		MaxConcurrent:   4,
		DefaultTimeout:  30 * time.Minute,
		WorktreeRoot:    ".buckley/experiments",
		CleanupOnDone:   true,
		MaxCostPerRun:   1.00,
		MaxTokensPerRun: 100000,
	}
	cfg.Batch = BatchConfig{
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
	}
	cfg.GitClone = giturl.ClonePolicy{
		AllowedSchemes:       []string{"https", "ssh"},
		AllowedHosts:         nil, // allow all
		DeniedHosts:          nil,
		DenyPrivateNetworks:  false,
		ResolveDNS:           true,
		DenySCPSyntax:        false,
		DNSResolveTimeoutSec: 2,
	}
	cfg.IPC = IPCConfig{
		Enabled:           false,
		Bind:              "127.0.0.1:4488",
		EnableBrowser:     false,
		AllowedOrigins:    []string{"http://localhost", "http://127.0.0.1"},
		PublicMetrics:     false,
		RequireToken:      false,
		BasicAuthEnabled:  false,
		BasicAuthUsername: "",
		BasicAuthPassword: "",
	}
	cfg.CostManagement = CostConfig{
		SessionBudget: 10.00,
		DailyBudget:   20.00,
		MonthlyBudget: 200.00,
		AutoStopAt:    50.00,
	}
	cfg.RetryPolicy = RetryPolicy{
		MaxRetries:     3,
		InitialBackoff: 1 * time.Second,
		MaxBackoff:     30 * time.Second,
		Multiplier:     2.0,
	}
	cfg.Artifacts = ArtifactsConfig{
		PlanningDir:          "docs/plans",
		ExecutionDir:         "docs/execution",
		ReviewDir:            "docs/reviews",
		ArchiveDir:           "docs/archive",
		ArchiveByMonth:       true,
		AutoArchiveOnPRMerge: true,
	}
	cfg.Workflow = WorkflowConfig{
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
	}
	cfg.Compaction = CompactionConfig{
		ContextThreshold: 0.80,
		RLMAutoTrigger:   0.85,
		CompactionRatio:  0.45,
		TaskInterval:     20,
		TokenThreshold:   15000,
		TargetReduction:  0.70,
		PreserveCommands: true,
		Models: []string{
			defaultOpenRouterUtilityModel,
		},
	}
	cfg.UI = UIConfig{
		ActivityPanelDefault:      "collapsed",
		DiffViewerDefault:         "collapsed",
		ToolGroupingWindowSeconds: 30,
		ShowToolCosts:             true,
		ShowIntentStatements:      true,
		SidebarWidth:              24,
		SidebarMinWidth:           16,
		SidebarMaxWidth:           60,
		MessageMetadata:           "always",
		Audio: UIAudioConfig{
			Enabled:      false,
			AssetsPath:   "",
			MasterVolume: 100,
			SFXVolume:    80,
			MusicVolume:  60,
			Muted:        false,
		},
	}
	cfg.WebUI = WebUIConfig{BaseURL: ""}
	cfg.Commenting = CommentingConfig{
		RequireFunctionDocs:           true,
		RequireBlockCommentsOverLines: 10,
		CommentNonObviousOnly:         true,
	}
}
