package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/filewatch"
	"github.com/odvcencio/buckley/pkg/headless"
	"github.com/odvcencio/buckley/pkg/ipc/command"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/ralph"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"gopkg.in/yaml.v3"
)

// getRalphDataDir returns the base directory for Ralph data (~/.buckley/ralph/).
func getRalphDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	if home == "" {
		return "", fmt.Errorf("could not determine home directory")
	}
	return filepath.Join(home, ".buckley", "ralph"), nil
}

// getProjectName returns a safe project name for organizing Ralph data.
// Uses the git repo name if in a git repo, otherwise the directory basename.
func getProjectName(workDir string) string {
	// Try to get git repo name
	if ralph.IsGitRepo(workDir) {
		if repoRoot, err := ralph.GetRepoRoot(workDir); err == nil {
			return filepath.Base(repoRoot)
		}
	}
	// Fall back to directory basename
	return filepath.Base(workDir)
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// ralphHeadlessRunner implements ralph.HeadlessRunner wrapping headless.Runner.
type ralphHeadlessRunner struct {
	runner    *headless.Runner
	store     *storage.Store
	sessionID string
}

type modelContextProvider struct {
	manager *model.Manager
}

func (p modelContextProvider) ContextLength(modelID string) int {
	if p.manager == nil {
		return 0
	}
	length, err := p.manager.GetContextLength(modelID)
	if err != nil {
		return 0
	}
	return length
}

func (r *ralphHeadlessRunner) ProcessInput(ctx context.Context, input string) error {
	if r == nil || r.runner == nil {
		return fmt.Errorf("runner not initialized")
	}
	return r.runner.HandleSessionCommand(command.SessionCommand{
		Type:    "input",
		Content: input,
	})
}

func (r *ralphHeadlessRunner) State() string {
	if r == nil || r.runner == nil {
		return "idle"
	}
	return string(r.runner.State())
}

func (r *ralphHeadlessRunner) SetModelOverride(modelID string) {
	if r == nil || r.runner == nil {
		return
	}
	r.runner.SetModelOverride(modelID)
}

func (r *ralphHeadlessRunner) WaitForIdle(ctx context.Context) error {
	if r == nil || r.runner == nil {
		return fmt.Errorf("runner not initialized")
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	// Wait for the runner to transition out of idle (start processing),
	// then wait for it to return to idle (finish processing).
	sawNonIdle := false
	maxWait := time.After(5 * time.Minute) // Safety timeout

	for {
		state := r.runner.State()
		switch state {
		case headless.StateIdle:
			if sawNonIdle {
				// Runner processed something and is now idle
				return nil
			}
			// Still waiting for processing to start
		case headless.StateProcessing:
			sawNonIdle = true
		case headless.StatePaused:
			return fmt.Errorf("runner paused")
		case headless.StateError:
			return fmt.Errorf("runner entered error state")
		case headless.StateStopped:
			return fmt.Errorf("runner stopped")
		default:
			sawNonIdle = true // Any other state counts as processing
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-maxWait:
			if sawNonIdle {
				return nil // Timed out but saw processing, treat as done
			}
			return fmt.Errorf("timeout waiting for runner to start processing")
		case <-ticker.C:
		}
	}
}

func (r *ralphHeadlessRunner) LatestAssistantMessageID(ctx context.Context) (int64, error) {
	_ = ctx
	if r == nil || r.store == nil {
		return 0, fmt.Errorf("store not initialized")
	}
	msg, err := r.store.GetLatestMessageByRole(r.sessionID, "assistant")
	if err != nil {
		return 0, err
	}
	if msg == nil {
		return 0, nil
	}
	return msg.ID, nil
}

func (r *ralphHeadlessRunner) LatestAssistantMessage(ctx context.Context, afterID int64) (string, int, int64, error) {
	_ = ctx
	if r == nil || r.store == nil {
		return "", 0, 0, fmt.Errorf("store not initialized")
	}
	msg, err := r.store.GetLatestMessageByRole(r.sessionID, "assistant")
	if err != nil {
		return "", 0, 0, err
	}
	if msg == nil || msg.ID <= afterID {
		return "", 0, 0, nil
	}
	content := msg.Content
	if strings.TrimSpace(content) == "" && strings.TrimSpace(msg.ContentJSON) != "" {
		content = msg.ContentJSON
	}
	return content, msg.Tokens, msg.ID, nil
}

func (r *ralphHeadlessRunner) Stop() {
	if r != nil && r.runner != nil {
		r.runner.Stop()
	}
}

// ralphEventEmitter bridges headless.RunnerEvent to ralph.Logger.
type ralphEventEmitter struct {
	logger *ralph.Logger
}

func (e *ralphEventEmitter) Emit(event headless.RunnerEvent) {
	if e == nil || e.logger == nil {
		return
	}

	switch event.Type {
	case headless.EventToolCallStarted:
		toolName, _ := event.Data["toolName"].(string)
		argsRaw, _ := event.Data["arguments"].(string)
		var args map[string]any
		if err := json.Unmarshal([]byte(argsRaw), &args); err != nil {
			args = map[string]any{"raw": argsRaw}
		}
		e.logger.LogToolCall(toolName, args)

	case headless.EventToolCallComplete:
		toolName, _ := event.Data["toolName"].(string)
		success, _ := event.Data["success"].(bool)
		output, _ := event.Data["output"].(string)
		if errMsg, ok := event.Data["error"].(string); ok && errMsg != "" {
			output = errMsg
		}
		e.logger.LogToolResult(toolName, success, output)
	}
}

// newRalphHeadlessRunner creates a headless runner configured for Ralph mode.
func newRalphHeadlessRunner(
	cfg *config.Config,
	mgr *model.Manager,
	store *storage.Store,
	registry *tool.Registry,
	logger *ralph.Logger,
	sessionID string,
	sandboxPath string,
	timeout time.Duration,
) (*ralphHeadlessRunner, error) {
	// Create storage session for the headless runner
	now := time.Now()
	session := &storage.Session{
		ID:          sessionID,
		ProjectPath: sandboxPath,
		CreatedAt:   now,
		LastActive:  now,
		Status:      storage.SessionStatusActive,
	}

	if err := store.CreateSession(session); err != nil {
		return nil, fmt.Errorf("creating session: %w", err)
	}

	// Create event emitter that logs to Ralph's logger
	emitter := &ralphEventEmitter{logger: logger}

	// Configure the runner
	runnerCfg := headless.RunnerConfig{
		Session:      session,
		ModelManager: mgr,
		Tools:        registry,
		Store:        store,
		Config:       cfg,
		Emitter:      emitter,
		MaxRuntime:   timeout,
	}

	runner, err := headless.NewRunner(runnerCfg)
	if err != nil {
		return nil, fmt.Errorf("creating headless runner: %w", err)
	}

	return &ralphHeadlessRunner{runner: runner, store: store, sessionID: sessionID}, nil
}

func runRalphCommand(args []string) error {
	fs := flag.NewFlagSet("ralph", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: buckley ralph [flags] [command]\n\n")
		fmt.Fprintf(os.Stderr, "Ralph is an autonomous execution mode for long-running tasks.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nCommands:\n")
		fmt.Fprintf(os.Stderr, "  list     List ralph sessions\n")
		fmt.Fprintf(os.Stderr, "  watch    Watch and react to file changes (stub)\n")
		fmt.Fprintf(os.Stderr, "  resume   Resume a previous session (stub)\n")
		fmt.Fprintf(os.Stderr, "  control  Manage Ralph control file settings\n")
	}

	prompt := fs.String("prompt", "", "Task prompt for Ralph to execute")
	promptFile := fs.String("prompt-file", "", "Read prompt from file (supports hot-reload)")
	dir := fs.String("dir", "", "Working directory (default: current directory)")
	timeout := fs.Duration("timeout", 0, "Maximum execution time (e.g., 30m, 1h)")
	maxIterations := fs.Int("max-iterations", 0, "Maximum number of iterations (0 = unlimited)")
	noRefine := fs.Bool("no-refine", false, "Skip prompt refinement phase")
	watch := fs.Bool("watch", false, "Watch prompt file for changes")
	model := fs.String("model", "", "Model to use for execution")
	autoCommit := fs.Bool("auto-commit", false, "Automatically commit changes after each iteration")
	createPR := fs.Bool("create-pr", false, "Create a PR when the session completes")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	remaining := fs.Args()
	if len(remaining) > 0 {
		switch remaining[0] {
		case "list":
			return runRalphList(remaining[1:])
		case "watch":
			fmt.Println("ralph watch: stub - not yet implemented")
			return nil
		case "resume":
			fmt.Println("ralph resume: stub - not yet implemented")
			return nil
		case "control":
			return runRalphControl(remaining[1:])
		}
	}

	// Validate prompt
	actualPrompt := *prompt
	if *promptFile != "" {
		content, err := os.ReadFile(*promptFile)
		if err != nil {
			return fmt.Errorf("reading prompt file: %w", err)
		}
		actualPrompt = string(content)
	}
	if actualPrompt == "" {
		return fmt.Errorf("either --prompt or --prompt-file is required")
	}

	// Determine working directory
	workDir := *dir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
	}

	if *watch {
		return fmt.Errorf("--watch not yet implemented")
	}

	// Initialize dependencies
	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		return fmt.Errorf("initializing dependencies: %w", err)
	}
	defer store.Close()

	// Apply model override if specified
	if *model != "" {
		cfg.Models.Execution = *model
	}

	controlPath := filepath.Join(workDir, "ralph-control.yaml")
	controlCfg, err := loadOrCreateControlConfig(controlPath)
	if err != nil {
		return err
	}
	if err := controlCfg.Validate(); err != nil {
		return fmt.Errorf("validating control config: %w", err)
	}
	if controlCfg.ContextProcessing.Enabled && strings.TrimSpace(controlCfg.ContextProcessing.Model) == "" {
		return fmt.Errorf("context_processing.model is required when context processing is enabled")
	}
	if controlCfg.Memory.Enabled && controlCfg.Memory.SummaryInterval > 0 && strings.TrimSpace(controlCfg.Memory.SummaryModel) == "" {
		return fmt.Errorf("memory.summary_model is required when summary_interval is set")
	}

	sessionID := uuid.New().String()[:8]

	// Get ralph data directory (~/.buckley/ralph/)
	ralphDataDir, err := getRalphDataDir()
	if err != nil {
		return fmt.Errorf("get ralph data directory: %w", err)
	}

	// Get project name for organizing data
	projectName := getProjectName(workDir)

	// Create run directory: ~/.buckley/ralph/projects/<project>/runs/<session>/
	runDir := filepath.Join(ralphDataDir, "projects", projectName, "runs", sessionID)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}

	// Setup sandbox in run directory
	sandboxPath := filepath.Join(runDir, "sandbox")
	var sandboxMgr *ralph.SandboxManager
	var repoRoot string
	var originalBranch string
	var ralphBranch string

	shouldPreserveSandbox := func() bool {
		if sandboxMgr == nil || sandboxPath == "" {
			return false
		}
		files, err := sandboxMgr.GetModifiedFiles(sandboxPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: preserving sandbox %s: %v\n", sandboxPath, err)
			return true
		}
		if len(files) == 0 {
			return false
		}
		sample := files
		if len(sample) > 10 {
			sample = sample[:10]
		}
		fmt.Fprintf(os.Stderr, "warning: preserving sandbox with uncommitted changes: %s\n", sandboxPath)
		fmt.Fprintf(os.Stderr, "warning: uncommitted files (%d): %s\n", len(files), strings.Join(sample, ", "))
		return true
	}
	if ralph.IsGitRepo(workDir) {
		var err error
		repoRoot, err = ralph.GetRepoRoot(workDir)
		if err != nil {
			return fmt.Errorf("get repo root: %w", err)
		}

		// Get current branch for PR creation
		originalBranch, err = ralph.GetCurrentBranch(repoRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not get current branch: %v\n", err)
		}

		sandboxMgr = ralph.NewSandboxManager(repoRoot)
		ralphBranch = fmt.Sprintf("ralph/%s", sessionID)
		if err := sandboxMgr.CreateWorktree(sandboxPath, ralphBranch); err != nil {
			return fmt.Errorf("create sandbox worktree: %w", err)
		}
		defer func() {
			if shouldPreserveSandbox() {
				return
			}
			if err := sandboxMgr.RemoveWorktree(sandboxPath); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to remove worktree: %v\n", err)
			}
		}()
	} else {
		sandboxMgr = ralph.NewSandboxManager(workDir)
		if err := sandboxMgr.CreateFreshDirectory(sandboxPath, true); err != nil {
			return fmt.Errorf("create sandbox directory: %w", err)
		}
		defer func() {
			if shouldPreserveSandbox() {
				return
			}
			if err := os.RemoveAll(sandboxPath); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to remove sandbox: %v\n", err)
			}
		}()
	}

	// Save prompt to run directory
	promptPath := filepath.Join(runDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(actualPrompt), 0644); err != nil {
		return fmt.Errorf("save prompt file: %w", err)
	}

	// Setup logger in run directory
	logPath := filepath.Join(runDir, "log.jsonl")
	logger, err := ralph.NewLogger(logPath)
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}
	defer logger.Close()

	var memoryStore *ralph.MemoryStore
	if controlCfg.Memory.Enabled {
		memoryPath := filepath.Join(runDir, "memory.db")
		memoryStore, err = ralph.NewMemoryStore(memoryPath)
		if err != nil {
			return fmt.Errorf("create memory store: %w", err)
		}
		defer memoryStore.Close()
		logger.SetEventSink(memoryStore)
	}

	// Determine max iterations (CLI flag takes precedence over config)
	effectiveMaxIterations := *maxIterations
	if effectiveMaxIterations == 0 && controlCfg.MaxIterations > 0 {
		effectiveMaxIterations = controlCfg.MaxIterations
	}

	// Create session with git workflow config
	session := ralph.NewSession(ralph.SessionConfig{
		SessionID:     sessionID,
		Prompt:        actualPrompt,
		PromptFile:    *promptFile,
		Sandbox:       sandboxPath,
		Timeout:       *timeout,
		MaxIterations: effectiveMaxIterations,
		NoRefine:      *noRefine,
		GitWorkflow: ralph.GitWorkflowConfig{
			AutoCommit:   *autoCommit,
			CreatePR:     *createPR,
			TargetBranch: originalBranch,
			RepoRoot:     repoRoot,
		},
	})

	projectCtx := ralph.BuildProjectContext(workDir)

	// Create tool registry configured for the sandbox
	registry := tool.NewRegistry()
	registry.SetWorkDir(sandboxPath)
	registry.ConfigureContainers(cfg, sandboxPath)
	registry.SetSandboxConfig(cfg.Sandbox.ToSandboxConfig(sandboxPath))
	registerMCPTools(cfg, registry)

	fileWatcher := filewatch.NewFileWatcher(100)
	fileWatcher.Subscribe("*", func(change filewatch.FileChange) {
		if logger != nil {
			logger.LogFileChange(change)
		}
		if session != nil && strings.TrimSpace(change.Path) != "" {
			session.AddModifiedFile(change.Path)
		}
	})
	registry.Use(tool.FileChangeTracking(fileWatcher))

	if memoryStore != nil {
		registry.Register(&builtin.SessionMemoryTool{
			Store:     memoryStore,
			SessionID: sessionID,
		})
	}

	// Create headless runner
	runner, err := newRalphHeadlessRunner(cfg, mgr, store, registry, logger, sessionID, sandboxPath, *timeout)
	if err != nil {
		return fmt.Errorf("creating headless runner: %w", err)
	}
	defer runner.Stop()

	backendRegistry := ralph.NewBackendRegistry()
	for name, backend := range controlCfg.Backends {
		if backend.Type == ralph.BackendTypeInternal {
			backendRegistry.Register(ralph.NewInternalBackend(name, runner, ralph.InternalOptions{
				PromptTemplate: backend.PromptTemplate,
			}))
		} else {
			backendRegistry.Register(ralph.NewExternalBackend(name, backend.Command, backend.Args, backend.Options))
		}
	}

	orchestrator := ralph.NewOrchestrator(backendRegistry, controlCfg)
	orchestrator.SetLogger(logger)
	orchestrator.SetContextProvider(modelContextProvider{manager: mgr})

	var contextProcessor *ralph.ContextProcessor
	if strings.TrimSpace(controlCfg.ContextProcessing.Model) != "" {
		maxTokens := controlCfg.ContextProcessing.MaxOutputTokens
		if maxTokens <= 0 {
			maxTokens = 500
		}
		contextProcessor = ralph.NewContextProcessor(mgr, controlCfg.ContextProcessing.Model, maxTokens)
	}

	var summaryGenerator *ralph.SummaryGenerator
	if strings.TrimSpace(controlCfg.Memory.SummaryModel) != "" {
		summaryGenerator = ralph.NewSummaryGenerator(mgr, controlCfg.Memory.SummaryModel, 500)
	}

	// Find or create commit backend for auto-commit
	var commitBackend ralph.Backend
	if *autoCommit {
		// Look for a commit backend in the config (buckley-commit, codex-commit, etc.)
		for name, bcfg := range controlCfg.Backends {
			if strings.Contains(name, "commit") && bcfg.Enabled {
				if b, ok := backendRegistry.Get(name); ok {
					commitBackend = b
					break
				}
			}
		}
		// Fall back to creating an internal commit backend if none found
		if commitBackend == nil {
			commitBackend = ralph.NewInternalBackend("auto-commit", runner, ralph.InternalOptions{
				PromptTemplate: `Stage and commit the files modified during this Ralph session.

Files to commit:
{session_files}

Create a concise commit message describing what was accomplished.
{prompt}`,
			})
		}
	}

	// Create session end handler for PR creation
	var sessionEndHandler func(ctx context.Context) error
	if *createPR && repoRoot != "" && ralphBranch != "" && originalBranch != "" {
		sessionEndHandler = func(ctx context.Context) error {
			fmt.Printf("\n[PR-CREATION] Pushing branch and creating PR...\n")

			// Push the branch
			if err := ralph.PushBranch(sandboxPath, ralphBranch, "origin"); err != nil {
				return fmt.Errorf("push branch: %w", err)
			}

			// Create PR
			title := fmt.Sprintf("Ralph session %s", sessionID)
			body := fmt.Sprintf("## Summary\n\nAutomated changes from Ralph session `%s`.\n\n**Original prompt:**\n```\n%s\n```\n\n---\n🤖 Generated by Ralph", sessionID, truncateString(actualPrompt, 500))
			prURL, err := ralph.CreatePR(sandboxPath, title, body, originalBranch)
			if err != nil {
				return fmt.Errorf("create PR: %w", err)
			}

			fmt.Printf("[PR-CREATION] PR created: %s\n", prURL)
			return nil
		}
	}

	// Create executor with progress feedback
	executorOpts := []ralph.ExecutorOption{
		ralph.WithProgressWriter(os.Stdout),
		ralph.WithOrchestrator(orchestrator),
		ralph.WithMemoryStore(memoryStore),
		ralph.WithContextProcessor(contextProcessor),
		ralph.WithSummaryGenerator(summaryGenerator),
		ralph.WithProjectContext(projectCtx),
	}
	if commitBackend != nil {
		executorOpts = append(executorOpts, ralph.WithCommitBackend(commitBackend))
	}
	if sessionEndHandler != nil {
		executorOpts = append(executorOpts, ralph.WithSessionEndHandler(sessionEndHandler))
	}
	executor := ralph.NewExecutor(session, runner, logger, executorOpts...)

	var controlWatcher *ralph.ControlWatcher
	var stopWatcher chan struct{}
	if _, err := os.Stat(controlPath); err == nil {
		controlWatcher = ralph.NewControlWatcher(controlPath, time.Second)
		if err := controlWatcher.Start(); err != nil {
			return fmt.Errorf("start control watcher: %w", err)
		}
		stopWatcher = make(chan struct{})
		updates := controlWatcher.Subscribe()
		go func() {
			for {
				select {
				case cfg := <-updates:
					if cfg == nil {
						continue
					}
					if err := cfg.Validate(); err != nil {
						if logger != nil {
							logger.LogError(0, "control_watcher", err)
						}
						continue
					}
					orchestrator.UpdateConfig(cfg)
				case <-stopWatcher:
					return
				}
			}
		}()
		defer controlWatcher.Stop()
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat control file: %w", err)
	}

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Fprintf(os.Stderr, "\nReceived interrupt, shutting down...\n")
		runner.Stop()
		cancel()
	}()

	if stopWatcher != nil {
		defer close(stopWatcher)
	}

	// Print startup info
	fmt.Printf("Ralph session %s starting\n", sessionID)
	fmt.Printf("  Run dir: %s\n", runDir)
	fmt.Printf("  Sandbox: %s\n", sandboxPath)
	if *timeout > 0 {
		fmt.Printf("  Timeout: %s\n", *timeout)
	}
	if effectiveMaxIterations > 0 {
		fmt.Printf("  Max iterations: %d\n", effectiveMaxIterations)
	}
	fmt.Println()

	// Run executor
	if err := executor.Run(ctx); err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	// Print completion summary
	stats := session.Stats()
	fmt.Printf("\nRalph session %s completed\n", sessionID)
	fmt.Printf("  Iterations: %d\n", stats.Iteration)
	fmt.Printf("  Tokens: %d\n", stats.TotalTokens)
	fmt.Printf("  Cost: $%.4f\n", stats.TotalCost)
	fmt.Printf("  Files modified: %d\n", stats.FilesModified)
	fmt.Printf("  Elapsed: %s\n", stats.Elapsed.Round(time.Second))

	return nil
}

