package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
)

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
		registry.ConfigureDockerSandbox(cfg, cwd)
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
		registry.ConfigureDockerSandbox(cfg, cwd)
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
		registry.ConfigureDockerSandbox(cfg, cwd)
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
	if cliFlags.encodingOverride != "" {
		cfg.Encoding.UseToon = cliFlags.encodingOverride != "json"
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
