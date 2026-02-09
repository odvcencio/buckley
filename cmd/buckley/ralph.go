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
				return fmt.Errorf("WaitForIdle: timed out after %v while processing", 5*time.Minute)
			}
			return fmt.Errorf("WaitForIdle: timed out after %v waiting for processing to start", 5*time.Minute)
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

// ralphRuntime holds the shared infrastructure components used by both
// runRalphCommand and runRalphResume. Call setupRalphRuntime to create one
// and Close when finished.
type ralphRuntime struct {
	cfg              *config.Config
	mgr              *model.Manager
	store            *storage.Store
	registry         *tool.Registry
	runner           *ralphHeadlessRunner
	backendRegistry  *ralph.BackendRegistry
	orchestrator     *ralph.Orchestrator
	memoryStore      *ralph.MemoryStore
	contextProcessor *ralph.ContextProcessor
	summaryGenerator *ralph.SummaryGenerator
	projectCtx       string
	logger           *ralph.Logger
}

// ralphRuntimeConfig contains the parameters needed to build a ralphRuntime.
type ralphRuntimeConfig struct {
	cfg         *config.Config
	mgr         *model.Manager
	store       *storage.Store
	controlCfg  *ralph.ControlConfig
	logger      *ralph.Logger
	session     *ralph.Session
	sessionID   string
	sandboxPath string
	workDir     string
	runDir      string
	timeout     time.Duration
}

// setupRalphRuntime creates the shared tool registry, headless runner,
// orchestrator, memory store, context processor, summary generator and
// project context that are identical between runRalphCommand and
// runRalphResume. The caller is responsible for calling Close.
func setupRalphRuntime(rc ralphRuntimeConfig) (*ralphRuntime, error) {
	rt := &ralphRuntime{
		cfg:   rc.cfg,
		mgr:   rc.mgr,
		store: rc.store,
	}

	// Tool registry
	rt.registry = tool.NewRegistry()
	rt.registry.SetWorkDir(rc.sandboxPath)
	rt.registry.ConfigureContainers(rc.cfg, rc.sandboxPath)
	rt.registry.ConfigureDockerSandbox(rc.cfg, rc.sandboxPath)
	rt.registry.SetSandboxConfig(rc.cfg.Sandbox.ToSandboxConfig(rc.sandboxPath))
	registerMCPTools(rc.cfg, rt.registry)

	fileWatcher := filewatch.NewFileWatcher(100)
	fileWatcher.Subscribe("*", func(change filewatch.FileChange) {
		if rc.logger != nil {
			rc.logger.LogFileChange(change)
		}
		if rc.session != nil && strings.TrimSpace(change.Path) != "" {
			rc.session.AddModifiedFile(change.Path)
		}
	})
	rt.registry.Use(tool.FileChangeTracking(fileWatcher))

	// Memory store
	if rc.controlCfg.Memory.Enabled {
		memoryPath := filepath.Join(rc.runDir, "memory.db")
		ms, err := ralph.NewMemoryStore(memoryPath)
		if err != nil {
			return nil, fmt.Errorf("create memory store: %w", err)
		}
		rt.memoryStore = ms
		rc.logger.SetEventSink(ms)
	}

	if rt.memoryStore != nil {
		rt.registry.Register(&builtin.SessionMemoryTool{
			Store:     rt.memoryStore,
			SessionID: rc.sessionID,
		})
	}

	// Headless runner
	runner, err := newRalphHeadlessRunner(rc.cfg, rc.mgr, rc.store, rt.registry, rc.logger, rc.sessionID, rc.sandboxPath, rc.timeout)
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("creating headless runner: %w", err)
	}
	rt.runner = runner

	// Backend registry + orchestrator
	backendRegistry := ralph.NewBackendRegistry()
	for name, backend := range rc.controlCfg.Backends {
		if backend.Type == ralph.BackendTypeInternal {
			backendRegistry.Register(ralph.NewInternalBackend(name, runner, ralph.InternalOptions{
				PromptTemplate: backend.PromptTemplate,
			}))
		} else {
			backendRegistry.Register(ralph.NewExternalBackend(name, backend.Command, backend.Args, backend.Options))
		}
	}

	rt.backendRegistry = backendRegistry
	rt.orchestrator = ralph.NewOrchestrator(backendRegistry, rc.controlCfg)
	rt.orchestrator.SetLogger(rc.logger)
	rt.orchestrator.SetContextProvider(modelContextProvider{manager: rc.mgr})

	// Context processor
	if strings.TrimSpace(rc.controlCfg.ContextProcessing.Model) != "" {
		maxTokens := rc.controlCfg.ContextProcessing.MaxOutputTokens
		if maxTokens <= 0 {
			maxTokens = 500
		}
		rt.contextProcessor = ralph.NewContextProcessor(rc.mgr, rc.controlCfg.ContextProcessing.Model, maxTokens)
	}

	// Summary generator
	if strings.TrimSpace(rc.controlCfg.Memory.SummaryModel) != "" {
		rt.summaryGenerator = ralph.NewSummaryGenerator(rc.mgr, rc.controlCfg.Memory.SummaryModel, 500)
	}

	// Project context
	rt.projectCtx = ralph.BuildProjectContext(rc.workDir)
	rt.logger = rc.logger

	return rt, nil
}