// runRalphControl handles the 'ralph control' subcommand for managing control file settings.
func runRalphControl(args []string) error {
	fs := flag.NewFlagSet("ralph control", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: buckley ralph control [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Manage Ralph control file settings.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}

	pause := fs.Bool("pause", false, "Pause Ralph execution")
	resume := fs.Bool("resume", false, "Resume Ralph execution")
	status := fs.Bool("status", false, "Show current control file status")
	nextBackend := fs.String("next-backend", "", "Switch to specified backend")
	set := fs.String("set", "", "Set config value (KEY=VALUE, supports dot notation)")
	controlFile := fs.String("control-file", "ralph-control.yaml", "Path to control file")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	// Count mutually exclusive options
	optCount := 0
	if *pause {
		optCount++
	}
	if *resume {
		optCount++
	}
	if *status {
		optCount++
	}
	if *nextBackend != "" {
		optCount++
	}
	if *set != "" {
		optCount++
	}

	if optCount == 0 {
		fs.Usage()
		return fmt.Errorf("one of --pause, --resume, --status, --next-backend, or --set is required")
	}
	if optCount > 1 {
		return fmt.Errorf("only one of --pause, --resume, --status, --next-backend, or --set can be specified")
	}

	// Handle --status separately as it doesn't need to write
	if *status {
		return showControlStatus(*controlFile)
	}

	// Load or create control config
	cfg, err := loadOrCreateControlConfig(*controlFile)
	if err != nil {
		return err
	}

	// Apply the requested change
	switch {
	case *pause:
		cfg.Override.Paused = true
		fmt.Println("Ralph execution paused")
	case *resume:
		cfg.Override.Paused = false
		fmt.Println("Ralph execution resumed")
	case *nextBackend != "":
		cfg.Override.NextAction = *nextBackend
		fmt.Printf("Next backend set to: %s\n", *nextBackend)
	case *set != "":
		if err := setControlConfigValue(cfg, *set); err != nil {
			return fmt.Errorf("setting config value: %w", err)
		}
		fmt.Printf("Config updated: %s\n", *set)
	}

	// Write back to file
	return saveControlConfig(*controlFile, cfg)
}

// loadOrCreateControlConfig loads an existing control config or creates a default one.
func loadOrCreateControlConfig(path string) (*ralph.ControlConfig, error) {
	// Check if file exists first
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return defaultControlConfig(), nil
	}

	cfg, err := ralph.LoadControlConfig(path)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// defaultControlConfig returns a default control configuration.
func defaultControlConfig() *ralph.ControlConfig {
	return &ralph.ControlConfig{
		Backends: map[string]ralph.BackendConfig{
			"buckley": {
				Type:    "internal",
				Enabled: true,
			},
		},
		Mode: ralph.ModeSequential,
		Override: ralph.OverrideConfig{
			Paused: false,
		},
	}
}

// saveControlConfig writes the control config to a file.
func saveControlConfig(path string, cfg *ralph.ControlConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling control config: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing control config: %w", err)
	}
	return nil
}

