package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/odvcencio/buckley/pkg/ralph"
)

func runRalphExecution(
	actualPrompt string,
	promptFile string,
	workDir string,
	timeout time.Duration,
	maxIterations int,
	noRefine bool,
	modelOverride string,
	verifyCommand string,
	autoCommit bool,
	createPR bool,
) error {
	// Initialize dependencies.
	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		return fmt.Errorf("initializing dependencies: %w", err)
	}
	defer store.Close()

	// Apply model override if specified.
	if modelOverride != "" {
		cfg.Models.Execution = modelOverride
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

	// Get ralph data directory (~/.buckley/ralph/).
	ralphDataDir, err := getRalphDataDir()
	if err != nil {
		return fmt.Errorf("get ralph data directory: %w", err)
	}

	// Get project name for organizing data.
	projectName := getProjectName(workDir)

	// Create run directory: ~/.buckley/ralph/projects/<project>/runs/<session>/.
	runDir := filepath.Join(ralphDataDir, "projects", projectName, "runs", sessionID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}

	// Setup sandbox in run directory.
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
		repoRoot, err = ralph.GetRepoRoot(workDir)
		if err != nil {
			return fmt.Errorf("get repo root: %w", err)
		}

		// Get current branch for PR creation.
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

	// Save prompt to run directory.
	promptPath := filepath.Join(runDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(actualPrompt), 0o644); err != nil {
		return fmt.Errorf("save prompt file: %w", err)
	}

	// Setup logger in run directory.
	logPath := filepath.Join(runDir, "log.jsonl")
	logger, err := ralph.NewLogger(logPath)
	if err != nil {
		return fmt.Errorf("create logger: %w", err)
	}
	defer logger.Close()

	// Determine max iterations (CLI flag takes precedence over config).
	effectiveMaxIterations := maxIterations
	if effectiveMaxIterations == 0 && controlCfg.MaxIterations > 0 {
		effectiveMaxIterations = controlCfg.MaxIterations
	}

	// Create session with git workflow config.
	// Save verify command for resume.
	if verifyCommand != "" {
		verifyPath := filepath.Join(runDir, "verify.txt")
		if err := os.WriteFile(verifyPath, []byte(verifyCommand), 0o600); err != nil {
			return fmt.Errorf("save verify command: %w", err)
		}
	}

	session := ralph.NewSession(ralph.SessionConfig{
		SessionID:     sessionID,
		Prompt:        actualPrompt,
		PromptFile:    promptFile,
		Sandbox:       sandboxPath,
		Timeout:       timeout,
		MaxIterations: effectiveMaxIterations,
		NoRefine:      noRefine,
		VerifyCommand: verifyCommand,
		GitWorkflow: ralph.GitWorkflowConfig{
			AutoCommit:   autoCommit,
			CreatePR:     createPR,
			TargetBranch: originalBranch,
			RepoRoot:     repoRoot,
		},
	})

	// Build shared runtime (registry, runner, orchestrator, memory, etc.).
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
		timeout:     timeout,
	})
	if err != nil {
		return err
	}
	defer rt.Close()

	// Find or create commit backend for auto-commit.
	var commitBackend ralph.Backend
	if autoCommit {
		// Look for a commit backend in the config (buckley-commit, codex-commit, etc.).
		// Use suffix/exact match to avoid false positives (e.g. "uncommitted-check").
		for name, bcfg := range controlCfg.Backends {
			if isCommitBackendName(name) && bcfg.Enabled {
				if b, ok := rt.backendRegistry.Get(name); ok {
					commitBackend = b
					break
				}
			}
		}
		// Fall back to creating an internal commit backend if none found.
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

	// Create session end handler for PR creation.
	var sessionEndHandler func(ctx context.Context) error
	if createPR && repoRoot != "" && ralphBranch != "" && originalBranch != "" {
		sessionEndHandler = func(ctx context.Context) error {
			fmt.Printf("\n[PR-CREATION] Pushing branch and creating PR...\n")

			// Push the branch.
			if err := ralph.PushBranch(sandboxPath, ralphBranch, "origin"); err != nil {
				return fmt.Errorf("push branch: %w", err)
			}

			// Create PR.
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

	// Create executor with progress feedback.
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

	// Print startup info.
	fmt.Printf("Ralph session %s starting\n", sessionID)
	fmt.Printf("  Run dir: %s\n", runDir)
	fmt.Printf("  Sandbox: %s\n", sandboxPath)
	if timeout > 0 {
		fmt.Printf("  Timeout: %s\n", timeout)
	}
	if effectiveMaxIterations > 0 {
		fmt.Printf("  Max iterations: %d\n", effectiveMaxIterations)
	}
	fmt.Println()

	if stopWatcher != nil {
		defer close(stopWatcher)
	}

	// Run executor with signal handling.
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
