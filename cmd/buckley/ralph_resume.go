package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/odvcencio/buckley/pkg/ralph"
)

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
	verifyOverride := fs.String("verify", "", "Command to run after each iteration for verification")

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
	// Resolve verify command: CLI flag takes precedence, then saved value
	verifyCmd := *verifyOverride
	if verifyCmd == "" {
		if data, err := os.ReadFile(filepath.Join(runDir, "verify.txt")); err == nil {
			verifyCmd = strings.TrimSpace(string(data))
		}
	}

	resumeID := uuid.New().String()[:8]
	session := ralph.NewSession(ralph.SessionConfig{
		SessionID:     resumeID,
		Prompt:        prompt,
		Sandbox:       sandboxPath,
		MaxIterations: maxIterations,
		VerifyCommand: verifyCmd,
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
