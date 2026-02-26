package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/odvcencio/buckley/pkg/buckley/ui/tui"
	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	rlmrunner "github.com/odvcencio/buckley/pkg/rlm/runner"
	"github.com/odvcencio/buckley/pkg/setup"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
)

// Version information - set via ldflags during build
var (
	version   = "0.1.0"
	commit    = "unknown"
	buildDate = "unknown"
)

// defaultFallbackModel is the compile-time fallback model used when no model
// is configured and BUCKLEY_DEFAULT_MODEL is not set.
const defaultFallbackModel = "openai/gpt-4o"

// getDefaultModel returns the default model to use when none is configured.
// It checks the BUCKLEY_DEFAULT_MODEL environment variable first, falling back
// to the compile-time defaultFallbackModel constant.
func getDefaultModel() string {
	if m := os.Getenv("BUCKLEY_DEFAULT_MODEL"); m != "" {
		return m
	}
	return defaultFallbackModel
}

// cliFlags holds mutable CLI state that was formerly package-level vars.
// It is populated once by main() and read by initDependencies / executeOneShot.
var cliFlags struct {
	encodingOverride string
	quiet            bool
	noColor          bool
	configPath       string
	rlmMode          bool
}

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
	agentSocket      string
	verbose          bool
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

	cliFlags.encodingOverride = opts.encodingOverride
	cliFlags.quiet = opts.quiet
	cliFlags.noColor = opts.noColor
	cliFlags.configPath = opts.configPath
	cliFlags.rlmMode = opts.rlmMode
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
	if cliFlags.configPath != "" {
		cfg, err = config.LoadFromPath(cliFlags.configPath)
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
		sess, err := store.GetSession(resumeSessionID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: looking up session %s: %v\n", resumeSessionID, err)
			os.Exit(1)
		}
		if sess == nil {
			fmt.Fprintf(os.Stderr, "Error: session not found: %s\n", resumeSessionID)
			os.Exit(1)
		}
	}

	// Handle one-shot prompt mode (-p flag)
	if promptFlag != "" {
		// Check if stdin is also piped; if so, prepend as context
		prompt := promptFlag
		stat, stErr := os.Stdin.Stat()
		if stErr == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
			scanner := bufio.NewScanner(os.Stdin)
			var lines []string
			for scanner.Scan() {
				lines = append(lines, scanner.Text())
			}
			if len(lines) > 0 {
				piped := strings.Join(lines, "\n")
				prompt = "<stdin>\n" + piped + "\n</stdin>\n\n" + promptFlag
			}
		}
		exitCode := executeOneShot(prompt, cfg, modelManager, store, projectContext, planStore, opts.verbose)
		os.Exit(exitCode)
	}

	// Handle piped stdin (no -p flag)
	if len(args) == 0 && plainMode {
		stat, err := os.Stdin.Stat()
		if err == nil && (stat.Mode()&os.ModeCharDevice) == 0 {
			scanner := bufio.NewScanner(os.Stdin)
			var lines []string
			for scanner.Scan() {
				lines = append(lines, scanner.Text())
			}
			if len(lines) > 0 {
				prompt := strings.Join(lines, "\n")
				exitCode := executeOneShot(prompt, cfg, modelManager, store, projectContext, planStore, opts.verbose)
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
		AgentSocket:  opts.agentSocket,
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
