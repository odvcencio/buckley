package parallel

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Coordinator orchestrates parallel agent execution with conflict-aware scheduling.
// It combines scope validation, file locking, and merge orchestration into a
// unified workflow that prevents agents from stepping on each other's changes.
type Coordinator struct {
	mu              sync.RWMutex
	worktreeManager WorktreeManager
	executor        TaskExecutor
	orchestratorCfg Config
	scopeValidator  *ScopeValidator
	lockManager     *FileLockManager
	mergeOrch       *MergeOrchestrator
	repoPath        string
	autoMerge       bool
	mergeCfg        MergeConfig
	onConflict      func(ConflictEvent)
	onPartition     func(PartitionEvent)
	onMerge         func(MergeEvent)
}

// ConflictEvent is emitted when a scope conflict is detected.
type ConflictEvent struct {
	Timestamp time.Time
	Conflicts []Conflict
	Message   string
}

// PartitionEvent is emitted when tasks are partitioned into waves.
type PartitionEvent struct {
	Timestamp  time.Time
	Partitions []TaskPartition
	TotalTasks int
	Waves      int
}

// MergeEvent is emitted when merge completes.
type MergeEvent struct {
	Timestamp time.Time
	Report    *MergeReport
}

// CoordinatorConfig configures the coordinator.
type CoordinatorConfig struct {
	RepoPath       string
	MaxAgents      int
	LockTTL        time.Duration
	MergeStrategy  MergeStrategy
	AutoMerge      bool
	CleanupOnMerge bool
}

// DefaultCoordinatorConfig returns sensible defaults.
func DefaultCoordinatorConfig(repoPath string) CoordinatorConfig {
	return CoordinatorConfig{
		RepoPath:       repoPath,
		MaxAgents:      4,
		LockTTL:        5 * time.Minute,
		MergeStrategy:  MergeStrategyPause,
		AutoMerge:      true,
		CleanupOnMerge: false,
	}
}

// NewCoordinator creates a new coordinator with all subsystems.
func NewCoordinator(wtManager WorktreeManager, executor TaskExecutor, cfg CoordinatorConfig) *Coordinator {
	orchCfg := Config{
		MaxAgents:       cfg.MaxAgents,
		TaskQueueSize:   100,
		ResultQueueSize: 100,
	}

	lockCfg := DefaultFileLockConfig()
	lockCfg.DefaultTTL = cfg.LockTTL

	mergeCfg := DefaultMergeConfig()
	mergeCfg.Strategy = cfg.MergeStrategy
	mergeCfg.CleanupOnMerge = cfg.CleanupOnMerge

	c := &Coordinator{
		worktreeManager: wtManager,
		executor:        executor,
		orchestratorCfg: orchCfg,
		scopeValidator:  NewScopeValidator(),
		lockManager:     NewFileLockManager(lockCfg),
		mergeOrch:       NewMergeOrchestrator(cfg.RepoPath),
		repoPath:        cfg.RepoPath,
		autoMerge:       cfg.AutoMerge,
		mergeCfg:        mergeCfg,
	}

	// Wire up lock conflict notifications
	c.lockManager.SetConflictCallback(func(lock *FileLock, waiter string) {
		if c.onConflict != nil {
			c.onConflict(ConflictEvent{
				Timestamp: time.Now(),
				Message:   fmt.Sprintf("Agent %s waiting for lock on %s (held by %s)", waiter, lock.Path, lock.AgentID),
			})
		}
	})

	return c
}