// showControlStatus displays the current state of the control file.
func showControlStatus(path string) error {
	cfg, err := loadOrCreateControlConfig(path)
	if err != nil {
		return err
	}

	exists := true
	if _, err := os.Stat(path); os.IsNotExist(err) {
		exists = false
	}

	fmt.Println("Ralph Control Status")
	if exists {
		fmt.Printf("  Control file: %s\n", path)
	} else {
		fmt.Printf("  Control file: %s (not created, showing defaults)\n", path)
	}
	fmt.Printf("  Mode: %s\n", cfg.Mode)
	fmt.Printf("  Paused: %t\n", cfg.Override.Paused)

	if cfg.Override.NextAction != "" {
		fmt.Printf("  Next action: %s\n", cfg.Override.NextAction)
	}

	fmt.Println()
	fmt.Println("Backends:")
	for name, backend := range cfg.Backends {
		backendType := backend.Type
		if backendType == "" {
			backendType = "external"
		}
		status := "disabled"
		if backend.Enabled {
			status = "enabled"
		}
		fmt.Printf("  %s (%s): %s\n", name, backendType, status)
	}

	if len(cfg.Override.ActiveBackends) > 0 {
		fmt.Println()
		fmt.Printf("Active backends override: %v\n", cfg.Override.ActiveBackends)
	}

	return nil
}

