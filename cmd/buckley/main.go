package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	acppb "github.com/odvcencio/buckley/pkg/acp/proto"
	acpserver "github.com/odvcencio/buckley/pkg/acp/server"
	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/conversation"
	coordination "github.com/odvcencio/buckley/pkg/coordination/coordinator"
	coordevents "github.com/odvcencio/buckley/pkg/coordination/events"
	"github.com/odvcencio/buckley/pkg/ipc"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	rlmrunner "github.com/odvcencio/buckley/pkg/rlm/runner"
	"github.com/odvcencio/buckley/pkg/setup"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/ui/buckley/tui"
)

// Version information - set via ldflags during build
var (
	version   = "1.0.0-dev"
	commit    = "unknown"
	buildDate = "unknown"
)

var encodingOverrideFlag string
var quietMode bool
var noColor bool
var configPath string
var rlmMode bool

// initDependenciesFn allows tests to stub dependency initialization without hitting the network.
var initDependenciesFn = initDependencies

// orchestratorRunner captures the subset of orchestrator behavior the CLI needs.
// It enables unit-testing CLI subcommands without live model calls.
type orchestratorRunner interface {
	PlanFeature(featureName, description string) (*orchestrator.Plan, error)
	LoadPlan(planID string) (*orchestrator.Plan, error)
	ExecutePlan() error
	ExecuteTask(taskID string) error
}

// newOrchestratorFn allows tests to stub orchestrator construction.
var newOrchestratorFn = func(store *storage.Store, mgr *model.Manager, registry *tool.Registry, cfg *config.Config, workflow *orchestrator.WorkflowManager, planStore orchestrator.PlanStore) orchestratorRunner {
	if cfg != nil && cfg.ExecutionMode() == config.ExecutionModeRLM {
		return rlmrunner.New(store, mgr, registry, cfg, workflow, planStore)
	}
	return orchestrator.NewOrchestrator(store, mgr, registry, cfg, workflow, planStore)
}

type startupOptions struct {
	prompt           string
	encodingOverride string
	args             []string
	resumeSessionID  string
	quiet            bool
	noColor          bool
	configPath       string
	plainModeSet     bool
	plainMode        bool
	rlmMode          bool
}

func main() {
	opts, err := parseStartupOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if err := opts.consumeResumeCommand(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	ensureBuckleyRuntimeIgnored()

	encodingOverrideFlag = opts.encodingOverride
	quietMode = opts.quiet
	noColor = opts.noColor
	configPath = opts.configPath
	rlmMode = opts.rlmMode
	os.Args = append([]string{os.Args[0]}, opts.args...)

	if handled, exitCode := dispatchSubcommand(opts.args); handled {
		os.Exit(exitCode)
	}

	resumeSessionID := opts.resumeSessionID
	plainMode := true
	if opts.plainModeSet {
		plainMode = opts.plainMode
	} else {
		plainMode = !isInteractiveTerminal()
	}
	promptFlag := opts.prompt
	args := opts.args

	// Check core dependencies
	checker := setup.NewChecker()
	if err := resolveDependencies(checker); err != nil {
		fmt.Fprintf(os.Stderr, "Setup error: %v\n", err)
		os.Exit(1)
	}

	// Load configuration
	var cfg *config.Config
	if configPath != "" {
		cfg, err = config.LoadFromPath(configPath)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(2)
	}
	applySandboxOverride(cfg)
	applyRLMOverride(cfg)
	tool.SetResultEncoding(cfg.Encoding.UseToon)

	if !cfg.Providers.HasReadyProvider() {
		fmt.Fprintln(os.Stderr, "Error: configure at least one provider (OPENROUTER_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLE_API_KEY, BUCKLEY_OLLAMA_ENABLED=1, or BUCKLEY_LITELLM_ENABLED=1).")
		os.Exit(2)
	}

	planStore := orchestrator.NewFilePlanStore(cfg.Artifacts.PlanningDir)

	// Create model manager
	modelManager, err := model.NewManager(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating model manager: %v\n", err)
		os.Exit(1)
	}

	// Initialize model manager (fetch catalog, validate models)
	if err := modelManager.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing models: %v\n", err)
		fmt.Fprintf(os.Stderr, "\nTip: Check your internet connection or API key validity.\n")
		fmt.Fprintf(os.Stderr, "     Run 'buckley config check' to validate your configuration.\n")
		os.Exit(1)
	}

	// Initialize storage
	dbPath, err := resolveDBPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	store, err := storage.New(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing storage: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	// Load project context (AGENTS.md)
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	loader := projectcontext.NewLoader(cwd)
	projectContext, err := loader.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading AGENTS.md: %v\n", err)
		os.Exit(1)
	}

	if resumeSessionID != "" {
		if sess, err := store.GetSession(resumeSessionID); err != nil || sess == nil {
			fmt.Fprintf(os.Stderr, "Error: session not found: %s\n", resumeSessionID)
			os.Exit(1)
		}
	}

	// Handle one-shot prompt mode (-p flag)
	if promptFlag != "" {
		// Prompt provided via -p flag
		exitCode := executeOneShot(promptFlag, cfg, modelManager, store, projectContext, planStore)
		os.Exit(exitCode)
	}

	// Check if stdin has data (piped input with -p)
	if len(args) == 0 && plainMode {
		stat, err := os.Stdin.Stat()
		if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
			// Stdin is piped, read all input
			scanner := bufio.NewScanner(os.Stdin)
			var lines []string
			for scanner.Scan() {
				lines = append(lines, scanner.Text())
			}
			if len(lines) > 0 {
				prompt := strings.Join(lines, "\n")
				exitCode := executeOneShot(prompt, cfg, modelManager, store, projectContext, planStore)
				os.Exit(exitCode)
			}
		}
	}

	coordRuntime, err := initCoordinationRuntime(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing coordination runtime: %v\n", err)
		os.Exit(1)
	}
	defer coordRuntime.Close()

	telemetryHub := telemetry.NewHub()
	defer telemetryHub.Close()
	stopTelemetry := startTelemetryPersistence(context.Background(), telemetryHub, coordRuntime.eventStore)
	defer stopTelemetry()

	// Create and run TUI
	ctrl, err := tui.NewController(tui.ControllerConfig{
		Config:       cfg,
		ModelManager: modelManager,
		Store:        store,
		ProjectCtx:   projectContext,
		Telemetry:    telemetryHub,
		SessionID:    resumeSessionID,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating TUI: %v\n", err)
		os.Exit(1)
	}

	// Set up signal handler
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		ctrl.Stop()
	}()

	if err := ctrl.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		os.Exit(1)
	}
}