// baseExecutorOpts returns the common executor options shared by both
// runRalphCommand and runRalphResume.
func (rt *ralphRuntime) baseExecutorOpts() []ralph.ExecutorOption {
	return []ralph.ExecutorOption{
		ralph.WithProgressWriter(os.Stdout),
		ralph.WithOrchestrator(rt.orchestrator),
		ralph.WithMemoryStore(rt.memoryStore),
		ralph.WithContextProcessor(rt.contextProcessor),
		ralph.WithSummaryGenerator(rt.summaryGenerator),
		ralph.WithProjectContext(rt.projectCtx),
	}
}

// runWithSignalHandler creates a cancellable context, registers signal
// handling, runs fn, and prints a completion summary.
func (rt *ralphRuntime) runWithSignalHandler(fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	go func() {
		<-sigChan
		fmt.Fprintf(os.Stderr, "\nReceived interrupt, shutting down...\n")
		rt.runner.Stop()
		cancel()
	}()

	return fn(ctx)
}

// Close releases resources owned by the runtime.
func (rt *ralphRuntime) Close() {
	if rt.runner != nil {
		rt.runner.Stop()
	}
	if rt.memoryStore != nil {
		rt.memoryStore.Close()
	}
}

// printSessionStats prints the standard completion summary.
func printSessionStats(label string, stats ralph.SessionStats) {
	fmt.Printf("\n%s\n", label)
	fmt.Printf("  Iterations: %d\n", stats.Iteration)
	fmt.Printf("  Tokens: %d\n", stats.TotalTokens)
	fmt.Printf("  Cost: $%.4f\n", stats.TotalCost)
	fmt.Printf("  Files modified: %d\n", stats.FilesModified)
	fmt.Printf("  Elapsed: %s\n", stats.Elapsed.Round(time.Second))
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
		fmt.Fprintf(os.Stderr, "  resume   Resume a previous session\n")
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
		case "resume":
			return runRalphResume(remaining[1:])
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
		if *promptFile == "" {
			return fmt.Errorf("--watch requires --prompt-file")
		}
		return runRalphWatch(watchOptions{
			promptFile:    *promptFile,
			workDir:       workDir,
			dirFlag:       *dir,
			timeout:       *timeout,
			maxIterations: *maxIterations,
			noRefine:      *noRefine,
			modelOverride: *model,
			autoCommit:    *autoCommit,
			createPR:      *createPR,
		})
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
	if err := os.MkdirAll(runDir, 0o755); err != nil {
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
	if err := os.WriteFile(promptPath, []byte(actualPrompt), 0o644); err != nil {
		return fmt.Errorf("save prompt file: %w", err)
	}

	// Setup logger in run directory
	logPath := filepath.Join(runDir, "log.jsonl")
	logger, err := ralph.NewLogger(logPath)
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}
	defer logger.Close()

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

	// Build shared runtime (registry, runner, orchestrator, memory, etc.)
	rt, err := setupRalphRuntime(ralphRuntimeConfig{
		cfg:         cfg,
		mgr:         mgr,
		store:       store,
		controlCfg:  controlCfg,
		logger:      logger,
		session:     session,
		sessionID:   sessionID,
		sandboxPath: sandboxPath,
		workDir:     workDir,
		runDir:      runDir,
		timeout:     *timeout,
	})
	if err != nil {
		return err
	}
	defer rt.Close()

	// Find or create commit backend for auto-commit
	var commitBackend ralph.Backend
	if *autoCommit {
		// Look for a commit backend in the config (buckley-commit, codex-commit, etc.)
		// Use suffix/exact match to avoid false positives (e.g. "uncommitted-check").
		for name, bcfg := range controlCfg.Backends {
			if isCommitBackendName(name) && bcfg.Enabled {
				if b, ok := rt.backendRegistry.Get(name); ok {
					commitBackend = b
					break
				}
			}
		}
		// Fall back to creating an internal commit backend if none found
		if commitBackend == nil {
			commitBackend = ralph.NewInternalBackend("auto-commit", rt.runner, ralph.InternalOptions{
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
	executorOpts := rt.baseExecutorOpts()
	if commitBackend != nil {
		executorOpts = append(executorOpts, ralph.WithCommitBackend(commitBackend))
	}
	if sessionEndHandler != nil {
		executorOpts = append(executorOpts, ralph.WithSessionEndHandler(sessionEndHandler))
	}
	executor := ralph.NewExecutor(session, rt.runner, logger, executorOpts...)

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
					rt.orchestrator.UpdateConfig(cfg)
				case <-stopWatcher:
					return
				}
			}
		}()
		defer controlWatcher.Stop()
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat control file: %w", err)
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

	if stopWatcher != nil {
		defer close(stopWatcher)
	}

	// Run executor with signal handling
	if err := rt.runWithSignalHandler(func(ctx context.Context) error {
		return executor.Run(ctx)
	}); err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	printSessionStats(
		fmt.Sprintf("Ralph session %s completed", sessionID),
		session.Stats(),
	)

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
	if err := os.WriteFile(path, data, 0o644); err != nil {
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
func parseSessionLog(path string) (sessionInfo, error) {
	var info sessionInfo
	f, err := os.Open(path)
	if err != nil {
		return info, err
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
			info.StartTime = evt.Timestamp
			if p, ok := evt.Data["prompt"].(string); ok {
				info.Prompt = p
			}
			info.Status = "running"
		case "session_end":
			info.EndTime = evt.Timestamp
			if reason, ok := evt.Data["reason"].(string); ok {
				info.Status = reason
			}
			if iters, ok := evt.Data["iterations"].(float64); ok {
				info.Iters = int(iters)
			}
			if cost, ok := evt.Data["total_cost"].(float64); ok {
				info.Cost = cost
			}
		case "iteration_end":
			info.Iters = evt.Iteration
			// The executor writes "session_total_cost"; fall back to "cost"
			if cost, ok := evt.Data["session_total_cost"].(float64); ok {
				info.Cost = cost
			} else if cost, ok := evt.Data["cost"].(float64); ok {
				info.Cost = cost
			}
		}
	}

	return info, scanner.Err()
}

// watchOptions bundles parameters for runRalphWatch.
type watchOptions struct {
	promptFile    string
	workDir       string
	dirFlag       string
	timeout       time.Duration
	maxIterations int
	noRefine      bool
	modelOverride string
	autoCommit    bool
	createPR      bool
}

// runRalphWatch polls a prompt file for changes and restarts ralph on each change.
func runRalphWatch(opts watchOptions) error {
	fmt.Printf("Watching %s for changes (Ctrl+C to stop)\n\n", opts.promptFile)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)
	go func() {
		<-sigChan
		fmt.Fprintf(os.Stderr, "\nStopping watch...\n")
		cancel()
	}()

	lastHash := hashFileContents(opts.promptFile)
	iteration := 0

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}

		currentHash := hashFileContents(opts.promptFile)
		if currentHash == lastHash && iteration > 0 {
			continue
		}
		lastHash = currentHash
		iteration++

		// Read the current prompt
		content, err := os.ReadFile(opts.promptFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: reading prompt file: %v\n", err)
			continue
		}
		prompt := strings.TrimSpace(string(content))
		if prompt == "" {
			continue
		}

		fmt.Printf("[watch] iteration %d: prompt file changed, starting ralph...\n", iteration)

		// Build args for a normal ralph run
		args := []string{"--prompt", prompt}
		if opts.dirFlag != "" {
			args = append(args, "--dir", opts.dirFlag)
		}
		if opts.timeout > 0 {
			args = append(args, "--timeout", opts.timeout.String())
		}
		if opts.maxIterations > 0 {
			args = append(args, "--max-iterations", strconv.Itoa(opts.maxIterations))
		}
		if opts.noRefine {
			args = append(args, "--no-refine")
		}
		if opts.modelOverride != "" {
			args = append(args, "--model", opts.modelOverride)
		}
		if opts.autoCommit {
			args = append(args, "--auto-commit")
		}
		if opts.createPR {
			args = append(args, "--create-pr")
		}

		if err := runRalphCommand(args); err != nil {
			fmt.Fprintf(os.Stderr, "[watch] ralph run failed: %v\n", err)
		}

		fmt.Printf("[watch] waiting for changes to %s...\n", opts.promptFile)
	}
}