// setControlConfigValue parses a KEY=VALUE string and sets the value in the config.
// Supports dot notation for nested values, e.g.:
//   - mode=parallel
//   - override.paused=true
//   - backends.claude.enabled=true
//   - backends.claude.options.model=haiku
func setControlConfigValue(cfg *ralph.ControlConfig, kv string) error {
	// Split on first '='
	idx := -1
	for i, c := range kv {
		if c == '=' {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("invalid format: expected KEY=VALUE, got %q", kv)
	}

	key := kv[:idx]
	value := kv[idx+1:]

	if key == "" {
		return fmt.Errorf("empty key in %q", kv)
	}

	// Parse the dot-separated path
	parts := splitDotPath(key)

	switch parts[0] {
	case "mode":
		if len(parts) != 1 {
			return fmt.Errorf("mode does not support nested keys")
		}
		cfg.Mode = value

	case "rotation":
		return setRotationValue(&cfg.Rotation, parts[1:], value)

	case "memory":
		return setMemoryValue(&cfg.Memory, parts[1:], value)

	case "context_processing":
		return setContextProcessingValue(&cfg.ContextProcessing, parts[1:], value)

	case "override":
		return setOverrideValue(&cfg.Override, parts[1:], value)

	case "backends":
		if len(parts) < 2 {
			return fmt.Errorf("backends requires at least a backend name")
		}
		return setBackendValue(cfg, parts[1], parts[2:], value)

	default:
		return fmt.Errorf("unknown top-level key: %q", parts[0])
	}

	return nil
}

// splitDotPath splits a dot-separated path into parts.
func splitDotPath(path string) []string {
	var parts []string
	var current string
	for _, c := range path {
		if c == '.' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

func splitCSV(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseBool(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "true" || value == "1" || value == "yes"
}

// setOverrideValue sets a value in the OverrideConfig.
func setOverrideValue(override *ralph.OverrideConfig, parts []string, value string) error {
	if len(parts) == 0 {
		return fmt.Errorf("override requires a field name")
	}

	switch parts[0] {
	case "paused":
		if len(parts) != 1 {
			return fmt.Errorf("paused does not support nested keys")
		}
		override.Paused = value == "true" || value == "1" || value == "yes"

	case "next_action":
		if len(parts) != 1 {
			return fmt.Errorf("next_action does not support nested keys")
		}
		override.NextAction = value

	case "backend_options":
		if len(parts) < 3 {
			return fmt.Errorf("backend_options requires backend.option path")
		}
		backendName := parts[1]
		optionName := parts[2]
		if override.BackendOptions == nil {
			override.BackendOptions = make(map[string]map[string]string)
		}
		if override.BackendOptions[backendName] == nil {
			override.BackendOptions[backendName] = make(map[string]string)
		}
		override.BackendOptions[backendName][optionName] = value

	default:
		return fmt.Errorf("unknown override field: %q", parts[0])
	}

	return nil
}

func setRotationValue(rotation *ralph.RotationConfig, parts []string, value string) error {
	if len(parts) == 0 {
		return fmt.Errorf("rotation requires a field name")
	}

	switch parts[0] {
	case "mode":
		if len(parts) != 1 {
			return fmt.Errorf("rotation.mode does not support nested keys")
		}
		rotation.Mode = value
	case "interval":
		if len(parts) != 1 {
			return fmt.Errorf("rotation.interval does not support nested keys")
		}
		interval, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid rotation interval: %w", err)
		}
		rotation.Interval = interval
	case "order":
		if len(parts) != 1 {
			return fmt.Errorf("rotation.order does not support nested keys")
		}
		rotation.Order = splitCSV(value)
	default:
		return fmt.Errorf("unknown rotation field: %q", parts[0])
	}

	return nil
}

func setMemoryValue(memory *ralph.MemoryConfig, parts []string, value string) error {
	if len(parts) == 0 {
		return fmt.Errorf("memory requires a field name")
	}

	switch parts[0] {
	case "enabled":
		if len(parts) != 1 {
			return fmt.Errorf("memory.enabled does not support nested keys")
		}
		memory.Enabled = parseBool(value)
	case "summary_interval":
		if len(parts) != 1 {
			return fmt.Errorf("memory.summary_interval does not support nested keys")
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid summary_interval: %w", err)
		}
		memory.SummaryInterval = parsed
	case "summary_model":
		if len(parts) != 1 {
			return fmt.Errorf("memory.summary_model does not support nested keys")
		}
		memory.SummaryModel = value
	case "retention_days":
		if len(parts) != 1 {
			return fmt.Errorf("memory.retention_days does not support nested keys")
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid retention_days: %w", err)
		}
		memory.RetentionDays = parsed
	case "max_raw_turns":
		if len(parts) != 1 {
			return fmt.Errorf("memory.max_raw_turns does not support nested keys")
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid max_raw_turns: %w", err)
		}
		memory.MaxRawTurns = parsed
	default:
		return fmt.Errorf("unknown memory field: %q", parts[0])
	}

	return nil
}

func setContextProcessingValue(cfg *ralph.ContextProcessingConfig, parts []string, value string) error {
	if len(parts) == 0 {
		return fmt.Errorf("context_processing requires a field name")
	}

	switch parts[0] {
	case "enabled":
		if len(parts) != 1 {
			return fmt.Errorf("context_processing.enabled does not support nested keys")
		}
		cfg.Enabled = parseBool(value)
	case "model":
		if len(parts) != 1 {
			return fmt.Errorf("context_processing.model does not support nested keys")
		}
		cfg.Model = value
	case "max_output_tokens":
		if len(parts) != 1 {
			return fmt.Errorf("context_processing.max_output_tokens does not support nested keys")
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid max_output_tokens: %w", err)
		}
		cfg.MaxOutputTokens = parsed
	case "budget_pct":
		if len(parts) != 1 {
			return fmt.Errorf("context_processing.budget_pct does not support nested keys")
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid budget_pct: %w", err)
		}
		cfg.BudgetPct = parsed
	default:
		return fmt.Errorf("unknown context_processing field: %q", parts[0])
	}

	return nil
}

// setBackendValue sets a value for a backend in the config.
func setBackendValue(cfg *ralph.ControlConfig, backendName string, parts []string, value string) error {
	if cfg.Backends == nil {
		cfg.Backends = make(map[string]ralph.BackendConfig)
	}

	backend, exists := cfg.Backends[backendName]
	if !exists {
		// Create new backend if it doesn't exist
		backend = ralph.BackendConfig{}
	}

	if len(parts) == 0 {
		return fmt.Errorf("backend %q requires a field name", backendName)
	}

	switch parts[0] {
	case "type":
		if len(parts) != 1 {
			return fmt.Errorf("type does not support nested keys")
		}
		backend.Type = value

	case "command":
		if len(parts) != 1 {
			return fmt.Errorf("command does not support nested keys")
		}
		backend.Command = value

	case "enabled":
		if len(parts) != 1 {
			return fmt.Errorf("enabled does not support nested keys")
		}
		backend.Enabled = value == "true" || value == "1" || value == "yes"

	case "options":
		if len(parts) < 2 {
			return fmt.Errorf("options requires an option name")
		}
		optionName := parts[1]
		if backend.Options == nil {
			backend.Options = make(map[string]string)
		}
		backend.Options[optionName] = value

	case "thresholds":
		if len(parts) < 2 {
			return fmt.Errorf("thresholds requires a field name")
		}
		if err := setBackendThreshold(&backend.Thresholds, parts[1:], value); err != nil {
			return err
		}

	case "models":
		if len(parts) < 2 {
			return fmt.Errorf("models requires a field name")
		}
		if err := setBackendModels(&backend.Models, parts[1:], value); err != nil {
			return err
		}

	default:
		return fmt.Errorf("unknown backend field: %q", parts[0])
	}

	cfg.Backends[backendName] = backend
	return nil
}

func setBackendThreshold(thresholds *ralph.BackendThresholds, parts []string, value string) error {
	switch parts[0] {
	case "max_requests_per_window":
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid max_requests_per_window: %w", err)
		}
		thresholds.MaxRequestsPerWindow = parsed
	case "max_cost_per_hour":
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return fmt.Errorf("invalid max_cost_per_hour: %w", err)
		}
		thresholds.MaxCostPerHour = parsed
	case "max_context_pct":
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid max_context_pct: %w", err)
		}
		thresholds.MaxContextPct = parsed
	case "max_consecutive_errors":
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid max_consecutive_errors: %w", err)
		}
		thresholds.MaxConsecutiveErrors = parsed
	default:
		return fmt.Errorf("unknown threshold field: %q", parts[0])
	}
	return nil
}