// executeOneShot executes a single prompt and exits
func executeOneShot(prompt string, cfg *config.Config, mgr *model.Manager, store *storage.Store, projectContext *projectcontext.ProjectContext, planStore orchestrator.PlanStore) int {
	if !quietMode {
		if cwd, err := os.Getwd(); err == nil {
			fmt.Fprintf(os.Stderr, "workdir: %s\n", cwd)
		}
		if modelID := strings.TrimSpace(cfg.Models.Execution); modelID != "" {
			fmt.Fprintf(os.Stderr, "model: %s\n", modelID)
		}
	}
	if mgr != nil {
		mgr.SetRequestTimeout(0)
	}

	// Get model ID
	modelID := cfg.Models.Execution
	if modelID == "" {
		modelID = "openai/gpt-4o"
	}

	// Build system prompt with budgeted project context
	budget := promptBudget(cfg, mgr, modelID)
	if budget > 0 {
		budget -= estimateMessageTokens("user", prompt)
		if budget < 0 {
			budget = 0
		}
	}
	systemPrompt := buildOneShotSystemPrompt(projectContext, budget)

	// Build messages
	messages := []model.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: prompt},
	}

	// Create request
	req := model.ChatRequest{
		Model:    modelID,
		Messages: messages,
		Stream:   true,
	}

	// Stream response
	ctx := context.Background()
	chunkChan, errChan := mgr.ChatCompletionStream(ctx, req)

	for {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				fmt.Println() // Final newline
				return 0
			}
			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
				fmt.Print(chunk.Choices[0].Delta.Content)
			}
		case err, ok := <-errChan:
			if ok && err != nil {
				fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
				return 1
			}
		}
	}
}

func buildOneShotSystemPrompt(projectContext *projectcontext.ProjectContext, budgetTokens int) string {
	base := "You are Buckley, an AI development assistant. Be concise and helpful.\n\n"
	var b strings.Builder
	used := 0

	appendSection := func(content string, required bool) {
		if strings.TrimSpace(content) == "" {
			return
		}
		if !required && budgetTokens <= 0 {
			return
		}
		tokens := conversation.CountTokens(content)
		if budgetTokens > 0 && !required && used+tokens > budgetTokens {
			return
		}
		b.WriteString(content)
		used += tokens
	}

	appendSection(base, true)

	if projectContext != nil && projectContext.Loaded {
		rawProject := strings.TrimSpace(projectContext.RawContent)
		projectSummary := buildProjectContextSummary(projectContext)
		if budgetTokens > 0 && (rawProject != "" || projectSummary != "") {
			projectSection := ""
			if rawProject != "" {
				projectSection = "Project Context:\n" + rawProject + "\n\n"
			}
			summarySection := ""
			if projectSummary != "" {
				summarySection = "Project Context (summary):\n" + projectSummary + "\n\n"
			}

			remaining := budgetTokens - used
			if remaining > 0 {
				if projectSection != "" && conversation.CountTokens(projectSection) <= remaining {
					appendSection(projectSection, false)
				} else if summarySection != "" && conversation.CountTokens(summarySection) <= remaining {
					appendSection(summarySection, false)
				}
			}
		}
	}

	return strings.TrimSpace(b.String())
}

func applyToolDefaults(registry *tool.Registry, cfg *config.Config, hub *telemetry.Hub, sessionID string) {
	if registry == nil {
		return
	}
	defaults := tool.DefaultRegistryConfig()
	if cfg != nil {
		defaults.MaxOutputBytes = cfg.ToolMiddleware.MaxResultBytes
		defaults.Middleware.DefaultTimeout = cfg.ToolMiddleware.DefaultTimeout
		defaults.Middleware.PerToolTimeouts = copyDurationMap(cfg.ToolMiddleware.PerToolTimeouts)
		defaults.Middleware.MaxResultBytes = cfg.ToolMiddleware.MaxResultBytes
		defaults.Middleware.RetryConfig = tool.RetryConfig{
			MaxAttempts:  cfg.ToolMiddleware.Retry.MaxAttempts,
			InitialDelay: cfg.ToolMiddleware.Retry.InitialDelay,
			MaxDelay:     cfg.ToolMiddleware.Retry.MaxDelay,
			Multiplier:   cfg.ToolMiddleware.Retry.Multiplier,
			Jitter:       cfg.ToolMiddleware.Retry.Jitter,
		}
	}
	defaults.TelemetryHub = hub
	defaults.TelemetrySessionID = strings.TrimSpace(sessionID)
	tool.ApplyRegistryConfig(registry, defaults)
	if cfg != nil {
		registry.SetSandboxConfig(cfg.Sandbox.ToSandboxConfig(""))
	}
}

func copyDurationMap(src map[string]time.Duration) map[string]time.Duration {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]time.Duration, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func runPlanCommand(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: buckley plan <feature-name> <description>")
	}

	// Initialize dependencies
	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		return err
	}
	defer store.Close()

	featureName := args[0]
	description := strings.Join(args[1:], " ")

	// Create orchestrator
	registry := tool.NewRegistry()
	applyToolDefaults(registry, cfg, nil, "")
	if cwd, err := os.Getwd(); err == nil {
		registry.ConfigureContainers(cfg, cwd)
	}
	registerMCPTools(cfg, registry)
	planStore := orchestrator.NewFilePlanStore(cfg.Artifacts.PlanningDir)
	orch := newOrchestratorFn(store, mgr, registry, cfg, nil, planStore)

	// Generate plan
	fmt.Printf("Generating plan for: %s\n", featureName)
	plan, err := orch.PlanFeature(featureName, description)
	if err != nil {
		return fmt.Errorf("failed to create plan: %w", err)
	}

	fmt.Printf("\n✓ Plan created: %s\n\n", plan.ID)
	fmt.Printf("Feature: %s\n", plan.FeatureName)
	fmt.Printf("Tasks: %d\n", len(plan.Tasks))
	fmt.Printf("\nTo execute: buckley execute %s\n", plan.ID)

	return nil
}

func runExecuteCommand(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: buckley execute <plan-id>")
	}

	// Initialize dependencies
	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		return err
	}
	defer store.Close()

	planID := args[0]

	// Create orchestrator
	registry := tool.NewRegistry()
	applyToolDefaults(registry, cfg, nil, "")
	if err := registry.LoadDefaultPlugins(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load some plugins: %v\n", err)
	}
	if cwd, err := os.Getwd(); err == nil {
		registry.ConfigureContainers(cfg, cwd)
	}
	registerMCPTools(cfg, registry)

	planStore := orchestrator.NewFilePlanStore(cfg.Artifacts.PlanningDir)
	orch := newOrchestratorFn(store, mgr, registry, cfg, nil, planStore)

	// Load plan
	plan, err := orch.LoadPlan(planID)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	// Execute plan
	fmt.Printf("Executing plan: %s\n", plan.FeatureName)
	fmt.Printf("Tasks: %d\n\n", len(plan.Tasks))

	if err := orch.ExecutePlan(); err != nil {
		return fmt.Errorf("failed to execute plan: %w", err)
	}

	fmt.Println("\n✓ Plan execution complete")

	return nil
}