// hashFileContents returns a simple hash of a file's content for change detection.
func hashFileContents(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	// Use mtime + size as a fast change detector (same approach as ControlWatcher)
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
}

// runRalphResume resumes a previous ralph session from where it left off.
func runRalphResume(args []string) error {
	fs := flag.NewFlagSet("ralph resume", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: buckley ralph resume <session-id> [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Resume a previous Ralph session.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}

	project := fs.String("project", "", "Project name (default: auto-detect from cwd)")
	modelOverride := fs.String("model", "", "Override model for resumed session")
	extraIters := fs.Int("max-iterations", 0, "Additional iterations to run (0 = same as original)")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	remaining := fs.Args()
	if len(remaining) == 0 {
		return fmt.Errorf("session ID is required (use 'buckley ralph list --all' to see sessions)")
	}
	sessionID := remaining[0]

	// Find the session's run directory
	ralphDataDir, err := getRalphDataDir()
	if err != nil {
		return fmt.Errorf("get ralph data directory: %w", err)
	}

	projectName := *project
	if projectName == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
		projectName = getProjectName(cwd)
	}

	runDir := filepath.Join(ralphDataDir, "projects", projectName, "runs", sessionID)
	if _, err := os.Stat(runDir); os.IsNotExist(err) {
		// Try scanning all projects
		runDir, projectName, err = findSessionRunDir(ralphDataDir, sessionID)
		if err != nil {
			return fmt.Errorf("session %s not found: %w", sessionID, err)
		}
	}

	// Parse the session log to get state
	logPath := filepath.Join(runDir, "log.jsonl")
	info, err := parseSessionLog(logPath)
	if err != nil {
		return fmt.Errorf("parsing session log: %w", err)
	}

	// Read the original prompt
	promptPath := filepath.Join(runDir, "prompt.md")
	promptData, err := os.ReadFile(promptPath)
	if err != nil {
		return fmt.Errorf("reading prompt: %w", err)
	}
	prompt := string(promptData)

	// Check if sandbox still exists
	sandboxPath := filepath.Join(runDir, "sandbox")
	sandboxExists := false
	if stat, err := os.Stat(sandboxPath); err == nil && stat.IsDir() {
		sandboxExists = true
	}

	fmt.Printf("Resuming Ralph session %s\n", sessionID)
	fmt.Printf("  Project:    %s\n", projectName)
	fmt.Printf("  Status:     %s\n", info.Status)
	fmt.Printf("  Iterations: %d completed\n", info.Iters)
	fmt.Printf("  Cost:       $%.4f\n", info.Cost)
	if sandboxExists {
		fmt.Printf("  Sandbox:    %s (exists)\n", sandboxPath)
	} else {
		fmt.Printf("  Sandbox:    %s (will recreate)\n", sandboxPath)
	}
	fmt.Println()

	// Initialize dependencies
	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		return fmt.Errorf("initializing dependencies: %w", err)
	}
	defer store.Close()

	if *modelOverride != "" {
		cfg.Models.Execution = *modelOverride
	}

	// Recreate sandbox if needed
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}

	if !sandboxExists {
		if ralph.IsGitRepo(workDir) {
			repoRoot, err := ralph.GetRepoRoot(workDir)
			if err != nil {
				return fmt.Errorf("get repo root: %w", err)
			}
			sandboxMgr := ralph.NewSandboxManager(repoRoot)
			ralphBranch := fmt.Sprintf("ralph/%s", sessionID)
			if err := sandboxMgr.CreateWorktree(sandboxPath, ralphBranch); err != nil {
				return fmt.Errorf("recreate sandbox worktree: %w", err)
			}
			defer func() {
				files, _ := sandboxMgr.GetModifiedFiles(sandboxPath)
				if len(files) > 0 {
					fmt.Fprintf(os.Stderr, "warning: preserving sandbox with uncommitted changes: %s\n", sandboxPath)
					return
				}
				if err := sandboxMgr.RemoveWorktree(sandboxPath); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to remove worktree: %v\n", err)
				}
			}()
		} else {
			// Non-git project: create sandbox as a plain directory
			if err := os.MkdirAll(sandboxPath, 0o755); err != nil {
				return fmt.Errorf("recreate sandbox directory: %w", err)
			}
		}
	}

	// Setup logger (append to existing log)
	logger, err := ralph.NewLogger(logPath)
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}
	defer logger.Close()

	// Determine iterations
	maxIterations := *extraIters

	// Create a new session with resumed state
	resumeID := uuid.New().String()[:8]
	session := ralph.NewSession(ralph.SessionConfig{
		SessionID:     resumeID,
		Prompt:        prompt,
		Sandbox:       sandboxPath,
		MaxIterations: maxIterations,
	})

	// Restore cost/iteration counters from the original session
	session.AddTokens(0, info.Cost)
	for i := 0; i < info.Iters; i++ {
		session.IncrementIteration()
	}

	controlPath := filepath.Join(workDir, "ralph-control.yaml")
	controlCfg, err := loadOrCreateControlConfig(controlPath)
	if err != nil {
		return err
	}

	// Build shared runtime (registry, runner, orchestrator, memory, etc.)
	rt, err := setupRalphRuntime(ralphRuntimeConfig{
		cfg:         cfg,
		mgr:         mgr,
		store:       store,
		controlCfg:  controlCfg,
		logger:      logger,
		session:     session,
		sessionID:   resumeID,
		sandboxPath: sandboxPath,
		workDir:     workDir,
		runDir:      runDir,
		timeout:     0,
	})
	if err != nil {
		return err
	}
	defer rt.Close()

	executor := ralph.NewExecutor(session, rt.runner, logger, rt.baseExecutorOpts()...)

	fmt.Printf("Resumed as session %s (continuing from iteration %d)\n\n", resumeID, info.Iters)

	if err := rt.runWithSignalHandler(func(ctx context.Context) error {
		return executor.Run(ctx)
	}); err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}

	printSessionStats(
		fmt.Sprintf("Ralph session %s completed (resumed from %s)", resumeID, sessionID),
		session.Stats(),
	)

	return nil
}

// findSessionRunDir searches all projects for a session ID.
func findSessionRunDir(ralphDataDir, sessionID string) (string, string, error) {
	projectsDir := filepath.Join(ralphDataDir, "projects")
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return "", "", fmt.Errorf("reading projects: %w", err)
	}

	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		runDir := filepath.Join(projectsDir, proj.Name(), "runs", sessionID)
		if _, err := os.Stat(runDir); err == nil {
			return runDir, proj.Name(), nil
		}
	}

	return "", "", fmt.Errorf("session %s not found in any project", sessionID)
}

// isCommitBackendName returns true if the backend name identifies a commit
// backend. It checks for exact match ("commit") or a "-commit" suffix to
// avoid false positives from names that merely contain "commit" as a substring.
func isCommitBackendName(name string) bool {
	return name == "commit" || strings.HasSuffix(name, "-commit")
}