// ExecuteParallel executes tasks in parallel with conflict-aware scheduling.
// Tasks with overlapping scopes are automatically serialized.
func (c *Coordinator) ExecuteParallel(ctx context.Context, tasks []*AgentTask, targetBranch string) (*ExecutionReport, error) {
	if len(tasks) == 0 {
		return &ExecutionReport{TargetBranch: targetBranch}, nil
	}

	start := time.Now()
	report := &ExecutionReport{
		StartTime:    start,
		TargetBranch: targetBranch,
	}

	// Phase 1: Extract scopes and partition tasks
	scopes := make([]*TaskScope, 0, len(tasks))
	taskByID := make(map[string]*AgentTask)
	for _, task := range tasks {
		if task == nil {
			continue
		}
		if strings.TrimSpace(task.ID) == "" {
			task.ID = generateTaskID()
		}
		scope := c.scopeValidator.ExtractScope(task)
		scopes = append(scopes, scope)
		taskByID[task.ID] = task
	}
	if len(taskByID) == 0 {
		return report, nil
	}

	// Check for conflicts
	conflicts := c.scopeValidator.CheckConflicts(scopes)
	report.Conflicts = conflicts

	if len(conflicts) > 0 && c.onConflict != nil {
		c.onConflict(ConflictEvent{
			Timestamp: time.Now(),
			Conflicts: conflicts,
			Message:   c.scopeValidator.ConflictReport(conflicts),
		})
	}

	// Partition tasks into execution waves
	partitions := c.scopeValidator.PartitionTasks(scopes)
	report.Partitions = partitions

	if c.onPartition != nil {
		c.onPartition(PartitionEvent{
			Timestamp:  time.Now(),
			Partitions: partitions,
			TotalTasks: len(tasks),
			Waves:      len(partitions),
		})
	}

	// Execute each wave sequentially, tasks within wave in parallel
	allResults := make([]*AgentResult, 0, len(tasks))

	for waveNum, partition := range partitions {
		report.CurrentWave = waveNum

		// Collect tasks for this wave
		waveTasks := make([]*AgentTask, 0, len(partition.TaskIDs))
		for _, taskID := range partition.TaskIDs {
			if task, ok := taskByID[taskID]; ok {
				waveTasks = append(waveTasks, task)
			}
		}

		if len(waveTasks) == 0 {
			continue
		}

		// Execute wave
		waveResults, err := c.executeWave(ctx, waveTasks)
		if err != nil {
			report.Error = err
			break
		}

		allResults = append(allResults, waveResults...)

		// Check if all tasks in wave succeeded
		allSucceeded := true
		for _, r := range waveResults {
			if !r.Success {
				allSucceeded = false
				break
			}
		}

		if !allSucceeded {
			report.Error = fmt.Errorf("wave %d had failures", waveNum)
			// Continue to next wave or stop based on policy
		}
	}

	report.Results = allResults
	report.Duration = time.Since(start)

	// Phase 2: Merge results back to target branch
	if c.autoMerge && len(allResults) > 0 && targetBranch != "" {
		mergeCfg := c.mergeCfg
		mergeCfg.TargetBranch = targetBranch
		mergeReport, err := c.mergeOrch.MergeResults(ctx, allResults, mergeCfg)
		if err != nil {
			report.MergeError = err
		} else {
			report.MergeReport = mergeReport
			if c.onMerge != nil {
				c.onMerge(MergeEvent{
					Timestamp: time.Now(),
					Report:    mergeReport,
				})
			}
		}
	}

	return report, nil
}

// executeWave executes a single wave of tasks in parallel.
func (c *Coordinator) executeWave(ctx context.Context, tasks []*AgentTask) ([]*AgentResult, error) {
	if len(tasks) == 0 {
		return nil, nil
	}

	// Start a fresh orchestrator per wave
	orchestrator := NewOrchestrator(c.worktreeManager, c.executor, c.orchestratorCfg)
	orchestrator.Start()
	defer orchestrator.Stop()

	// Submit all tasks
	for _, task := range tasks {
		if err := orchestrator.Submit(task); err != nil {
			return nil, fmt.Errorf("failed to submit task %s: %w", task.ID, err)
		}
	}

	// Collect results
	results := make([]*AgentResult, 0, len(tasks))
	timeout := 30 * time.Minute // Per-wave timeout

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for len(results) < len(tasks) {
		select {
		case result := <-orchestrator.Results():
			results = append(results, result)

		case <-timer.C:
			return results, fmt.Errorf("wave timeout after %v", timeout)

		case <-ctx.Done():
			return results, ctx.Err()
		}
	}

	return results, nil
}

// ExecutionReport captures the outcome of parallel execution.
type ExecutionReport struct {
	StartTime    time.Time
	Duration     time.Duration
	TargetBranch string

	// Conflict detection
	Conflicts   []Conflict
	Partitions  []TaskPartition
	CurrentWave int

	// Execution
	Results []*AgentResult
	Error   error

	// Merge
	MergeReport *MergeReport
	MergeError  error
}