func runExecuteTaskCommand(args []string) error {
	fs := flag.NewFlagSet("execute-task", flag.ContinueOnError)
	defaultRemoteBranch := strings.TrimSpace(os.Getenv("BUCKLEY_REMOTE_BRANCH"))
	defaultRemoteName := strings.TrimSpace(os.Getenv("BUCKLEY_REMOTE_NAME"))
	if defaultRemoteName == "" {
		defaultRemoteName = "origin"
	}
	planID := fs.String("plan", "", "plan identifier")
	taskID := fs.String("task", "", "task identifier")
	workdir := fs.String("workdir", "", "working directory (defaults to BUCKLEY_TASK_WORKDIR)")
	repoURL := fs.String("repo-url", "", "repo URL to clone when no git repo is present (defaults to BUCKLEY_REPO_URL)")
	repoRef := fs.String("repo-ref", "", "git ref to checkout after clone (defaults to BUCKLEY_REPO_REF or BUCKLEY_GIT_BRANCH)")
	repoDir := fs.String("repo-dir", "", "directory to clone into (defaults to BUCKLEY_REPO_DIR)")
	remoteBranch := fs.String("remote-branch", defaultRemoteBranch, "remote branch to push after completion")
	remoteName := fs.String("remote-name", defaultRemoteName, "remote to push the branch to")
	pushChanges := fs.Bool("push", true, "push to the remote branch when set")
	if err := fs.Parse(args); err != nil {
		return err
	}

	remaining := fs.Args()
	if *planID == "" && len(remaining) > 0 {
		*planID = remaining[0]
	}
	if *taskID == "" && len(remaining) > 1 {
		*taskID = remaining[1]
	}

	if strings.TrimSpace(*planID) == "" || strings.TrimSpace(*taskID) == "" {
		return fmt.Errorf("usage: buckley execute-task --plan <plan-id> --task <task-id> [--remote-branch <branch>]")
	}

	if _, err := prepareTaskWorkspace(*workdir, *repoURL, *repoRef, *repoDir); err != nil {
		return err
	}

	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		return err
	}
	defer store.Close()

	registry := tool.NewRegistry()
	applyToolDefaults(registry, cfg, nil, "")
	if err := registry.LoadDefaultPlugins(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load some plugins: %v\n", err)
	}
	if cwd, err := os.Getwd(); err == nil {
		registry.ConfigureContainers(cfg, cwd)
	}
	registerMCPTools(cfg, registry)

	planStore := orchestrator.NewFilePlanStore(cfg.Artifacts.PlanningDir)
	orch := newOrchestratorFn(store, mgr, registry, cfg, nil, planStore)

	plan, err := orch.LoadPlan(*planID)
	if err != nil {
		return fmt.Errorf("failed to load plan: %w", err)
	}

	fmt.Printf("Executing task %s (%s)\n", *taskID, plan.FeatureName)
	if err := orch.ExecuteTask(*taskID); err != nil {
		return fmt.Errorf("failed to execute task: %w", err)
	}

	if branch := strings.TrimSpace(*remoteBranch); branch != "" && *pushChanges {
		if err := pushBranch(strings.TrimSpace(*remoteName), branch); err != nil {
			return err
		}
	}
	return nil
}

func runMigrateCommand() error {
	store, err := initIPCStore()
	if err != nil {
		return err
	}
	defer store.Close()
	fmt.Println("✅ Buckley migrations applied")
	return nil
}

func pushBranch(remote, branch string) error {
	if branch == "" {
		return nil
	}
	if remote == "" {
		remote = "origin"
	}
	fmt.Printf("Pushing HEAD to %s:%s\n", remote, branch)
	return runGitCommand("push", remote, fmt.Sprintf("HEAD:%s", branch))
}

func initDependencies() (*config.Config, *model.Manager, *storage.Store, error) {
	ensureBuckleyRuntimeIgnored()

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, nil, withExitCode(fmt.Errorf("failed to load config: %w", err), 2)
	}
	if encodingOverrideFlag != "" {
		cfg.Encoding.UseToon = encodingOverrideFlag != "json"
	}
	applyRLMOverride(cfg)
	tool.SetResultEncoding(cfg.Encoding.UseToon)

	if !cfg.Providers.HasReadyProvider() {
		return nil, nil, nil, withExitCode(fmt.Errorf("no providers configured; set OPENROUTER_API_KEY (recommended) or enable BUCKLEY_OLLAMA_ENABLED / BUCKLEY_LITELLM_ENABLED"), 2)
	}

	// Create model manager
	modelManager, err := model.NewManager(cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create model manager: %w", err)
	}

	// Initialize model manager
	if err := modelManager.Initialize(); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to initialize model manager: %w", err)
	}

	// Initialize storage
	dbPath, err := resolveDBPath()
	if err != nil {
		return nil, nil, nil, err
	}
	store, err := storage.New(dbPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	return cfg, modelManager, store, nil
}