func setBackendModels(models *ralph.BackendModels, parts []string, value string) error {
	switch parts[0] {
	case "default":
		if len(parts) != 1 {
			return fmt.Errorf("models.default does not support nested keys")
		}
		models.Default = value
	case "rules":
		if len(parts) != 1 {
			return fmt.Errorf("models.rules does not support nested keys")
		}
		var rules []ralph.ModelRule
		if err := yaml.Unmarshal([]byte(value), &rules); err != nil {
			return fmt.Errorf("parsing model rules: %w", err)
		}
		models.Rules = rules
	default:
		return fmt.Errorf("unknown models field: %q", parts[0])
	}
	return nil
}

// runRalphList lists ralph sessions from log files.
func runRalphList(args []string) error {
	fs := flag.NewFlagSet("ralph list", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: buckley ralph list [flags]\n\n")
		fmt.Fprintf(os.Stderr, "List Ralph sessions from log files.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}

	logDir := fs.String("log-dir", "", "Directory containing ralph runs (overrides project detection)")
	project := fs.String("project", "", "Project name (default: current directory's project)")
	allProjects := fs.Bool("all-projects", false, "Show sessions from all projects")
	all := fs.Bool("all", false, "Show all sessions including completed")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	// Determine runs directory
	runsDir := *logDir
	if runsDir == "" {
		ralphDataDir, err := getRalphDataDir()
		if err != nil {
			return fmt.Errorf("get ralph data directory: %w", err)
		}

		if *allProjects {
			// List from all projects
			return listAllProjectSessions(ralphDataDir, *all)
		}

		// Get project name
		projectName := *project
		if projectName == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			projectName = getProjectName(cwd)
		}
		runsDir = filepath.Join(ralphDataDir, "projects", projectName, "runs")
	}

	// Check if directory exists
	if _, err := os.Stat(runsDir); os.IsNotExist(err) {
		fmt.Println("No ralph sessions found.")
		return nil
	}

	// List run directories
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return fmt.Errorf("reading runs directory: %w", err)
	}

	type sessionInfo struct {
		ID        string
		StartTime time.Time
		EndTime   time.Time
		Status    string
		Prompt    string
		Iters     int
		Cost      float64
	}

	var sessions []sessionInfo

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		sessionID := entry.Name()
		logPath := filepath.Join(runsDir, sessionID, "log.jsonl")

		// Check if log file exists
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			continue
		}

		info, err := parseSessionLog(logPath)
		if err != nil {
			continue // Skip unparseable files
		}

		info.ID = sessionID

		// Filter based on flags
		if !*all && info.Status == "completed" {
			continue
		}

		sessions = append(sessions, info)
	}

	if len(sessions) == 0 {
		fmt.Println("No ralph sessions found.")
		return nil
	}

	// Sort by start time (newest first)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})

	// Print header
	fmt.Printf("%-10s  %-12s  %-8s  %-6s  %-10s  %s\n",
		"SESSION", "STARTED", "STATUS", "ITERS", "COST", "PROMPT")
	fmt.Println(strings.Repeat("-", 80))

	// Print sessions
	for _, s := range sessions {
		prompt := s.Prompt
		if len(prompt) > 30 {
			prompt = prompt[:27] + "..."
		}
		prompt = strings.ReplaceAll(prompt, "\n", " ")

		fmt.Printf("%-10s  %-12s  %-8s  %-6d  $%-9.4f  %s\n",
			s.ID,
			s.StartTime.Format("01-02 15:04"),
			s.Status,
			s.Iters,
			s.Cost,
			prompt,
		)
	}

	return nil
}

