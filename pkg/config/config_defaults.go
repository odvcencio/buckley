package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/giturl"
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

func defaultNATSURL() string {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		return "nats://nats:4222"
	}
	return "nats://127.0.0.1:4222"
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
		},
		Sandbox: defaultSandboxConfig(),
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
				"moonshotai/kimi-k2.5",
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
			MessageMetadata:           "always",
			Audio: UIAudioConfig{
				Enabled:      false,
				AssetsPath:   "",
				MasterVolume: 100,
				SFXVolume:    80,
				MusicVolume:  60,
				Muted:        false,
			},
		},
		WebUI: WebUIConfig{
			BaseURL: "",
		},
		Commenting: CommentingConfig{
			RequireFunctionDocs:           true,
			RequireBlockCommentsOverLines: 10,
			CommentNonObviousOnly:         true,
		},
	}
}