func printHelp() {
	fmt.Println("Buckley - AI Development Assistant")
	fmt.Println()
	fmt.Println("USAGE:")
	fmt.Println("  buckley [FLAGS] [COMMAND]")
	fmt.Println()
	fmt.Println("MODES:")
	fmt.Println("  buckley                          Start interactive session (rich TUI by default)")
	fmt.Println("  buckley --plain                  Start with plain scrollback mode")
	fmt.Println("  buckley --tui                    Force rich TUI interface")
	fmt.Println("  buckley -p \"prompt\"              One-shot mode: run prompt and exit")
	fmt.Println()
	fmt.Println("COMMANDS:")
	fmt.Println("  plan <name> <desc>               Generate feature plan")
	fmt.Println("  execute <plan-id>                Execute a plan")
	fmt.Println("  execute-task --plan <id> --task <id>")
	fmt.Println("                                   Execute single task (CI/batch friendly)")
	fmt.Println("  commit [--dry-run]               Generate structured commit via tool-use (transparent)")
	fmt.Println("  pr [--dry-run]                   Generate structured PR via tool-use (transparent)")
	fmt.Println("  experiment run <name> -m <model> -p <prompt>")
	fmt.Println("                                   Run a parallel model comparison experiment")
	fmt.Println("  experiment list [--status <s>]   List recent experiments")
	fmt.Println("  experiment show <id|name>        Show experiment results (--format terminal|markdown)")
	fmt.Println("  experiment diff <id|name>        Compare variant outputs side-by-side")
	fmt.Println("  experiment replay <session-id>   Replay a session with a new model")
	fmt.Println("  serve [--bind host:port]         Start local HTTP/WebSocket server")
	fmt.Println("  remote <subcommand>              Remote session operations (attach, sessions, tokens, login, console)")
	fmt.Println("  batch prune-workspaces           Garbage-collect stale batch workspaces (k8s/CI)")
	fmt.Println("  git-webhook                      Listen for merge webhooks and run regression/release commands")
	fmt.Println("  agent-server                     HTTP proxy for ACP editor workflows (inline propose/apply)")
	fmt.Println("  lsp [--coordinator addr]         Start LSP server on stdio (editor integration)")
	fmt.Println("  acp [--workdir dir] [--log file] Start ACP agent on stdio (Zed/JetBrains/Neovim)")
	fmt.Println("  hunt [--dir path]                Scan codebase for improvement suggestions")
	fmt.Println("  dream [--dir path] [--plan]      Analyze architecture and identify gaps")
	fmt.Println("  ralph --prompt <p> [--timeout t] Autonomous task runner with iteration control")
	fmt.Println("  config [check|show|path]         Manage configuration")
	fmt.Println("  doctor                           Quick system health check (alias for config check)")
	fmt.Println("  completion [bash|zsh|fish]       Generate shell completions")
	fmt.Println("  worktree create [--container]    Create git worktree")
	fmt.Println("  migrate                          Apply database migrations")
	fmt.Println("  db backup --out <path>           Create a consistent SQLite backup (VACUUM INTO)")
	fmt.Println("  db restore --in <path> --force   Restore SQLite backup (stop Buckley first)")
	fmt.Println("  resume <session-id>              Resume a previous session")
	fmt.Println()
	fmt.Println("FLAGS:")
	fmt.Println("  -p <prompt>                      Run prompt in one-shot mode")
	fmt.Println("  -c, --config <path>              Use custom config file")
	fmt.Println("  -q, --quiet                      Suppress non-essential output")
	fmt.Println("  --no-color                       Disable colored output")
	fmt.Println("  --tui                            Use rich TUI interface")
	fmt.Println("  --plain                          Use plain scrollback mode")
	fmt.Println("  --rlm                            Use RLM execution mode (experimental)")
	fmt.Println("  --encoding json|toon             Set serialization format")
	fmt.Println("  --json                           Shortcut for --encoding json")
	fmt.Println("  -v, --version                    Show version information")
	fmt.Println("  -h, --help                       Show this help")
	fmt.Println()
	fmt.Println("ENVIRONMENT:")
	fmt.Println("  OPENROUTER_API_KEY               Provider API key (at least one provider key is required)")
	fmt.Println("  OPENAI_API_KEY                   Provider API key (optional alternative)")
	fmt.Println("  ANTHROPIC_API_KEY                Provider API key (optional alternative)")
	fmt.Println("  GOOGLE_API_KEY                   Provider API key (optional alternative)")
	fmt.Println("  BUCKLEY_MODEL_PLANNING           Override planning model")
	fmt.Println("  BUCKLEY_MODEL_EXECUTION          Override execution model")
	fmt.Println("  BUCKLEY_MODEL_REVIEW             Override review model")
	fmt.Println("  BUCKLEY_MODEL_COMMIT             Override model for `buckley commit`")
	fmt.Println("  BUCKLEY_MODEL_PR                 Override model for `buckley pr`")
	fmt.Println("  BUCKLEY_PROMPT_COMMIT            Override prompt template for `buckley commit`")
	fmt.Println("  BUCKLEY_PROMPT_PR                Override prompt template for `buckley pr`")
	fmt.Println("  BUCKLEY_PR_BASE                  Override PR base branch (e.g., main)")
	fmt.Println("  BUCKLEY_REMOTE_NAME              Remote name for pushes (default: origin)")
	fmt.Println("  BUCKLEY_IPC_TOKEN                IPC auth token (required for remote binds when enabled)")
	fmt.Println("  BUCKLEY_GENERATE_IPC_TOKEN       Auto-generate an IPC token when missing (serve mode)")
	fmt.Println("  BUCKLEY_IPC_TOKEN_FILE           Read/write the IPC token from this path (serve mode)")
	fmt.Println("  BUCKLEY_PRINT_GENERATED_IPC_TOKEN Print generated IPC token to stderr (serve mode; use cautiously)")
	fmt.Println("  BUCKLEY_BASIC_AUTH_USER          IPC basic auth username (optional)")
	fmt.Println("  BUCKLEY_BASIC_AUTH_PASSWORD      IPC basic auth password (optional)")
	fmt.Println("  BUCKLEY_DB_PATH                  Override primary SQLite DB path")
	fmt.Println("  BUCKLEY_DATA_DIR                 Directory containing Buckley DB files (db, remote-auth, checkpoints, etc)")
	fmt.Println("  BUCKLEY_LOG_DIR                  Override telemetry log directory")
	fmt.Println("  BUCKLEY_QUIET                    Suppress non-essential output")
	fmt.Println("  NO_COLOR                         Disable colored output")
	fmt.Println()
	fmt.Println("CONFIGURATION:")
	fmt.Println("  User config:    ~/.buckley/config.yaml")
	fmt.Println("  Project config: ./.buckley/config.yaml")
	fmt.Println("  Run 'buckley config check' to validate your setup")
	fmt.Println()
	fmt.Println("GETTING STARTED:")
	fmt.Println("  1. Get an API key: https://openrouter.ai/keys")
	fmt.Println(`  2. Run: export OPENROUTER_API_KEY="<YOUR_OPENROUTER_API_KEY>"`)
	fmt.Println("  3. Start: buckley")
	fmt.Println("  4. Type /help for available commands")
	fmt.Println()
	fmt.Println("DOCUMENTATION:")
	fmt.Println("  https://github.com/odvcencio/buckley")
}

func printVersion() {
	fmt.Printf("Buckley %s\n", version)
	if commit != "unknown" {
		fmt.Printf("  Commit:     %s\n", commit)
	}
	if buildDate != "unknown" {
		fmt.Printf("  Built:      %s\n", buildDate)
	}
	fmt.Printf("  Go version: %s\n", runtime.Version())
}

func runConfigCommand(args []string) error {
	subCmd := "show"
	if len(args) > 0 {
		subCmd = args[0]
	}

	switch subCmd {
	case "check":
		return runConfigCheck()
	case "show":
		return runConfigShow()
	case "path":
		return runConfigPath()
	default:
		return fmt.Errorf("unknown config command: %s (use check, show, or path)", subCmd)
	}
}