// listAllProjectSessions lists sessions from all projects.
func listAllProjectSessions(ralphDataDir string, showAll bool) error {
	projectsDir := filepath.Join(ralphDataDir, "projects")
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		fmt.Println("No ralph sessions found.")
		return nil
	}

	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return fmt.Errorf("reading projects directory: %w", err)
	}

	type sessionInfo struct {
		Project   string
		ID        string
		StartTime time.Time
		Status    string
		Iters     int
		Cost      float64
		Prompt    string
	}

	var allSessions []sessionInfo

	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		projectName := proj.Name()
		runsDir := filepath.Join(projectsDir, projectName, "runs")

		entries, err := os.ReadDir(runsDir)
		if err != nil {
			continue // Skip projects without runs
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			sessionID := entry.Name()
			logPath := filepath.Join(runsDir, sessionID, "log.jsonl")

			// Check if log file exists
			if _, err := os.Stat(logPath); os.IsNotExist(err) {
				continue
			}

			info, err := parseSessionLog(logPath)
			if err != nil {
				continue
			}

			// Skip completed sessions unless --all is specified
			if !showAll && info.Status == "completed" {
				continue
			}

			allSessions = append(allSessions, sessionInfo{
				Project:   projectName,
				ID:        sessionID,
				StartTime: info.StartTime,
				Status:    info.Status,
				Iters:     info.Iters,
				Cost:      info.Cost,
				Prompt:    info.Prompt,
			})
		}
	}

	if len(allSessions) == 0 {
		fmt.Println("No ralph sessions found.")
		return nil
	}

	// Sort by start time (newest first)
	sort.Slice(allSessions, func(i, j int) bool {
		return allSessions[i].StartTime.After(allSessions[j].StartTime)
	})

	// Print header
	fmt.Printf("%-15s  %-10s  %-12s  %-8s  %-6s  %-10s  %s\n",
		"PROJECT", "SESSION", "STARTED", "STATUS", "ITERS", "COST", "PROMPT")
	fmt.Println(strings.Repeat("-", 100))

	// Print sessions
	for _, s := range allSessions {
		prompt := s.Prompt
		if len(prompt) > 25 {
			prompt = prompt[:22] + "..."
		}
		prompt = strings.ReplaceAll(prompt, "\n", " ")

		projectName := s.Project
		if len(projectName) > 15 {
			projectName = projectName[:12] + "..."
		}

		fmt.Printf("%-15s  %-10s  %-12s  %-8s  %-6d  $%-9.4f  %s\n",
			projectName,
			s.ID,
			s.StartTime.Format("01-02 15:04"),
			s.Status,
			s.Iters,
			s.Cost,
			prompt,
		)
	}

	return nil
}