// Markdown generates a markdown report.
func (r *ExecutionReport) Markdown() string {
	var b strings.Builder

	b.WriteString("# Parallel Execution Report\n\n")
	b.WriteString(fmt.Sprintf("**Duration:** %s\n", r.Duration.Round(time.Millisecond)))
	b.WriteString(fmt.Sprintf("**Target Branch:** %s\n\n", r.TargetBranch))

	// Conflicts
	if len(r.Conflicts) > 0 {
		b.WriteString("## Scope Conflicts\n\n")
		b.WriteString(fmt.Sprintf("%d conflicts detected, tasks partitioned into %d waves.\n\n",
			len(r.Conflicts), len(r.Partitions)))

		for _, c := range r.Conflicts {
			b.WriteString(fmt.Sprintf("- **%s** ↔ **%s**: ", c.TaskA, c.TaskB))
			if len(c.OverlapFiles) > 0 {
				b.WriteString(strings.Join(c.OverlapFiles, ", "))
			}
			if len(c.OverlapGlobs) > 0 {
				b.WriteString(strings.Join(c.OverlapGlobs, ", "))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Execution results
	b.WriteString("## Execution Results\n\n")

	succeeded := 0
	failed := 0
	for _, result := range r.Results {
		if result.Success {
			succeeded++
		} else {
			failed++
		}
	}

	b.WriteString(fmt.Sprintf("| Metric | Value |\n"))
	b.WriteString(fmt.Sprintf("|--------|-------|\n"))
	b.WriteString(fmt.Sprintf("| Total Tasks | %d |\n", len(r.Results)))
	b.WriteString(fmt.Sprintf("| Succeeded | %d |\n", succeeded))
	b.WriteString(fmt.Sprintf("| Failed | %d |\n", failed))
	b.WriteString(fmt.Sprintf("| Waves | %d |\n", len(r.Partitions)))
	b.WriteString("\n")

	// Per-task details
	if len(r.Results) > 0 {
		b.WriteString("### Task Details\n\n")
		b.WriteString("| Task | Status | Duration | Files | Branch |\n")
		b.WriteString("|------|--------|----------|-------|--------|\n")

		for _, result := range r.Results {
			status := "✗"
			if result.Success {
				status = "✓"
			}
			files := fmt.Sprintf("%d", len(result.Files))
			b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
				result.TaskID, status, result.Duration.Round(time.Millisecond), files, result.Branch))
		}
		b.WriteString("\n")
	}

	// Merge report
	if r.MergeReport != nil {
		b.WriteString(r.MergeReport.Markdown())
	}

	if r.Error != nil {
		b.WriteString(fmt.Sprintf("\n**Error:** %v\n", r.Error))
	}
	if r.MergeError != nil {
		b.WriteString(fmt.Sprintf("\n**Merge Error:** %v\n", r.MergeError))
	}

	return b.String()
}

// SetConflictHandler sets a callback for conflict events.
func (c *Coordinator) SetConflictHandler(fn func(ConflictEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onConflict = fn
}

// SetPartitionHandler sets a callback for partition events.
func (c *Coordinator) SetPartitionHandler(fn func(PartitionEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onPartition = fn
}

// SetMergeHandler sets a callback for merge events.
func (c *Coordinator) SetMergeHandler(fn func(MergeEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMerge = fn
}

// GetLockManager returns the file lock manager for direct access.
func (c *Coordinator) GetLockManager() *FileLockManager {
	return c.lockManager
}

// GetScopeValidator returns the scope validator for direct access.
func (c *Coordinator) GetScopeValidator() *ScopeValidator {
	return c.scopeValidator
}

// PreviewExecution does a dry-run to show what would happen.
func (c *Coordinator) PreviewExecution(tasks []*AgentTask) *ExecutionPreview {
	scopes := make([]*TaskScope, 0, len(tasks))
	for _, task := range tasks {
		scopes = append(scopes, c.scopeValidator.ExtractScope(task))
	}

	conflicts := c.scopeValidator.CheckConflicts(scopes)
	partitions := c.scopeValidator.PartitionTasks(scopes)

	return &ExecutionPreview{
		TotalTasks:    len(tasks),
		Conflicts:     conflicts,
		Partitions:    partitions,
		CanParallel:   len(partitions) == 1,
		RequiresWaves: len(partitions) > 1,
	}
}

// ExecutionPreview shows what would happen without executing.
type ExecutionPreview struct {
	TotalTasks    int
	Conflicts     []Conflict
	Partitions    []TaskPartition
	CanParallel   bool
	RequiresWaves bool
}

// Markdown generates a preview report.
func (p *ExecutionPreview) Markdown() string {
	var b strings.Builder

	b.WriteString("# Execution Preview\n\n")
	b.WriteString(fmt.Sprintf("**Total Tasks:** %d\n", p.TotalTasks))

	if p.CanParallel {
		b.WriteString("**Status:** All tasks can run in parallel ✓\n\n")
	} else {
		b.WriteString(fmt.Sprintf("**Status:** Tasks will run in %d waves due to conflicts\n\n", len(p.Partitions)))
	}

	if len(p.Conflicts) > 0 {
		b.WriteString("## Detected Conflicts\n\n")
		for _, c := range p.Conflicts {
			b.WriteString(fmt.Sprintf("- %s ↔ %s\n", c.TaskA, c.TaskB))
		}
		b.WriteString("\n")
	}

	b.WriteString("## Execution Waves\n\n")
	for i, partition := range p.Partitions {
		b.WriteString(fmt.Sprintf("### Wave %d\n", i+1))
		b.WriteString(fmt.Sprintf("Tasks: %s\n", strings.Join(partition.TaskIDs, ", ")))
		if len(partition.WaitFor) > 0 {
			b.WriteString(fmt.Sprintf("Waits for: %s\n", strings.Join(partition.WaitFor, ", ")))
		}
		b.WriteString("\n")
	}

	return b.String()
}