func runConfigCheck() error {
	fmt.Println("Checking Buckley configuration...")
	fmt.Println()

	// Check config files
	home, _ := os.UserHomeDir()
	userConfig := filepath.Join(home, ".buckley", "config.yaml")
	projectConfig := ".buckley/config.yaml"

	fmt.Println("Configuration files:")
	if _, err := os.Stat(userConfig); err == nil {
		fmt.Printf("  ✓ User config:    %s\n", userConfig)
	} else {
		fmt.Printf("  - User config:    %s (not found)\n", userConfig)
	}
	if _, err := os.Stat(projectConfig); err == nil {
		fmt.Printf("  ✓ Project config: %s\n", projectConfig)
	} else {
		fmt.Printf("  - Project config: %s (not found)\n", projectConfig)
	}
	fmt.Println()

	// Check API keys
	fmt.Println("API keys:")
	providers := []struct {
		name   string
		envVar string
	}{
		{"OpenRouter", "OPENROUTER_API_KEY"},
		{"OpenAI", "OPENAI_API_KEY"},
		{"Anthropic", "ANTHROPIC_API_KEY"},
		{"Google", "GOOGLE_API_KEY"},
	}

	hasProvider := false
	for _, p := range providers {
		if key := os.Getenv(p.envVar); key != "" {
			fmt.Printf("  ✓ %s: configured\n", p.name)
			hasProvider = true
		} else {
			fmt.Printf("  - %s: not set\n", p.name)
		}
	}

	// Check config.env fallback
	if !hasProvider {
		if key := checkConfigEnvFile(); key != "" {
			fmt.Printf("  ✓ OpenRouter: found in ~/.buckley/config.env\n")
			hasProvider = true
		}
	}
	fmt.Println()

	// Check dependencies
	fmt.Println("Dependencies:")
	if _, err := exec.LookPath("git"); err == nil {
		fmt.Println("  ✓ git: installed")
	} else {
		fmt.Println("  ✗ git: not found (required)")
	}
	fmt.Println()

	// Load and validate config
	cfg, err := config.Load()
	if err != nil {
		return withExitCode(err, 2)
	}

	// Show validation warnings
	warnings := cfg.ValidationWarnings()
	if len(warnings) > 0 {
		fmt.Println("Warnings:")
		for _, w := range warnings {
			fmt.Printf("  ⚠ %s\n", w)
		}
		fmt.Println()
	}

	if cfg.Providers.HasReadyProvider() {
		fmt.Println("✓ Configuration is valid")
	} else {
		fmt.Println("✗ No provider configured")
		fmt.Println()
		fmt.Println(`To fix: export OPENROUTER_API_KEY="<YOUR_OPENROUTER_API_KEY>"`)
		fmt.Println("Or enable a local provider (BUCKLEY_OLLAMA_ENABLED=1 or BUCKLEY_LITELLM_ENABLED=1).")
		fmt.Println("Get a key at: https://openrouter.ai/keys")
		return withExitCode(fmt.Errorf("no providers configured"), 2)
	}

	return nil
}

func runConfigShow() error {
	cfg, err := config.Load()
	if err != nil {
		return withExitCode(fmt.Errorf("failed to load config: %w", err), 2)
	}

	fmt.Println("Current configuration:")
	fmt.Println()
	fmt.Printf("Models:\n")
	fmt.Printf("  Planning:  %s\n", cfg.Models.Planning)
	fmt.Printf("  Execution: %s\n", cfg.Models.Execution)
	fmt.Printf("  Review:    %s\n", cfg.Models.Review)
	fmt.Println()
	fmt.Printf("Orchestrator:\n")
	fmt.Printf("  Trust level: %s\n", cfg.Orchestrator.TrustLevel)
	fmt.Printf("  Auto workflow: %v\n", cfg.Orchestrator.AutoWorkflow)
	fmt.Println()
	fmt.Printf("Providers:\n")
	for _, p := range cfg.Providers.ReadyProviders() {
		fmt.Printf("  ✓ %s\n", p)
	}
	return nil
}

func runConfigPath() error {
	home, _ := os.UserHomeDir()
	fmt.Println("Configuration file locations:")
	fmt.Printf("  User:    %s\n", filepath.Join(home, ".buckley", "config.yaml"))
	fmt.Printf("  Project: %s\n", ".buckley/config.yaml")
	fmt.Printf("  Env:     %s\n", filepath.Join(home, ".buckley", "config.env"))
	dbPath, err := resolveDBPath()
	if err != nil {
		dbPath = fmt.Sprintf("error: %v", err)
	}
	fmt.Printf("  DB:      %s\n", dbPath)
	return nil
}

func checkConfigEnvFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	envPath := filepath.Join(home, ".buckley", "config.env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		if strings.HasPrefix(line, "OPENROUTER_API_KEY=") {
			key := strings.TrimPrefix(line, "OPENROUTER_API_KEY=")
			return strings.Trim(key, "\"'")
		}
	}
	return ""
}

func runCompletionCommand(args []string) error {
	if len(args) == 0 {
		fmt.Println("Generate shell completions for Buckley")
		fmt.Println()
		fmt.Println("Usage: buckley completion [bash|zsh|fish]")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  # Bash (add to ~/.bashrc)")
		fmt.Println("  eval \"$(buckley completion bash)\"")
		fmt.Println()
		fmt.Println("  # Zsh (add to ~/.zshrc)")
		fmt.Println("  eval \"$(buckley completion zsh)\"")
		fmt.Println()
		fmt.Println("  # Fish (add to ~/.config/fish/config.fish)")
		fmt.Println("  buckley completion fish | source")
		return nil
	}

	shell := args[0]
	switch shell {
	case "bash":
		printBashCompletion()
	case "zsh":
		printZshCompletion()
	case "fish":
		printFishCompletion()
	default:
		return fmt.Errorf("unsupported shell: %s (use bash, zsh, or fish)", shell)
	}
	return nil
}

func printBashCompletion() {
	fmt.Print(`_buckley_completions() {
    local cur prev commands
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev="${COMP_WORDS[COMP_CWORD-1]}"

    commands="plan execute execute-task commit pr experiment serve remote batch git-webhook agent-server lsp acp config doctor completion worktree migrate db resume help version"

    case "${prev}" in
        buckley)
            COMPREPLY=( $(compgen -W "${commands} --help --version --tui --plain --quiet --no-color --config" -- "${cur}") )
            return 0
            ;;
        batch)
            COMPREPLY=( $(compgen -W "prune-workspaces" -- "${cur}") )
            return 0
            ;;
        config)
            COMPREPLY=( $(compgen -W "check show path" -- "${cur}") )
            return 0
            ;;
        experiment)
            COMPREPLY=( $(compgen -W "run" -- "${cur}") )
            return 0
            ;;
        completion)
            COMPREPLY=( $(compgen -W "bash zsh fish" -- "${cur}") )
            return 0
            ;;
        db)
            COMPREPLY=( $(compgen -W "backup restore" -- "${cur}") )
            return 0
            ;;
        --config|-c)
            COMPREPLY=( $(compgen -f -- "${cur}") )
            return 0
            ;;
    esac

    COMPREPLY=( $(compgen -W "${commands}" -- "${cur}") )
}
complete -F _buckley_completions buckley
`)
}