// parseSessionLog reads a ralph log file and extracts session info.
func parseSessionLog(path string) (sessionInfo struct {
	ID        string
	StartTime time.Time
	EndTime   time.Time
	Status    string
	Prompt    string
	Iters     int
	Cost      float64
}, err error) {
	f, err := os.Open(path)
	if err != nil {
		return sessionInfo, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var evt ralph.LogEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			continue
		}

		switch evt.Event {
		case "session_start":
			sessionInfo.StartTime = evt.Timestamp
			if p, ok := evt.Data["prompt"].(string); ok {
				sessionInfo.Prompt = p
			}
			sessionInfo.Status = "running"
		case "session_end":
			sessionInfo.EndTime = evt.Timestamp
			if reason, ok := evt.Data["reason"].(string); ok {
				sessionInfo.Status = reason
			}
			if iters, ok := evt.Data["iterations"].(float64); ok {
				sessionInfo.Iters = int(iters)
			}
			if cost, ok := evt.Data["total_cost"].(float64); ok {
				sessionInfo.Cost = cost
			}
		case "iteration_end":
			sessionInfo.Iters = evt.Iteration
			if cost, ok := evt.Data["cost"].(float64); ok {
				sessionInfo.Cost = cost
			}
		}
	}

	return sessionInfo, scanner.Err()
}
