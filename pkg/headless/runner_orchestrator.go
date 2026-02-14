package headless

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
)

func (r *Runner) ensureOrchestrator() (*orchestrator.Orchestrator, *orchestrator.WorkflowManager, error) {
	r.mu.RLock()
	if r.orchestrator != nil && r.workflow != nil {
		orch := r.orchestrator
		wf := r.workflow
		r.mu.RUnlock()
		return orch, wf, nil
	}
	r.mu.RUnlock()

	if r.modelManager == nil {
		return nil, nil, fmt.Errorf("model manager not configured")
	}

	projectRoot := ""
	if r.session != nil {
		projectRoot = strings.TrimSpace(r.session.ProjectPath)
		if projectRoot == "" {
			projectRoot = strings.TrimSpace(r.session.GitRepo)
		}
	}
	if projectRoot != "" {
		if abs, err := filepath.Abs(projectRoot); err == nil {
			projectRoot = abs
		}
		projectRoot = filepath.Clean(projectRoot)
	}

	cfg := r.config
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	// Ensure any relative artifact paths resolve within the session project.
	cfg = resolveSessionConfig(cfg, r.session)
	docsRoot := docsRootFromConfig(cfg)

	wf := orchestrator.NewWorkflowManager(cfg, r.modelManager, r.tools, r.store, docsRoot, projectRoot, r.telemetry)
	wf.SetSessionID(r.sessionID)
	if err := wf.InitializeDocumentation(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize docs hierarchy: %v\n", err)
	}

	orch := orchestrator.NewOrchestrator(r.store, r.modelManager, r.tools, cfg, wf, nil)

	r.mu.Lock()
	r.workflow = wf
	r.orchestrator = orch
	r.mu.Unlock()

	return orch, wf, nil
}

func docsRootFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return "docs"
	}
	planDir := strings.TrimSpace(cfg.Artifacts.PlanningDir)
	if planDir == "" {
		planDir = filepath.Join("docs", "plans")
	}
	return filepath.Dir(planDir)
}

func resolveSessionConfig(cfg *config.Config, sess *storage.Session) *config.Config {
	if cfg == nil {
		return config.DefaultConfig()
	}
	projectRoot := ""
	if sess != nil {
		projectRoot = strings.TrimSpace(sess.ProjectPath)
		if projectRoot == "" {
			projectRoot = strings.TrimSpace(sess.GitRepo)
		}
	}
	next := *cfg
	if strings.TrimSpace(projectRoot) == "" {
		return &next
	}
	if abs, err := filepath.Abs(projectRoot); err == nil {
		projectRoot = abs
	}
	projectRoot = filepath.Clean(projectRoot)

	resolve := func(path string) string {
		path = strings.TrimSpace(path)
		if path == "" || filepath.IsAbs(path) {
			return path
		}
		return filepath.Clean(filepath.Join(projectRoot, path))
	}

	next.Artifacts.PlanningDir = resolve(next.Artifacts.PlanningDir)
	next.Artifacts.ExecutionDir = resolve(next.Artifacts.ExecutionDir)
	next.Artifacts.ReviewDir = resolve(next.Artifacts.ReviewDir)
	next.Artifacts.ArchiveDir = resolve(next.Artifacts.ArchiveDir)
	return &next
}

func (r *Runner) runPlanCommand(args []string) error {
	orch, _, err := r.ensureOrchestrator()
	if err != nil {
		return fmt.Errorf("initializing orchestrator for plan: %w", err)
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: /plan <feature-name> <description>")
	}

	featureName := args[0]
	description := strings.Join(args[1:], " ")

	r.setState(StateProcessing)
	defer func() {
		if r.State() == StateProcessing {
			r.setState(StateIdle)
		}
	}()

	_ = r.persistSystemMessage(fmt.Sprintf("⏳ Planning %q…", featureName))

	plan, err := orch.PlanFeatureWithContext(r.baseContext(), featureName, description)
	if err != nil {
		if handled := r.handleWorkflowPause(err); handled {
			return nil
		}
		return fmt.Errorf("creating feature plan: %w", err)
	}

	summary := formatPlanSummary(plan, r.config)
	summary += "\nPlan created. Use /execute to start implementation or /status to inspect details."
	return r.persistSystemMessage(summary)
}