func printZshCompletion() {
	fmt.Print(`#compdef buckley

_buckley() {
    local -a commands
    commands=(
        'plan:Generate feature plan'
        'execute:Execute a plan'
        'execute-task:Execute single task'
        'commit:Create action-style commit'
        'pr:Create pull request'
        'experiment:Run model comparison experiments'
        'serve:Start local server'
        'remote:Remote session management'
        'batch:Batch helpers (k8s/CI)'
        'git-webhook:Run regression/release webhooks daemon'
        'agent-server:Run ACP HTTP proxy for editor workflows'
        'lsp:Start LSP server on stdio for editor integration'
        'acp:Start ACP agent on stdio for Zed/JetBrains/Neovim'
        'config:Manage configuration'
        'completion:Generate shell completions'
        'worktree:Git worktree management'
        'migrate:Apply database migrations'
        'db:Backup/restore SQLite DB'
        'resume:Resume a previous session'
        'doctor:Quick system health check'
        'help:Show help information'
        'version:Show version information'
    )

    _arguments -C \
        '-p[Run prompt in one-shot mode]:prompt:' \
        '-c[Use custom config file]:config file:_files' \
        '--config[Use custom config file]:config file:_files' \
        '-q[Suppress non-essential output]' \
        '--quiet[Suppress non-essential output]' \
        '--no-color[Disable colored output]' \
        '--tui[Use rich TUI interface]' \
        '--plain[Use plain scrollback mode]' \
        '-v[Show version]' \
        '--version[Show version]' \
        '-h[Show help]' \
        '--help[Show help]' \
        '1: :->command' \
        '*::arg:->args'

    case $state in
        command)
            _describe -t commands 'buckley commands' commands
            ;;
        args)
            case $words[1] in
                batch)
                    _values 'batch command' prune-workspaces
                    ;;
                experiment)
                    _values 'experiment command' run
                    ;;
                config)
                    _values 'config command' check show path
                    ;;
                completion)
                    _values 'shell' bash zsh fish
                    ;;
                db)
                    _values 'db command' backup restore
                    ;;
            esac
            ;;
    esac
}

_buckley "$@"
`)
}

func printFishCompletion() {
	fmt.Print(`# Fish completion for buckley

complete -c buckley -f

# Commands
complete -c buckley -n __fish_use_subcommand -a plan -d 'Generate feature plan'
complete -c buckley -n __fish_use_subcommand -a execute -d 'Execute a plan'
complete -c buckley -n __fish_use_subcommand -a execute-task -d 'Execute single task'
complete -c buckley -n __fish_use_subcommand -a commit -d 'Create action-style commit'
complete -c buckley -n __fish_use_subcommand -a pr -d 'Create pull request'
complete -c buckley -n __fish_use_subcommand -a experiment -d 'Run model comparison experiments'
complete -c buckley -n __fish_use_subcommand -a serve -d 'Start local server'
complete -c buckley -n __fish_use_subcommand -a remote -d 'Remote session management'
complete -c buckley -n __fish_use_subcommand -a batch -d 'Batch helpers (k8s/CI)'
complete -c buckley -n __fish_use_subcommand -a git-webhook -d 'Run regression/release webhooks daemon'
complete -c buckley -n __fish_use_subcommand -a agent-server -d 'Run ACP HTTP proxy for editor workflows'
complete -c buckley -n __fish_use_subcommand -a lsp -d 'Start LSP server on stdio'
complete -c buckley -n __fish_use_subcommand -a acp -d 'Start ACP agent on stdio (Zed/JetBrains/Neovim)'
complete -c buckley -n __fish_use_subcommand -a config -d 'Manage configuration'
complete -c buckley -n __fish_use_subcommand -a completion -d 'Generate shell completions'
complete -c buckley -n __fish_use_subcommand -a worktree -d 'Git worktree management'
complete -c buckley -n __fish_use_subcommand -a migrate -d 'Apply database migrations'
complete -c buckley -n __fish_use_subcommand -a db -d 'Backup/restore SQLite DB'
complete -c buckley -n __fish_use_subcommand -a resume -d 'Resume a previous session'
complete -c buckley -n __fish_use_subcommand -a doctor -d 'Quick system health check'
complete -c buckley -n __fish_use_subcommand -a help -d 'Show help information'
complete -c buckley -n __fish_use_subcommand -a version -d 'Show version information'

# Global flags
complete -c buckley -s p -d 'Run prompt in one-shot mode'
complete -c buckley -s c -l config -d 'Use custom config file' -r
complete -c buckley -s q -l quiet -d 'Suppress non-essential output'
complete -c buckley -l no-color -d 'Disable colored output'
complete -c buckley -l tui -d 'Use rich TUI interface'
complete -c buckley -l plain -d 'Use plain scrollback mode'
complete -c buckley -s v -l version -d 'Show version'
complete -c buckley -s h -l help -d 'Show help'

# Config subcommands
complete -c buckley -n '__fish_seen_subcommand_from config' -a check -d 'Validate configuration'
complete -c buckley -n '__fish_seen_subcommand_from config' -a show -d 'Show current configuration'
complete -c buckley -n '__fish_seen_subcommand_from config' -a path -d 'Show config file paths'

# Experiment subcommands
complete -c buckley -n '__fish_seen_subcommand_from experiment' -a run -d 'Run an experiment'

# Completion subcommands
complete -c buckley -n '__fish_seen_subcommand_from completion' -a bash -d 'Generate bash completion'
complete -c buckley -n '__fish_seen_subcommand_from completion' -a zsh -d 'Generate zsh completion'
complete -c buckley -n '__fish_seen_subcommand_from completion' -a fish -d 'Generate fish completion'

# DB subcommands
complete -c buckley -n '__fish_seen_subcommand_from db' -a backup -d 'Create a consistent SQLite backup'
complete -c buckley -n '__fish_seen_subcommand_from db' -a restore -d 'Restore an SQLite backup'

# Batch subcommands
complete -c buckley -n '__fish_seen_subcommand_from batch' -a prune-workspaces -d 'Garbage-collect stale batch workspaces'
`)
}

func dispatchSubcommand(args []string) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "--version", "-v", "version":
		printVersion()
		return true, 0
	case "--help", "-h", "help":
		printHelp()
		return true, 0
	case "plan":
		return true, runCommand(runPlanCommand, args[1:])
	case "execute":
		return true, runCommand(runExecuteCommand, args[1:])
	case "remote":
		return true, runCommand(runRemoteCommand, args[1:])
	case "batch":
		return true, runCommand(runBatchCommand, args[1:])
	case "git-webhook":
		return true, runCommand(runGitWebhookCommand, args[1:])
	case "execute-task":
		return true, runCommand(runExecuteTaskCommand, args[1:])
	case "commit":
		return true, runCommand(runCommitCommand, args[1:])
	case "pr":
		return true, runCommand(runPRCommand, args[1:])
	case "review":
		return true, runCommand(runReviewCommand, args[1:])
	case "review-pr":
		return true, runCommand(runReviewPRCommand, args[1:])
	case "experiment":
		return true, runCommand(runExperimentCommand, args[1:])
	case "serve":
		return true, runCommand(runServeCommand, args[1:])
	case "migrate":
		if err := runMigrateCommand(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return true, exitCodeForError(err)
		}
		return true, 0
	case "db":
		return true, runCommand(runDBCommand, args[1:])
	case "worktree":
		return true, runCommand(runWorktreeCommand, args[1:])
	case "resume":
		return false, 0
	case "agent-server":
		return true, runCommand(runAgentServerCommand, args[1:])
	case "lsp":
		return true, runCommand(runLSPCommand, args[1:])
	case "acp":
		return true, runCommand(runACPCommand, args[1:])
	case "hunt":
		return true, runCommand(runHuntCommand, args[1:])
	case "dream":
		return true, runCommand(runDreamCommand, args[1:])
	case "ralph":
		return true, runCommand(runRalphCommand, args[1:])
	case "config":
		return true, runCommand(runConfigCommand, args[1:])
	case "doctor":
		// Alias for config check - quick system health check
		return true, runCommand(runConfigCommand, []string{"check"})
	case "completion":
		return true, runCommand(runCompletionCommand, args[1:])
	default:
		if strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(os.Stderr, "Error: unknown flag: %s\n", args[0])
		} else {
			fmt.Fprintf(os.Stderr, "Error: unknown command: %s\n", args[0])
		}
		fmt.Fprintln(os.Stderr, "Run 'buckley --help' for usage.")
		return true, 1
	}
}

func runCommand(handler func([]string) error, args []string) int {
	if err := handler(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return exitCodeForError(err)
	}
	return 0
}

func parseStartupOptions(raw []string) (*startupOptions, error) {
	opts := &startupOptions{}
	if val, ok := parseBoolEnv("BUCKLEY_QUIET"); ok {
		opts.quiet = val
	}
	if val, ok := parseBoolEnv("NO_COLOR"); ok {
		opts.noColor = val
	}

	filtered := make([]string, 0, len(raw))
	var nextPrompt bool
	var nextEncoding bool
	var nextConfig bool

	for _, arg := range raw {
		if nextPrompt {
			opts.prompt = arg
			nextPrompt = false
			continue
		}
		if nextEncoding {
			opts.encodingOverride = strings.ToLower(arg)
			nextEncoding = false
			continue
		}
		if nextConfig {
			opts.configPath = arg
			nextConfig = false
			continue
		}

		switch arg {
		case "--plain", "--no-tui":
			opts.plainModeSet = true
			opts.plainMode = true
		case "--tui":
			opts.plainModeSet = true
			opts.plainMode = false
		case "-p":
			nextPrompt = true
		case "--encoding":
			nextEncoding = true
		case "--encoding=toon":
			opts.encodingOverride = "toon"
		case "--encoding=json", "--json":
			opts.encodingOverride = "json"
		case "--quiet", "-q":
			opts.quiet = true
		case "--no-color":
			opts.noColor = true
		case "--rlm":
			opts.rlmMode = true
		case "--config", "-c":
			nextConfig = true
		default:
			if strings.HasPrefix(arg, "--config=") {
				opts.configPath = strings.TrimPrefix(arg, "--config=")
			} else {
				filtered = append(filtered, arg)
			}
		}
	}

	if nextPrompt {
		return nil, fmt.Errorf("-p requires a prompt argument")
	}
	if nextEncoding {
		return nil, fmt.Errorf("--encoding requires a value")
	}
	if nextConfig {
		return nil, fmt.Errorf("--config requires a path argument")
	}

	opts.args = filtered
	return opts, nil
}

func (o *startupOptions) consumeResumeCommand() error {
	if len(o.args) == 0 || o.args[0] != "resume" {
		return nil
	}
	if len(o.args) < 2 {
		return fmt.Errorf("usage: buckley resume <session-id>")
	}
	o.resumeSessionID = o.args[1]
	o.args = o.args[:0]
	return nil
}

func parseBoolEnv(key string) (bool, bool) {
	val := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if val == "" {
		return false, false
	}
	switch val {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func applySandboxOverride(cfg *config.Config) {
	if cfg == nil {
		return
	}
	mode := strings.TrimSpace(strings.ToLower(os.Getenv("BUCKLEY_SANDBOX")))
	if mode == "" {
		mode = strings.TrimSpace(strings.ToLower(os.Getenv("BUCKLEY_SANDBOX_MODE")))
	}
	switch mode {
	case "container", "containers", "devcontainer", "sandbox", "on", "true", "yes":
		cfg.Worktrees.UseContainers = true
	case "host", "off", "disable", "disabled", "false", "no":
		cfg.Worktrees.UseContainers = false
	}
}

func applyRLMOverride(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if rlmMode {
		cfg.Execution.Mode = config.ExecutionModeRLM
		cfg.Oneshot.Mode = config.ExecutionModeRLM
	}
}

func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) &&
		term.IsTerminal(int(os.Stdout.Fd()))
}