func (r *Runner) runExecuteCommand(args []string) error {
	orch, _, err := r.ensureOrchestrator()
	if err != nil {
		return fmt.Errorf("initializing orchestrator for execute: %w", err)
	}

	if orch.GetCurrentPlan() == nil {
		return fmt.Errorf("no active plan. Use /plan to create one or /resume <plan-id> to load an existing plan")
	}

	r.setState(StateProcessing)
	defer func() {
		if r.State() != StatePaused {
			r.setState(StateIdle)
		}
	}()

	_ = r.persistSystemMessage("⏳ Executing…")

	if len(args) > 0 {
		taskID := args[0]
		if err := orch.ExecuteTaskWithContext(r.baseContext(), taskID); err != nil {
			if handled := r.handleWorkflowPause(err); handled {
				return nil
			}
			return fmt.Errorf("executing task %s: %w", taskID, err)
		}
		return r.persistSystemMessage(fmt.Sprintf("✓ Task %s completed.", taskID))
	}

	if err := orch.ExecutePlanWithContext(r.baseContext()); err != nil {
		if handled := r.handleWorkflowPause(err); handled {
			return nil
		}
		return fmt.Errorf("executing plan: %w", err)
	}
	return r.persistSystemMessage("✓ Plan execution completed.")
}

func (r *Runner) runStatusCommand() error {
	orch, wf, err := r.ensureOrchestrator()
	if err != nil {
		return fmt.Errorf("initializing orchestrator for status: %w", err)
	}
	plan := orch.GetCurrentPlan()
	if plan == nil {
		return r.persistSystemMessage("No active plan. Use /plan to create one or /resume <plan-id> to load an existing plan.")
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Plan: %s\n\n", plan.FeatureName))
	b.WriteString(fmt.Sprintf("Plan ID: %s\n", plan.ID))
	b.WriteString(fmt.Sprintf("Created: %s\n", plan.CreatedAt.Format("2006-01-02 15:04")))

	completed := 0
	total := len(plan.Tasks)
	for _, task := range plan.Tasks {
		if task.Status == orchestrator.TaskCompleted {
			completed++
		}
	}
	percent := 0.0
	if total > 0 {
		percent = float64(completed) / float64(total) * 100
	}
	b.WriteString(fmt.Sprintf("Progress: %d/%d tasks completed (%.0f%%)\n\n", completed, total, percent))

	b.WriteString("Tasks:\n")
	for i, task := range plan.Tasks {
		status := planTaskStatus(task.Status)
		b.WriteString(fmt.Sprintf("  %s %d. %s\n", status, i+1, task.Title))
	}

	if wf != nil {
		b.WriteString("\nWorkflow:\n")
		phase := string(wf.GetCurrentPhase())
		if phase == "" {
			phase = "unknown"
		}
		b.WriteString(fmt.Sprintf("  Phase: %s\n", phase))
		agent := wf.GetActiveAgent()
		if agent == "" {
			agent = "N/A"
		}
		b.WriteString(fmt.Sprintf("  Active Agent: %s\n", agent))
		if paused, reason, question, at := wf.GetPauseInfo(); paused {
			if reason == "" {
				reason = "Awaiting user input"
			}
			if question == "" {
				question = "Confirm next steps"
			}
			when := ""
			if !at.IsZero() {
				when = fmt.Sprintf(" (since %s)", at.Format("15:04:05"))
			}
			b.WriteString(fmt.Sprintf("  Status: PAUSED%s\n", when))
			b.WriteString(fmt.Sprintf("    Reason: %s\n", reason))
			b.WriteString(fmt.Sprintf("    Action: %s\n", question))
		}
	}

	return r.persistSystemMessage(b.String())
}

func (r *Runner) runPlansCommand() error {
	orch, _, err := r.ensureOrchestrator()
	if err != nil {
		return fmt.Errorf("initializing orchestrator for plans list: %w", err)
	}

	plans, err := orch.ListPlans()
	if err != nil {
		return fmt.Errorf("listing plans: %w", err)
	}
	if len(plans) == 0 {
		return r.persistSystemMessage("No saved plans found. Use /plan to create one.")
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Saved Plans (%d):\n\n", len(plans)))
	for _, plan := range plans {
		completed := 0
		for _, task := range plan.Tasks {
			if task.Status == orchestrator.TaskCompleted {
				completed++
			}
		}
		b.WriteString(fmt.Sprintf("  %s\n", plan.ID))
		b.WriteString(fmt.Sprintf("    Feature: %s\n", plan.FeatureName))
		b.WriteString(fmt.Sprintf("    Created: %s\n", plan.CreatedAt.Format("2006-01-02 15:04")))
		b.WriteString(fmt.Sprintf("    Progress: %d/%d tasks\n", completed, len(plan.Tasks)))
		b.WriteString("\n")
	}
	b.WriteString("Use /resume <plan-id> to continue work on a plan.\n")
	return r.persistSystemMessage(b.String())
}

func (r *Runner) runResumePlanCommand(args []string) error {
	orch, _, err := r.ensureOrchestrator()
	if err != nil {
		return fmt.Errorf("initializing orchestrator for resume: %w", err)
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: /resume <plan-id>")
	}
	planID := args[0]
	if err := orch.ResumeFeature(planID); err != nil {
		return fmt.Errorf("resuming plan %s: %w", planID, err)
	}
	plan := orch.GetCurrentPlan()
	if plan == nil {
		return fmt.Errorf("plan not loaded")
	}
	completed := 0
	for _, task := range plan.Tasks {
		if task.Status == orchestrator.TaskCompleted {
			completed++
		}
	}
	return r.persistSystemMessage(fmt.Sprintf("✓ Resumed plan: %s (%d/%d tasks completed)\nUse /status to see details.", plan.FeatureName, completed, len(plan.Tasks)))
}

func (r *Runner) runWorkflowCommand(args []string) error {
	_, wf, err := r.ensureOrchestrator()
	if err != nil {
		return fmt.Errorf("initializing orchestrator for workflow: %w", err)
	}
	if wf == nil {
		return fmt.Errorf("workflow manager not initialized")
	}

	action := "status"
	if len(args) > 0 {
		action = strings.ToLower(args[0])
	}

	switch action {
	case "status":
		return r.persistSystemMessage(formatWorkflowStatus(wf))
	case "pause":
		reason := "Manual pause via /workflow pause"
		if len(args) > 1 {
			reason = strings.Join(args[1:], " ")
		}
		if err := wf.Pause(reason, "Awaiting user instructions"); err != nil && !errors.Is(err, orchestrator.ErrWorkflowPaused) {
			return fmt.Errorf("pausing workflow: %w", err)
		}
		r.setState(StatePaused)
		return r.persistSystemMessage(fmt.Sprintf("⚠ Workflow paused (%s)", reason))
	case "resume":
		note := "Manual resume via /workflow resume"
		if len(args) > 1 {
			note = strings.Join(args[1:], " ")
		}
		wf.Resume(note)
		r.setState(StateIdle)
		return r.persistSystemMessage(fmt.Sprintf("✓ Workflow resumed (%s)", note))
	case "phases":
		return r.persistSystemMessage(formatWorkflowPhases(wf.TaskPhases()))
	default:
		return fmt.Errorf("unknown workflow action: %s (try status|pause|resume|phases)", action)
	}
}

func (r *Runner) handleWorkflowPause(err error) bool {
	var pauseErr *orchestrator.WorkflowPauseError
	if err == nil || !errors.As(err, &pauseErr) {
		return false
	}

	reason := strings.TrimSpace(pauseErr.Reason)
	if reason == "" {
		reason = "Awaiting user input"
	}
	action := strings.TrimSpace(pauseErr.Question)
	if action == "" {
		action = "Confirm next steps"
	}

	_ = r.persistSystemMessage(fmt.Sprintf("⚠ Workflow paused: %s\nAction required: %s", reason, action))
	r.setState(StatePaused)
	return true
}

func (r *Runner) formatCommandError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("Error: %v", err)
}