// ansiEscapePattern matches ANSI escape sequences including:
// - CSI sequences like \x1b[...X (cursor reports, colors, etc.)
// - OSC sequences like \x1b]...ST (window titles, etc.)
// - Simple escapes like \x1b[?1h
var ansiEscapePattern = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[a-zA-Z]|\][^\x07]*\x07|\[[^\x1b]*[a-zA-Z])`)

// sanitizeTerminalInput removes ANSI escape sequences from input
// This filters out cursor position reports, color codes, and other
// terminal responses that can leak into pasted text
func sanitizeTerminalInput(input string) string {
	return ansiEscapePattern.ReplaceAllString(input, "")
}

func resolveDependencies(checker *setup.Checker) error {
	missing, err := checker.CheckAll()
	if err != nil {
		return err
	}

	if len(missing) == 0 {
		return nil
	}

	if err := checker.RunWizard(missing); err != nil {
		return err
	}

	missing, err = checker.CheckAll()
	if err != nil {
		return err
	}

	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for _, dep := range missing {
			names = append(names, dep.Name)
		}
		return fmt.Errorf("missing dependencies: %s", strings.Join(names, ", "))
	}

	return nil
}

func hasACPTLS(cfg config.ACPConfig) bool {
	return strings.TrimSpace(cfg.TLSCertFile) != "" &&
		strings.TrimSpace(cfg.TLSKeyFile) != "" &&
		strings.TrimSpace(cfg.TLSClientCAFile) != ""
}

func isLoopbackAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func startEmbeddedIPCServer(cfg *config.Config, store *storage.Store, telemetryHub *telemetry.Hub, commandGateway *command.Gateway, planStore orchestrator.PlanStore, workflow *orchestrator.WorkflowManager, models *model.Manager) (func(), string, error) {
	ipcCfg := cfg.IPC
	if !ipcCfg.Enabled {
		return nil, "", nil
	}
	if strings.TrimSpace(ipcCfg.Bind) == "" {
		return nil, "", nil
	}

	token := strings.TrimSpace(os.Getenv("BUCKLEY_IPC_TOKEN"))
	if ipcCfg.RequireToken && token == "" && !ipcCfg.BasicAuthEnabled {
		return nil, "", fmt.Errorf("IPC token required (set BUCKLEY_IPC_TOKEN)")
	}

	projectRoot := config.ResolveProjectRoot(cfg)
	serverCfg := ipc.Config{
		BindAddress:       ipcCfg.Bind,
		StaticDir:         "",
		EnableBrowser:     ipcCfg.EnableBrowser,
		AllowedOrigins:    append([]string{}, ipcCfg.AllowedOrigins...),
		PublicMetrics:     ipcCfg.PublicMetrics,
		RequireToken:      ipcCfg.RequireToken,
		AuthToken:         token,
		Version:           version,
		BasicAuthEnabled:  ipcCfg.BasicAuthEnabled,
		BasicAuthUsername: ipcCfg.BasicAuthUsername,
		BasicAuthPassword: ipcCfg.BasicAuthPassword,
		ProjectRoot:       projectRoot,
	}

	ctx, cancel := context.WithCancel(context.Background())
	server := ipc.NewServer(serverCfg, store, telemetryHub, commandGateway, planStore, cfg, workflow, models)

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start(ctx)
	}()

	select {
	case err := <-errCh:
		cancel()
		if errors.Is(err, context.Canceled) {
			return nil, "", nil
		}
		return nil, "", err
	case <-time.After(350 * time.Millisecond):
	}

	url := humanReadableURL(serverCfg.BindAddress)
	stop := func() {
		cancel()
	}
	return stop, url, nil
}

func humanReadableURL(bind string) string {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return fmt.Sprintf("http://%s", bind)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if port == "" {
		return fmt.Sprintf("http://%s", host)
	}
	return fmt.Sprintf("http://%s:%s", host, port)
}

// startACPServer launches the ACP gRPC server when configured.
func startACPServer(cfg *config.Config, mgr *model.Manager, store *storage.Store, runtime *coordinationRuntime) (func(), error) {
	acpCfg := cfg.ACP
	if strings.TrimSpace(acpCfg.Listen) == "" {
		return nil, nil
	}

	useTLS := hasACPTLS(acpCfg)
	allowInsecure := acpCfg.AllowInsecureLocal && isLoopbackAddress(acpCfg.Listen)
	if !useTLS && !allowInsecure {
		return nil, fmt.Errorf("acp listener %s requires mTLS or allow_insecure_local=true on loopback", acpCfg.Listen)
	}

	var tlsCfg *tls.Config
	if useTLS {
		cert, err := tls.LoadX509KeyPair(acpCfg.TLSCertFile, acpCfg.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load ACP TLS certs: %w", err)
		}

		clientCAPEM, err := os.ReadFile(acpCfg.TLSClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("load ACP client CA: %w", err)
		}

		clientCAPool := x509.NewCertPool()
		if ok := clientCAPool.AppendCertsFromPEM(clientCAPEM); !ok {
			return nil, fmt.Errorf("invalid ACP client CA bundle")
		}

		tlsCfg = &tls.Config{
			Certificates: []tls.Certificate{cert},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    clientCAPool,
			MinVersion:   tls.VersionTLS12,
		}
	} else {
		fmt.Fprintf(os.Stderr, "Warning: starting ACP without TLS (allow_insecure_local=true for %s)\n", acpCfg.Listen)
	}

	var err error
	var eventStore coordevents.EventStore
	var closeStore func()
	if runtime != nil && runtime.eventStore != nil {
		eventStore = runtime.eventStore
	} else {
		store, closer, err := buildCoordinationEventStore(cfg)
		if err != nil {
			return nil, err
		}
		eventStore = store
		closeStore = closer
	}

	coord := (*coordination.Coordinator)(nil)
	if runtime != nil {
		coord = runtime.coordinator
	}
	if coord == nil {
		coord, err = coordination.NewCoordinator(coordination.DefaultConfig(), eventStore)
		if err != nil {
			if closeStore != nil {
				closeStore()
			}
			return nil, fmt.Errorf("init ACP coordinator: %w", err)
		}
	}
	if coord == nil {
		if closeStore != nil {
			closeStore()
		}
		return nil, fmt.Errorf("init ACP coordinator: coordinator is nil")
	}
	srv, err := acpserver.NewServer(coord, mgr, cfg, store)
	if err != nil {
		if closeStore != nil {
			closeStore()
		}
		return nil, fmt.Errorf("init ACP gRPC server: %w", err)
	}

	grpcOpts := []grpc.ServerOption{}
	if tlsCfg != nil {
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}
	grpcOpts = append(grpcOpts,
		grpc.ChainUnaryInterceptor(srv.UnaryAuthInterceptor),
		grpc.ChainStreamInterceptor(srv.StreamAuthInterceptor),
	)
	grpcServer := grpc.NewServer(grpcOpts...)
	acppb.RegisterAgentCommunicationServer(grpcServer, srv)

	lis, err := net.Listen("tcp", acpCfg.Listen)
	if err != nil {
		if closeStore != nil {
			closeStore()
		}
		return nil, fmt.Errorf("listen on %s: %w", acpCfg.Listen, err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- grpcServer.Serve(lis)
	}()

	stop := func() {
		grpcServer.GracefulStop()
		if closeStore != nil {
			closeStore()
		}
	}

	// Non-blocking health check: give the server a moment to start
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			stop()
			return nil, err
		}
	case <-time.After(150 * time.Millisecond):
	}

	fmt.Printf("🚀 ACP gRPC server listening on %s (event store: %s)\n", acpCfg.Listen, strings.ToLower(acpCfg.EventStore))
	return stop, nil
}
