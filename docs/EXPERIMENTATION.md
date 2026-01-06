# Buckley Experimentation Platform

**Version**: 1.1
**Date**: 2026-01-02
**Status**: Ship ASAP - Core Infrastructure Complete

---

## Executive Summary

This document defines the architecture for Buckley's experimentation capabilities—the features that transform it from "another AI coding assistant" into **THE agent harness** for parallel work and model experimentation.

The core insight: Buckley already has most of the infrastructure. `pkg/parallel/` has a complete worktree-based orchestrator. `pkg/telemetry/` has event pub/sub. The schema has `agent_activity` and `tool_audit_log`.

**As of commit d143dfd (2026-01-02)**, significant gaps have been closed:
- ✅ Real-time operation visibility (telemetry bridge rewrite)
- ✅ Web UI components (ApprovalModal, CommandPalette, ConversationSearch)
- ✅ Model streaming lifecycle events
- ✅ File listing API with SQLite indexing
- ✅ This design document

**What remains for MVP ship (now complete):**
1. ✅ Wire `pkg/parallel/` to CLI (`buckley experiment run`)
2. ✅ Add Ollama provider (~200 lines)
3. ✅ Basic markdown report generation

This design follows Buckley's established principles: DDD bounded contexts, Clean Architecture dependency flow, and the existing schema/telemetry patterns.

---

## 0. Ship ASAP: Minimum Viable Release

### What Just Shipped (d143dfd)

| Component | Lines | Impact |
|-----------|-------|--------|
| `EXPERIMENTATION.md` | 2,053 | Full architecture spec |
| `ApprovalModal.tsx` | 150 | Tool approval with diff preview |
| `CommandPalette.tsx` | 188 | Fuzzy slash command picker |
| `ConversationSearch.tsx` | 163 | Session search |
| `telemetry_bridge.go` | 484 | Complete rewrite - live operation visibility |
| `runtime.go` | +77 | Streaming state, tool tracking |
| `server.go` | +84 | `/api/files` endpoint |
| Telemetry events | +2 | `model.stream_start`, `model.stream_end` |

**Total: +3,384 lines across 20 files**

### MVP Ship Checklist

```
MUST HAVE (blocks release):
├── [x] Parallel orchestrator (pkg/parallel/agents.go - 419 lines, complete)
├── [x] Telemetry hub with lifecycle events (pkg/telemetry/ - complete)
├── [x] Real-time UI updates (telemetry_bridge.go - just shipped)
├── [x] Web UI approval flow (ApprovalModal.tsx - just shipped)
├── [x] Design documentation (this file)
├── [x] CLI exposure: `buckley experiment run` command
├── [x] Ollama provider (pkg/model/provider_ollama.go)
└── [x] Basic markdown reporter

NICE TO HAVE (ship without):
├── [x] LiteLLM integration
├── [ ] Experiment TUI widget
├── [ ] Session replay
├── [ ] Success criteria evaluation
└── [ ] Cost comparison charts
```

### 48-Hour Ship Path

**Hour 0-8: CLI Command**
```go
// cmd/buckley/experiment.go - wire existing infrastructure
var experimentRunCmd = &cobra.Command{
    Use:   "run [name]",
    Short: "Run parallel model comparison",
    RunE: func(cmd *cobra.Command, args []string) error {
        // 1. Parse --models, --prompt flags
        // 2. Create parallel.Orchestrator with worktree manager
        // 3. Submit AgentTask per model
        // 4. Collect results, print markdown table
    },
}
```

**Hour 8-16: Ollama Provider**
```go
// pkg/model/provider_ollama.go - OpenAI-compatible, ~200 lines
// Ollama exposes /api/chat with same format
// Just needs: FetchCatalog(), ChatCompletion(), ChatCompletionStream()
```

**Hour 16-24: Reporter**
```go
// pkg/experiment/reporter.go - markdown table generator
// Input: []parallel.AgentResult
// Output: | Model | Success | Duration | Tokens | Cost |
```

**Hour 24-32: Integration Testing**
```bash
# Test the full flow
buckley experiment run "test-refactor" \
    -m openrouter/anthropic/claude-3.5-sonnet \
    -m ollama/codellama:34b \
    -p "Refactor the config loader to use YAML"
```

**Hour 32-48: Documentation + Release**
- Update README with experiment examples
- Record demo GIF
- Tag release

### What's Already Wired

The telemetry bridge (just shipped) already:
- Tracks running tools in real-time
- Syncs plan task status
- Manages recent files list
- Pushes updates to sidebar

The parallel orchestrator already:
- Creates worktree per agent
- Manages worker pool (default 4)
- Collects results via channel
- Handles cleanup

**The hard work is done. What remains is plumbing.**

---

## 1. Gap Analysis: Current State

### What Exists (Infrastructure Complete)

| Component | Location | Status |
|-----------|----------|--------|
| Parallel orchestrator | `pkg/parallel/agents.go` | **Complete.** Worktree-based, task queuing, worker pool, cleanup. |
| Telemetry hub | `pkg/telemetry/` | **Complete.** Event types for full lifecycle, pub/sub with buffering. |
| Telemetry UI bridge | `pkg/ui/tui/telemetry_bridge.go` | **Complete.** ✅ Just shipped - real-time operation visibility. |
| Runtime state tracker | `pkg/ui/viewmodel/runtime.go` | **Complete.** ✅ Just shipped - streaming state, tool tracking. |
| Web UI components | `web/src/components/` | **Complete.** ✅ Just shipped - ApprovalModal, CommandPalette, ConversationSearch. |
| Agent activity tracking | `storage/schema.sql:agent_activity` | **Complete.** Status, actions, timestamps per agent. |
| Tool audit log | `storage/schema.sql:tool_audit_log` | **Complete.** Decision, risk score, duration, input/output. |
| Provider abstraction | `pkg/model/provider.go` | **Complete.** Interface with OpenRouter, OpenAI, Anthropic, Google implementations. |
| Execution tracking | `storage/schema.sql:executions` | **Complete.** Plan/task linkage, retry counts, artifacts. |
| Session management | `pkg/session/`, `pkg/storage/` | **Complete.** SQLite-backed with full conversation persistence. |
| File listing API | `pkg/ipc/server.go` | **Complete.** ✅ Just shipped - SQLite index with filesystem fallback. |
| Model streaming events | `pkg/telemetry/telemetry.go` | **Complete.** ✅ Just shipped - `model.stream_start`/`model.stream_end`. |

### What's Missing (MVP Blockers in Bold)

| Gap | Impact | Effort | Priority |
|-----|--------|--------|----------|
| **CLI exposure** | Can't run experiments from command line | **Low** | **MVP** |
| **Ollama provider** | Can't use local LLMs | **Low** | **MVP** |
| **Basic reporter** | Can't see comparison results | **Low** | **MVP** |
| Experiment abstraction | Can't persist/query experiments | Medium | Post-MVP |
| Result comparison | Can't diff outputs semantically | Medium | Post-MVP |
| LiteLLM integration | Limited to 4 providers | Low | Post-MVP |
| Success criteria | Can't auto-evaluate results | Medium | Post-MVP |
| Replay capability | Can't re-run with different config | High | Post-MVP |

---

## 2. Architecture Overview

### 2.1 New Bounded Context: `pkg/experiment/`

```
pkg/experiment/
├── experiment.go      # Core domain types (Experiment, Variant, Run)
├── store.go           # SQLite persistence layer
├── runner.go          # Coordinates parallel execution
├── comparator.go      # Diffs and scores results
├── reporter.go        # Generates comparison reports
└── replay.go          # Session replay with variant config
```

This follows the existing pattern: domain types → store → business logic.

### 2.2 System Flow

```
┌────────────────────────────────────────────────────────────────────────┐
│                         Experiment Runner                               │
│                                                                        │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐                │
│  │  Variant 1  │    │  Variant 2  │    │  Variant N  │                │
│  │  (Claude)   │    │  (GPT-4o)   │    │  (Ollama)   │                │
│  └──────┬──────┘    └──────┬──────┘    └──────┬──────┘                │
│         │                  │                  │                        │
│         ▼                  ▼                  ▼                        │
│  ┌─────────────────────────────────────────────────────┐              │
│  │           pkg/parallel/Orchestrator                  │              │
│  │  • Worktree per variant (isolated git state)        │              │
│  │  • TaskExecutor invokes model + tools               │              │
│  │  • Results channel collects outcomes                │              │
│  └─────────────────────────────────────────────────────┘              │
│                              │                                         │
│                              ▼                                         │
│  ┌─────────────────────────────────────────────────────┐              │
│  │              Telemetry Hub (existing)                │              │
│  │  • tool.started/completed/failed                    │              │
│  │  • cost.updated, tokens.updated                     │              │
│  │  • model.stream_start/end                           │              │
│  └─────────────────────────────────────────────────────┘              │
│                              │                                         │
│                              ▼                                         │
│  ┌─────────────────────────────────────────────────────┐              │
│  │              Comparator + Reporter                   │              │
│  │  • Diff outputs (semantic + textual)                │              │
│  │  • Aggregate metrics (cost, tokens, duration)       │              │
│  │  • Score by success criteria                        │              │
│  └─────────────────────────────────────────────────────┘              │
└────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Domain Model

### 3.1 Core Types

```go
// pkg/experiment/experiment.go

// Experiment groups related variants testing a hypothesis.
type Experiment struct {
    ID          string
    Name        string
    Description string
    Hypothesis  string            // What we're testing
    Task        Task              // The work to perform
    Variants    []Variant         // Different configurations
    Criteria    []SuccessCriterion // How to judge results
    Status      ExperimentStatus
    CreatedAt   time.Time
    CompletedAt *time.Time
}

// Variant is a specific configuration to test.
type Variant struct {
    ID            string
    Name          string
    ModelID       string            // e.g., "anthropic/claude-3.5-sonnet"
    Provider      string            // e.g., "openrouter", "ollama", "litellm"
    SystemPrompt  *string           // Override system prompt
    Temperature   *float64          // Override temperature
    MaxTokens     *int              // Override max tokens
    ToolsAllowed  []string          // Restrict available tools
    CustomConfig  map[string]any    // Provider-specific options
}

// Task describes what the agent should do.
type Task struct {
    Prompt      string
    Context     map[string]string // File contents, env vars, etc.
    WorkingDir  string
    Timeout     time.Duration
}

// Run is a single execution of a variant.
type Run struct {
    ID           string
    ExperimentID string
    VariantID    string
    SessionID    string            // Links to sessions table
    Branch       string            // Git worktree branch
    Status       RunStatus
    Output       string
    Files        []string          // File paths changed (MVP)
    Metrics      RunMetrics
    Error        *string
    StartedAt    time.Time
    CompletedAt  *time.Time
}

// RunMetrics captures measurable outcomes.
type RunMetrics struct {
    DurationMs      int64
    PromptTokens    int
    CompletionTokens int
    TotalCost       float64
    ToolCalls       int
    ToolSuccesses   int
    ToolFailures    int
    FilesModified   int
    LinesChanged    int
}

// SuccessCriterion defines how to evaluate results.
type SuccessCriterion struct {
    Name       string
    Type       CriterionType // "test_pass", "file_exists", "contains", "command", "manual"
    Target     string        // What to check
    Weight     float64       // Importance for scoring
}
```

### 3.2 Status Enums

```go
type ExperimentStatus string

const (
    ExperimentPending   ExperimentStatus = "pending"
    ExperimentRunning   ExperimentStatus = "running"
    ExperimentCompleted ExperimentStatus = "completed"
    ExperimentFailed    ExperimentStatus = "failed"
    ExperimentCancelled ExperimentStatus = "cancelled"
)

type RunStatus string

const (
    RunPending   RunStatus = "pending"
    RunRunning   RunStatus = "running"
    RunCompleted RunStatus = "completed"
    RunFailed    RunStatus = "failed"
    RunCancelled RunStatus = "cancelled"
)

type CriterionType string

const (
    CriterionTestPass   CriterionType = "test_pass"    // Run tests, check exit code
    CriterionFileExists CriterionType = "file_exists"  // Check file was created
    CriterionContains   CriterionType = "contains"     // Output contains string
    CriterionCommand    CriterionType = "command"      // Run command, check exit code
    CriterionManual     CriterionType = "manual"       // Human judgment
)
```

---

## 4. Schema Extensions

Add to `pkg/storage/schema.sql`:

```sql
-- Experiments table: groups related variant runs
CREATE TABLE IF NOT EXISTS experiments (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT,
    hypothesis TEXT,
    task_prompt TEXT NOT NULL,
    task_context TEXT,          -- JSON
    task_working_dir TEXT,
    task_timeout_ms INTEGER,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK(status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_experiments_status ON experiments(status);
CREATE INDEX IF NOT EXISTS idx_experiments_created ON experiments(created_at);

-- Experiment variants: different configurations to test
CREATE TABLE IF NOT EXISTS experiment_variants (
    id TEXT PRIMARY KEY,
    experiment_id TEXT NOT NULL,
    name TEXT NOT NULL,
    model_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    system_prompt TEXT,
    temperature REAL,
    max_tokens INTEGER,
    tools_allowed TEXT,         -- JSON array
    custom_config TEXT,         -- JSON
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (experiment_id) REFERENCES experiments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_variants_experiment ON experiment_variants(experiment_id);

-- Experiment runs: individual executions of variants
CREATE TABLE IF NOT EXISTS experiment_runs (
    id TEXT PRIMARY KEY,
    experiment_id TEXT NOT NULL,
    variant_id TEXT NOT NULL,
    session_id TEXT,
    branch TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK(status IN ('pending', 'running', 'completed', 'failed', 'cancelled')),
    output TEXT,
    files_changed TEXT,         -- JSON array of file paths (MVP)
    error TEXT,
    -- Metrics
    duration_ms INTEGER,
    prompt_tokens INTEGER,
    completion_tokens INTEGER,
    total_cost REAL,
    tool_calls INTEGER,
    tool_successes INTEGER,
    tool_failures INTEGER,
    files_modified INTEGER,
    lines_changed INTEGER,
    -- Timestamps
    started_at TIMESTAMP,
    completed_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (experiment_id) REFERENCES experiments(id) ON DELETE CASCADE,
    FOREIGN KEY (variant_id) REFERENCES experiment_variants(id) ON DELETE CASCADE,
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS idx_runs_experiment ON experiment_runs(experiment_id);
CREATE INDEX IF NOT EXISTS idx_runs_variant ON experiment_runs(variant_id);
CREATE INDEX IF NOT EXISTS idx_runs_status ON experiment_runs(status);

-- Success criteria definitions
CREATE TABLE IF NOT EXISTS experiment_criteria (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    experiment_id TEXT NOT NULL,
    name TEXT NOT NULL,
    criterion_type TEXT NOT NULL
        CHECK(criterion_type IN ('test_pass', 'file_exists', 'contains', 'command', 'manual')),
    target TEXT NOT NULL,
    weight REAL DEFAULT 1.0,
    FOREIGN KEY (experiment_id) REFERENCES experiments(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_criteria_experiment ON experiment_criteria(experiment_id);

-- Criterion evaluations per run
CREATE TABLE IF NOT EXISTS experiment_evaluations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id TEXT NOT NULL,
    criterion_id INTEGER NOT NULL,
    passed INTEGER NOT NULL,    -- 0 or 1
    score REAL,                 -- 0.0 to 1.0 for partial credit
    details TEXT,               -- Explanation or error message
    evaluated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (run_id) REFERENCES experiment_runs(id) ON DELETE CASCADE,
    FOREIGN KEY (criterion_id) REFERENCES experiment_criteria(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_evaluations_run ON experiment_evaluations(run_id);
```

---

## 5. Runner Implementation

### 5.1 Integration with `pkg/parallel/`

The existing `pkg/parallel/Orchestrator` handles worktree management and worker pools. The experiment runner wraps it:

```go
// pkg/experiment/runner.go

type Runner struct {
    store           *Store
    parallel        *parallel.Orchestrator
    modelManager    *model.Manager
    telemetryHub    *telemetry.Hub
    maxConcurrent   int
    cleanupOnDone   bool
}

type RunnerConfig struct {
    MaxConcurrent   int           // Default: 4
    DefaultTimeout  time.Duration // Default: 30m
    WorktreeRoot    string        // Passed to worktree.NewManager (not parallel.Config)
    CleanupOnDone   bool          // Default: true
}

func NewRunner(cfg RunnerConfig, deps Dependencies) *Runner {
    parallelCfg := parallel.Config{
        MaxAgents:     cfg.MaxConcurrent,
        TaskQueueSize: 100,
    }

    // Create TaskExecutor that invokes model + tools
    executor := &experimentExecutor{
        modelManager: deps.ModelManager,
        telemetry:    deps.TelemetryHub,
    }

    return &Runner{
        store:        deps.Store,
        parallel:     parallel.NewOrchestrator(deps.WorktreeManager, executor, parallelCfg),
        modelManager: deps.ModelManager,
        telemetryHub: deps.TelemetryHub,
        maxConcurrent: cfg.MaxConcurrent,
        cleanupOnDone: cfg.CleanupOnDone,
    }
}

// RunExperiment executes all variants in parallel.
func (r *Runner) RunExperiment(ctx context.Context, exp *Experiment) error {
    // Update status
    exp.Status = ExperimentRunning
    if err := r.store.UpdateExperiment(exp); err != nil {
        return fmt.Errorf("update experiment: %w", err)
    }

    // Start parallel orchestrator
    r.parallel.Start()
    defer r.parallel.Stop()

    // Submit tasks for each variant
    for _, variant := range exp.Variants {
        task := &parallel.AgentTask{
            ID:          variant.ID,
            Name:        variant.Name,
            Description: exp.Task.Prompt,
            Branch:      fmt.Sprintf("experiment/%s/%s", exp.ID, variant.ID),
            Prompt:      exp.Task.Prompt,
            Context: map[string]string{
                "model_id":   variant.ModelID,
                "provider":   variant.Provider,
                "experiment": exp.ID,
            },
        }

        if err := r.parallel.Submit(task); err != nil {
            return fmt.Errorf("submit variant %s: %w", variant.ID, err)
        }
    }

    // Collect results
    results := make(map[string]*parallel.AgentResult)
    for result := range r.parallel.Results() {
        variantID := result.TaskID
        results[variantID] = result

        // Persist run
        run := r.resultToRun(exp.ID, variantID, result)
        if err := r.store.SaveRun(run); err != nil {
            // Log but don't fail
            log.Printf("failed to save run: %v", err)
        }

        // Emit telemetry
        r.telemetryHub.Publish(telemetry.Event{
            Type:      "experiment.run_completed",
            SessionID: run.SessionID,
            Data: map[string]any{
                "experiment_id": exp.ID,
                "variant_id":    variantID,
                "success":       result.Success,
                "duration_ms":   result.Duration.Milliseconds(),
            },
        })

        if len(results) == len(exp.Variants) {
            break
        }
    }

    // Update experiment status
    exp.Status = ExperimentCompleted
    exp.CompletedAt = ptr(time.Now())
    if r.cleanupOnDone {
        if err := r.parallel.Cleanup(); err != nil {
            log.Printf("cleanup failed: %v", err)
        }
    }
    return r.store.UpdateExperiment(exp)
}
```

### 5.2 Experiment Executor

```go
// experimentExecutor implements parallel.TaskExecutor for experiments.
type experimentExecutor struct {
    modelManager *model.Manager
    telemetry    *telemetry.Hub
}

func (e *experimentExecutor) Execute(
    ctx context.Context,
    task *parallel.AgentTask,
    wtPath string,
) (*parallel.AgentResult, error) {
    modelID := task.Context["model_id"]
    if modelID == "" {
        modelID = task.Name
    }

    // Create isolated session for this run
    session := &conversation.Session{
        ID:          ulid.Make().String(),
        ProjectPath: wtPath,
        // ... other fields
    }

    // Build a per-run tool registry to avoid cross-run mutation.
    registry := tool.NewRegistry()
    allowedSet := map[string]struct{}{}
    if allowedTools, ok := task.Context["tools_allowed"]; ok {
        for _, name := range strings.Split(allowedTools, ",") {
            name = strings.TrimSpace(name)
            if name != "" {
                allowedSet[name] = struct{}{}
            }
        }
        registry = tool.NewRegistry(tool.WithBuiltinFilter(func(t tool.Tool) bool {
            _, ok := allowedSet[strings.TrimSpace(t.Name())]
            return ok
        }))
    }
    _ = registry.LoadDefaultPlugins()
    if len(allowedSet) > 0 {
        registry.Filter(func(t tool.Tool) bool {
            _, ok := allowedSet[strings.TrimSpace(t.Name())]
            return ok
        })
    }
    registry.SetWorkDir(wtPath)
    if e.telemetry != nil {
        registry.EnableTelemetry(e.telemetry, session.ID)
    }

    // Execute the task
    start := time.Now()
    result, err := e.executeConversation(ctx, session, modelID, registry, task.Prompt)
    duration := time.Since(start)

    if err != nil {
        return &parallel.AgentResult{
            TaskID:   task.ID,
            Success:  false,
            Error:    err,
            Duration: duration,
            Branch:   task.Branch,
        }, nil
    }

    return &parallel.AgentResult{
        TaskID:   task.ID,
        Success:  true,
        Output:   result.Output,
        Duration: duration,
        Branch:   task.Branch,
        Files:    result.ModifiedFiles,
        Metrics: map[string]int{
            "prompt_tokens":     result.PromptTokens,
            "completion_tokens": result.CompletionTokens,
            "tool_calls":        result.ToolCalls,
        },
    }, nil
}
```

---

## 6. Comparator and Reporter

### 6.1 Comparison Logic

```go
// pkg/experiment/comparator.go

type Comparator struct {
    store *Store
}

type ComparisonReport struct {
    ExperimentID string
    Variants     []VariantReport
    Rankings     []Ranking
    Summary      string
}

type VariantReport struct {
    VariantID     string
    VariantName   string
    ModelID       string
    Metrics       RunMetrics
    CriteriaScore float64          // 0.0 to 1.0
    CriteriaPassed []string
    CriteriaFailed []string
    OutputPreview string           // First 500 chars
    DiffFromBest  *FileDiff        // Diff against highest-scoring variant
}

type Ranking struct {
    VariantID string
    Score     float64
    Rank      int
    Notes     string
}

func (c *Comparator) Compare(exp *Experiment) (*ComparisonReport, error) {
    runs, err := c.store.GetRunsForExperiment(exp.ID)
    if err != nil {
        return nil, err
    }

    criteria, err := c.store.GetCriteriaForExperiment(exp.ID)
    if err != nil {
        return nil, err
    }

    // Evaluate each run against criteria
    variantReports := make([]VariantReport, 0, len(runs))
    for _, run := range runs {
        variant, _ := c.store.GetVariant(run.VariantID)

        score, passed, failed := c.evaluateCriteria(run, criteria)

        variantReports = append(variantReports, VariantReport{
            VariantID:      run.VariantID,
            VariantName:    variant.Name,
            ModelID:        variant.ModelID,
            Metrics:        run.Metrics,
            CriteriaScore:  score,
            CriteriaPassed: passed,
            CriteriaFailed: failed,
            OutputPreview:  truncate(run.Output, 500),
        })
    }

    // Rank by weighted score
    rankings := c.rankVariants(variantReports)

    // Compute diffs from best
    if len(rankings) > 0 {
        bestID := rankings[0].VariantID
        for i := range variantReports {
            if variantReports[i].VariantID != bestID {
                variantReports[i].DiffFromBest = c.computeDiff(bestID, variantReports[i].VariantID)
            }
        }
    }

    return &ComparisonReport{
        ExperimentID: exp.ID,
        Variants:     variantReports,
        Rankings:     rankings,
        Summary:      c.generateSummary(exp, rankings, variantReports),
    }, nil
}

func (c *Comparator) evaluateCriteria(
    run *Run,
    criteria []SuccessCriterion,
) (float64, []string, []string) {
    var totalWeight, earnedWeight float64
    var passed, failed []string

    for _, crit := range criteria {
        totalWeight += crit.Weight

        ok := false
        switch crit.Type {
        case CriterionTestPass:
            ok = c.checkTestPass(run, crit.Target)
        case CriterionFileExists:
            ok = c.checkFileExists(run, crit.Target)
        case CriterionContains:
            ok = strings.Contains(run.Output, crit.Target)
        case CriterionCommand:
            ok = c.checkCommand(run, crit.Target)
        case CriterionManual:
            // Skip for auto-evaluation
            continue
        }

        if ok {
            earnedWeight += crit.Weight
            passed = append(passed, crit.Name)
        } else {
            failed = append(failed, crit.Name)
        }
    }

    if totalWeight == 0 {
        return 1.0, passed, failed
    }
    return earnedWeight / totalWeight, passed, failed
}
```

### 6.2 Report Generation

```go
// pkg/experiment/reporter.go

type Reporter struct {
    comparator *Comparator
}

// GenerateMarkdown creates a human-readable comparison.
func (r *Reporter) GenerateMarkdown(exp *Experiment) (string, error) {
    report, err := r.comparator.Compare(exp)
    if err != nil {
        return "", err
    }

    var buf bytes.Buffer

    fmt.Fprintf(&buf, "# Experiment: %s\n\n", exp.Name)
    fmt.Fprintf(&buf, "**Hypothesis:** %s\n\n", exp.Hypothesis)
    fmt.Fprintf(&buf, "**Task:** %s\n\n", exp.Task.Prompt)

    // Rankings table
    buf.WriteString("## Rankings\n\n")
    buf.WriteString("| Rank | Variant | Model | Score | Cost | Duration |\n")
    buf.WriteString("|------|---------|-------|-------|------|----------|\n")
    for _, r := range report.Rankings {
        v := findVariant(report.Variants, r.VariantID)
        fmt.Fprintf(&buf, "| %d | %s | %s | %.1f%% | $%.4f | %dms |\n",
            r.Rank, v.VariantName, v.ModelID,
            r.Score*100, v.Metrics.TotalCost, v.Metrics.DurationMs)
    }

    // Detailed breakdown per variant
    buf.WriteString("\n## Variant Details\n\n")
    for _, v := range report.Variants {
        fmt.Fprintf(&buf, "### %s (%s)\n\n", v.VariantName, v.ModelID)
        fmt.Fprintf(&buf, "- **Score:** %.1f%%\n", v.CriteriaScore*100)
        fmt.Fprintf(&buf, "- **Tokens:** %d prompt + %d completion\n",
            v.Metrics.PromptTokens, v.Metrics.CompletionTokens)
        fmt.Fprintf(&buf, "- **Tool calls:** %d (%d success, %d failed)\n",
            v.Metrics.ToolCalls, v.Metrics.ToolSuccesses, v.Metrics.ToolFailures)
        fmt.Fprintf(&buf, "- **Files modified:** %d (%d lines)\n",
            v.Metrics.FilesModified, v.Metrics.LinesChanged)

        if len(v.CriteriaPassed) > 0 {
            fmt.Fprintf(&buf, "- **Passed:** %s\n", strings.Join(v.CriteriaPassed, ", "))
        }
        if len(v.CriteriaFailed) > 0 {
            fmt.Fprintf(&buf, "- **Failed:** %s\n", strings.Join(v.CriteriaFailed, ", "))
        }

        buf.WriteString("\n")
    }

    return buf.String(), nil
}
```

---

## 7. CLI Integration

### 7.1 New Commands

Add to `cmd/buckley/`:

```go
// cmd/buckley/experiment.go

var experimentCmd = &cobra.Command{
    Use:   "experiment",
    Short: "Run model comparison experiments",
    Long:  "Execute the same task across multiple models and compare results.",
}

var experimentRunCmd = &cobra.Command{
    Use:   "run [name]",
    Short: "Run an experiment",
    Args:  cobra.ExactArgs(1),
    RunE: func(cmd *cobra.Command, args []string) error {
        name := args[0]

        // Parse flags
        models, _ := cmd.Flags().GetStringSlice("models")
        prompt, _ := cmd.Flags().GetString("prompt")
        timeout, _ := cmd.Flags().GetDuration("timeout")

        // Build experiment
        exp := &experiment.Experiment{
            ID:   ulid.Make().String(),
            Name: name,
            Task: experiment.Task{
                Prompt:  prompt,
                Timeout: timeout,
            },
        }

        // Add variants for each model
        for _, modelID := range models {
            providerID := deps.ModelManager.ProviderIDForModel(modelID)
            exp.Variants = append(exp.Variants, experiment.Variant{
                ID:       ulid.Make().String(),
                Name:     modelID,
                ModelID:  modelID,
                Provider: providerID,
            })
        }

        // Run
        runner := experiment.NewRunner(cfg, deps)
        if err := runner.RunExperiment(cmd.Context(), exp); err != nil {
            return err
        }

        // Generate and print report
        reporter := experiment.NewReporter(comparator)
        report, err := reporter.GenerateMarkdown(exp)
        if err != nil {
            return err
        }

        fmt.Println(report)
        return nil
    },
}

func init() {
    experimentRunCmd.Flags().StringSliceP("models", "m", nil,
        "Models to compare (e.g., -m claude-3.5-sonnet -m gpt-4o)")
    experimentRunCmd.Flags().StringP("prompt", "p", "",
        "Task prompt")
    experimentRunCmd.Flags().Duration("timeout", 30*time.Minute,
        "Timeout per variant")
    experimentRunCmd.MarkFlagRequired("models")
    experimentRunCmd.MarkFlagRequired("prompt")

    experimentCmd.AddCommand(experimentRunCmd)
    experimentCmd.AddCommand(experimentListCmd)
    experimentCmd.AddCommand(experimentShowCmd)
    experimentCmd.AddCommand(experimentReplayCmd)
    rootCmd.AddCommand(experimentCmd)
}
```

### 7.2 TUI Integration

Add experiment widget to sidebar:

```go
// pkg/ui/widgets/experiment.go

type ExperimentWidget struct {
    *base.FocusableBase

    currentExperiment *experiment.Experiment
    runs              []*experiment.Run
    selectedVariant   int
}

func (w *ExperimentWidget) Render(buf *runtime.Buffer, rect runtime.Rect) {
    if w.currentExperiment == nil {
        buf.SetString(rect.X, rect.Y, "No experiment running", nil)
        return
    }

    y := rect.Y

    // Header
    buf.SetString(rect.X, y, fmt.Sprintf("Experiment: %s", w.currentExperiment.Name), headerStyle)
    y++

    // Status per variant
    for i, run := range w.runs {
        style := normalStyle
        if i == w.selectedVariant {
            style = selectedStyle
        }

        status := statusIcon(run.Status)
        variant := findVariant(w.currentExperiment.Variants, run.VariantID)

        line := fmt.Sprintf("%s %s (%s)", status, variant.Name, run.Status)
        if run.Status == experiment.RunCompleted {
            line += fmt.Sprintf(" - $%.4f", run.Metrics.TotalCost)
        }

        buf.SetString(rect.X, y, line, style)
        y++
    }
}
```

---

## 8. Local LLM Provider: Ollama

### 8.1 Ollama Integration

```go
// pkg/model/provider_ollama.go

type OllamaProvider struct {
    baseURL string
    client  *http.Client
}

func NewOllamaProvider(baseURL string, networkLogsEnabled bool) *OllamaProvider {
    if baseURL == "" {
        baseURL = "http://localhost:11434"
    }
    transport := NewLoggingTransportWithEnabled(nil, networkLogsEnabled)
    return &OllamaProvider{
        baseURL: baseURL,
        client:  &http.Client{Timeout: defaultTimeout, Transport: transport},
    }
}

func (p *OllamaProvider) ID() string { return "ollama" }

func (p *OllamaProvider) FetchCatalog() (*ModelCatalog, error) {
    resp, err := p.client.Get(p.baseURL + "/api/tags")
    if err != nil {
        return nil, fmt.Errorf("list models: %w", err)
    }
    defer resp.Body.Close()

    var result struct {
        Models []struct {
            Name       string `json:"name"`
            Size       int64  `json:"size"`
            ModifiedAt string `json:"modified_at"`
        } `json:"models"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }

    models := make([]ModelInfo, len(result.Models))
    for i, m := range result.Models {
        models[i] = ModelInfo{
            ID:            "ollama/" + m.Name,
            Name:          m.Name,
            ContextLength: 8192, // Default; can query /api/show for exact context
            Pricing: ModelPricing{
                Prompt:     0,
                Completion: 0,
            },
        }
    }
    return &ModelCatalog{Data: models}, nil
}

func (p *OllamaProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
    catalog, err := p.FetchCatalog()
    if err != nil {
        return nil, err
    }
    for _, info := range catalog.Data {
        if info.ID == modelID {
            return &info, nil
        }
    }
    return nil, fmt.Errorf("ollama model not found: %s", modelID)
}

func (p *OllamaProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
    req.Stream = false
    // Convert to Ollama's request format, then map response into ChatResponse.
    // (Ollama replies are JSON with message/content/tool_calls fields.)
    // ...
}

func (p *OllamaProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
    req.Stream = true
    // Stream JSON lines from /api/chat and translate to StreamChunk deltas.
    // ...
}
```

---

## 9. LiteLLM Integration

### 9.1 Why LiteLLM

LiteLLM provides a unified interface to 100+ LLM providers with:
- **Single API**: OpenAI-compatible interface for all providers
- **Automatic fallbacks**: Chain multiple providers with retry logic
- **Load balancing**: Distribute requests across provider endpoints
- **Cost tracking**: Built-in token counting and cost calculation
- **Proxy mode**: Run as a server with OpenAI-compatible endpoints

This eliminates the need to implement provider-specific clients while gaining access to models from Anthropic, OpenAI, Google, Cohere, Mistral, Together, Anyscale, Replicate, AWS Bedrock, Azure, and many more.

### 9.2 Integration Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                          Buckley Model Manager                          │
│                                                                         │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────────────┐ │
│  │ OpenRouter      │  │ Ollama          │  │ LiteLLM                 │ │
│  │ (existing)      │  │ (local)         │  │ (100+ providers)        │ │
│  └────────┬────────┘  └────────┬────────┘  └────────────┬────────────┘ │
│           │                    │                        │              │
│           ▼                    ▼                        ▼              │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                    Provider Interface                            │   │
│  │  FetchCatalog() | ChatCompletion() | ChatCompletionStream()      │   │
│  └─────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────┘
```

### 9.3 LiteLLM Provider Implementation

```go
// pkg/model/provider_litellm.go

// LiteLLMProvider connects to a LiteLLM proxy server or uses the library directly.
// LiteLLM exposes an OpenAI-compatible API, so we can reuse OpenAI client logic.
type LiteLLMProvider struct {
    baseURL    string           // LiteLLM proxy URL (e.g., http://localhost:4000)
    apiKey     string           // Optional API key for the proxy
    client     *http.Client
    modelCache []ModelInfo      // Cached model list
    cacheTTL   time.Duration
    cacheTime  time.Time
}

// LiteLLMConfig configures the LiteLLM provider.
type LiteLLMConfig struct {
    // BaseURL is the LiteLLM proxy URL. If empty, defaults to http://localhost:4000
    BaseURL string `yaml:"base_url"`

    // APIKey is the master key for the LiteLLM proxy (if authentication is enabled)
    APIKey string `yaml:"api_key"`

    // Models explicitly lists available models when the proxy doesn't expose /models
    // Format: provider/model (e.g., "anthropic/claude-3-sonnet", "together/llama-3-70b")
    Models []string `yaml:"models"`

    // Fallbacks defines fallback chains for reliability
    // Format: primary -> fallback1 -> fallback2
    Fallbacks map[string][]string `yaml:"fallbacks"`

    // RouterSettings configures LiteLLM's routing behavior
    RouterSettings *LiteLLMRouterSettings `yaml:"router_settings"`
}

type LiteLLMRouterSettings struct {
    // RoutingStrategy: "simple-shuffle", "least-busy", "latency-based", "cost-based"
    RoutingStrategy string `yaml:"routing_strategy"`

    // NumRetries before giving up on a request
    NumRetries int `yaml:"num_retries"`

    // Timeout per request in seconds
    Timeout int `yaml:"timeout"`

    // FallbackModels used when primary fails
    FallbackModels []string `yaml:"fallback_models"`
}

func NewLiteLLMProvider(cfg LiteLLMConfig) *LiteLLMProvider {
    baseURL := cfg.BaseURL
    if baseURL == "" {
        baseURL = "http://localhost:4000"
    }

    return &LiteLLMProvider{
        baseURL:  strings.TrimSuffix(baseURL, "/"),
        apiKey:   cfg.APIKey,
        client:   &http.Client{Timeout: 5 * time.Minute},
        cacheTTL: 5 * time.Minute,
    }
}

func (p *LiteLLMProvider) ID() string { return "litellm" }

// FetchCatalog queries the LiteLLM proxy for available models.
// LiteLLM exposes /models (OpenAI-compatible) and /model/info for detailed metadata.
func (p *LiteLLMProvider) FetchCatalog() (*ModelCatalog, error) {
    // Check cache
    if time.Since(p.cacheTime) < p.cacheTTL && len(p.modelCache) > 0 {
        return &ModelCatalog{Data: p.modelCache}, nil
    }

    // Try /model/info first (LiteLLM-specific, more detailed)
    ctx := context.Background()
    models, err := p.fetchModelInfo(ctx)
    if err != nil {
        // Fallback to /models (OpenAI-compatible)
        models, err = p.fetchModels(ctx)
        if err != nil {
            return nil, fmt.Errorf("list models: %w", err)
        }
    }

    p.modelCache = models
    p.cacheTime = time.Now()
    return &ModelCatalog{Data: models}, nil
}

func (p *LiteLLMProvider) fetchModelInfo(ctx context.Context) ([]ModelInfo, error) {
    req, _ := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/model/info", nil)
    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }

    resp, err := p.client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("model info returned %d", resp.StatusCode)
    }

    // LiteLLM /model/info returns detailed model metadata
    var result struct {
        Data []struct {
            ModelName     string  `json:"model_name"`
            LiteLLMParams struct {
                Model       string `json:"model"`
                APIBase     string `json:"api_base"`
                APIKey      string `json:"api_key"` // Redacted
            } `json:"litellm_params"`
            ModelInfo struct {
                ID                  string  `json:"id"`
                MaxTokens           int     `json:"max_tokens"`
                MaxInputTokens      int     `json:"max_input_tokens"`
                InputCostPerToken   float64 `json:"input_cost_per_token"`
                OutputCostPerToken  float64 `json:"output_cost_per_token"`
                Mode                string  `json:"mode"` // "chat", "completion", "embedding"
                SupportsFunctionCalling bool `json:"supports_function_calling"`
                SupportsVision      bool `json:"supports_vision"`
            } `json:"model_info"`
        } `json:"data"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }

    models := make([]ModelInfo, 0, len(result.Data))
    for _, m := range result.Data {
        // Skip non-chat models
        if m.ModelInfo.Mode != "" && m.ModelInfo.Mode != "chat" {
            continue
        }

        contextLength := m.ModelInfo.MaxInputTokens
        if contextLength == 0 {
            contextLength = m.ModelInfo.MaxTokens
        }
        if contextLength == 0 {
            contextLength = 8192 // Fallback default
        }

        models = append(models, ModelInfo{
            ID:             "litellm/" + m.ModelName,
            Name:           m.ModelName,
            ContextLength:  contextLength,
            Pricing: ModelPricing{
                Prompt:     m.ModelInfo.InputCostPerToken * 1_000_000,
                Completion: m.ModelInfo.OutputCostPerToken * 1_000_000,
            },
            SupportedParameters: func() []string {
                if m.ModelInfo.SupportsFunctionCalling {
                    return []string{"tools", "functions"}
                }
                return nil
            }(),
            Architecture: func() Architecture {
                if m.ModelInfo.SupportsVision {
                    return Architecture{Modality: "text+image"}
                }
                return Architecture{Modality: "text"}
            }(),
        })
    }

    return models, nil
}

func (p *LiteLLMProvider) fetchModels(ctx context.Context) ([]ModelInfo, error) {
    req, _ := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/models", nil)
    if p.apiKey != "" {
        req.Header.Set("Authorization", "Bearer "+p.apiKey)
    }

    resp, err := p.client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    // OpenAI-compatible /models response
    var result struct {
        Data []struct {
            ID      string `json:"id"`
            Object  string `json:"object"`
            OwnedBy string `json:"owned_by"`
        } `json:"data"`
    }

    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }

    models := make([]ModelInfo, 0, len(result.Data))
    for _, m := range result.Data {
        models = append(models, ModelInfo{
            ID:            "litellm/" + m.ID,
            Name:          m.ID,
            ContextLength: 8192, // Default, no metadata available
        })
    }

    return models, nil
}

// GetModelInfo returns cached model metadata when available.
func (p *LiteLLMProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
    catalog, err := p.FetchCatalog()
    if err != nil {
        return nil, err
    }
    for _, info := range catalog.Data {
        if info.ID == modelID {
            return &info, nil
        }
    }
    return nil, fmt.Errorf("litellm model not found: %s", modelID)
}

// ChatCompletion sends a completion request through LiteLLM.
// LiteLLM uses OpenAI-compatible format with model prefix routing.
func (p *LiteLLMProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
    // Strip our provider prefix, keep LiteLLM model routing prefix
    req.Model = strings.TrimPrefix(req.Model, "litellm/")
    req.Stream = false

    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, "POST",
        p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }

    resp, err := p.client.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("chat request: %w", err)
    }
    defer resp.Body.Close()

    if resp.StatusCode != http.StatusOK {
        bodyBytes, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("chat request failed (%d): %s", resp.StatusCode, string(bodyBytes))
    }

    var chatResp ChatResponse
    if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
        return nil, fmt.Errorf("decode response: %w", err)
    }

    return &chatResp, nil
}

// ChatCompletionStream returns a streaming channel for responses.
func (p *LiteLLMProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
    req.Model = strings.TrimPrefix(req.Model, "litellm/")
    req.Stream = true

    body, _ := json.Marshal(req)
    httpReq, _ := http.NewRequestWithContext(ctx, "POST",
        p.baseURL+"/chat/completions", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    httpReq.Header.Set("Accept", "text/event-stream")
    if p.apiKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
    }

    resp, err := p.client.Do(httpReq)
    if err != nil {
        return nil, err
    }

    if resp.StatusCode != http.StatusOK {
        resp.Body.Close()
        return nil, fmt.Errorf("stream request failed: %d", resp.StatusCode)
    }

    chunks := make(chan StreamChunk, 64)
    errChan := make(chan error, 1)
    go func() {
        defer close(chunks)
        defer close(errChan)
        if err := p.processStream(ctx, resp.Body, chunks); err != nil {
            errChan <- err
        }
    }()

    return chunks, errChan
}

func (p *LiteLLMProvider) processStream(ctx context.Context, body io.ReadCloser, chunks chan<- StreamChunk) error {
    defer body.Close()

    reader := bufio.NewReader(body)
    for {
        select {
        case <-ctx.Done():
            return nil
        default:
        }

        line, err := reader.ReadString('\n')
        if err != nil {
            if err != io.EOF {
                return err
            }
            return nil
        }

        line = strings.TrimSpace(line)
        if line == "" || line == "data: [DONE]" {
            continue
        }

        if !strings.HasPrefix(line, "data: ") {
            continue
        }

        data := strings.TrimPrefix(line, "data: ")

        var chunk StreamChunk
        if err := json.Unmarshal([]byte(data), &chunk); err != nil {
            continue
        }
        chunks <- chunk
    }
}
```

### 9.4 LiteLLM Configuration

Add to `pkg/config/config.go`:

```go
type ProviderConfig struct {
    OpenRouter   ProviderSettings  `yaml:"openrouter"`
    OpenAI       ProviderSettings  `yaml:"openai"`
    Anthropic    ProviderSettings  `yaml:"anthropic"`
    Google       ProviderSettings  `yaml:"google"`
    Ollama       ProviderSettings  `yaml:"ollama"`
    LiteLLM      LiteLLMConfig     `yaml:"litellm"`  // NEW
    ModelRouting map[string]string `yaml:"model_routing"`
}

type LiteLLMConfig struct {
    // Enabled controls whether the LiteLLM provider is active
    Enabled bool `yaml:"enabled"`

    // BaseURL is the LiteLLM proxy URL
    BaseURL string `yaml:"base_url"`

    // APIKey for LiteLLM proxy authentication
    APIKey string `yaml:"api_key"`

    // Models to expose (when proxy doesn't list them)
    Models []string `yaml:"models"`

    // Fallbacks for reliability
    Fallbacks map[string][]string `yaml:"fallbacks"`

    // Router settings
    Router *LiteLLMRouterConfig `yaml:"router"`
}

type LiteLLMRouterConfig struct {
    Strategy       string   `yaml:"strategy"`        // simple-shuffle, least-busy, latency-based, cost-based
    NumRetries     int      `yaml:"num_retries"`     // Default: 3
    Timeout        int      `yaml:"timeout_seconds"` // Default: 600
    FallbackModels []string `yaml:"fallback_models"`
}
```

Environment overrides are wired in `applyEnvOverrides`, so add any `BUCKLEY_LITELLM_*`
keys there alongside existing provider key handling.

Example configuration:

```yaml
providers:
  litellm:
    enabled: true
    base_url: http://localhost:4000
    api_key: ${LITELLM_MASTER_KEY}  # Optional, for authenticated proxies

    # Explicit model list (optional if proxy exposes /models)
    models:
      - anthropic/claude-3-5-sonnet-20241022
      - openai/gpt-4o
      - together/meta-llama/Llama-3-70b-chat-hf
      - bedrock/anthropic.claude-3-sonnet-20240229-v1:0
      - vertex_ai/gemini-pro

    # Fallback chains
    fallbacks:
      anthropic/claude-3-5-sonnet:
        - openai/gpt-4o
        - together/meta-llama/Llama-3-70b-chat-hf
      openai/gpt-4o:
        - anthropic/claude-3-5-sonnet

    # Router configuration
    router:
      strategy: cost-based    # Prefer cheaper models when quality is similar
      num_retries: 3
      timeout_seconds: 300
      fallback_models:
        - together/meta-llama/Llama-3-70b-chat-hf
```

### 9.5 Provider Registry Integration

```go
// pkg/model/provider.go

func providerFactory(cfg *config.Config) (map[string]Provider, error) {
    providers := make(map[string]Provider)
    networkLogsEnabled := cfg.Diagnostics.NetworkLogsEnabled

    if cfg.Providers.OpenRouter.Enabled && cfg.Providers.OpenRouter.APIKey != "" {
        client := NewClientWithOptions(cfg.Providers.OpenRouter.APIKey, cfg.Providers.OpenRouter.BaseURL, ClientOptions{
            NetworkLogsEnabled: networkLogsEnabled,
        })
        providers["openrouter"] = &OpenRouterProvider{client: client}
    }
    if cfg.Providers.OpenAI.Enabled && cfg.Providers.OpenAI.APIKey != "" {
        providers["openai"] = NewOpenAIProvider(cfg.Providers.OpenAI.APIKey, cfg.Providers.OpenAI.BaseURL, networkLogsEnabled)
    }
    if cfg.Providers.Anthropic.Enabled && cfg.Providers.Anthropic.APIKey != "" {
        providers["anthropic"] = NewAnthropicProvider(cfg.Providers.Anthropic.APIKey, cfg.Providers.Anthropic.BaseURL, networkLogsEnabled)
    }
    if cfg.Providers.Google.Enabled && cfg.Providers.Google.APIKey != "" {
        providers["google"] = NewGoogleProvider(cfg.Providers.Google.APIKey, cfg.Providers.Google.BaseURL, networkLogsEnabled)
    }

    if cfg.Providers.Ollama.Enabled {
        providers["ollama"] = NewOllamaProvider(cfg.Providers.Ollama.BaseURL, networkLogsEnabled)
    }
    if cfg.Providers.LiteLLM.Enabled {
        providers["litellm"] = NewLiteLLMProvider(cfg.Providers.LiteLLM)
    }

    if len(providers) == 0 {
        return nil, fmt.Errorf("no providers configured")
    }
    return providers, nil
}
```

Model routing already lives in `Manager.ProviderIDForModel` and `providers.model_routing`, so CLI
and runner code should use that instead of adding a new `inferProvider`.

### 9.6 LiteLLM Proxy Deployment

For production use, deploy LiteLLM as a proxy server:

```bash
# Install LiteLLM
pip install litellm[proxy]

# Create config file: litellm_config.yaml
model_list:
  - model_name: claude-3-5-sonnet
    litellm_params:
      model: anthropic/claude-3-5-sonnet-20241022
      api_key: ${ANTHROPIC_API_KEY}

  - model_name: gpt-4o
    litellm_params:
      model: openai/gpt-4o
      api_key: ${OPENAI_API_KEY}

  - model_name: llama-3-70b
    litellm_params:
      model: together_ai/meta-llama/Llama-3-70b-chat-hf
      api_key: ${TOGETHER_API_KEY}

  - model_name: gemini-pro
    litellm_params:
      model: vertex_ai/gemini-pro
      vertex_project: ${GOOGLE_PROJECT_ID}
      vertex_location: us-central1

router_settings:
  routing_strategy: cost-based
  num_retries: 3
  timeout: 300

# Start proxy
litellm --config litellm_config.yaml --port 4000
```

Docker deployment:

```yaml
# docker-compose.yml
services:
  litellm:
    image: ghcr.io/berriai/litellm:main-latest
    ports:
      - "4000:4000"
    volumes:
      - ./litellm_config.yaml:/app/config.yaml
    environment:
      - ANTHROPIC_API_KEY
      - OPENAI_API_KEY
      - TOGETHER_API_KEY
    command: --config /app/config.yaml
```

### 9.7 Model ID Conventions

With LiteLLM, Buckley supports these model ID formats:

| Format | Example | Provider |
|--------|---------|----------|
| `litellm/<name>` | `litellm/claude-3-5-sonnet` | LiteLLM proxy (uses proxy's model mapping) |
| `litellm/<provider>/<model>` | `litellm/anthropic/claude-3-5-sonnet` | LiteLLM with explicit provider routing |
| `ollama/<model>` | `ollama/codellama:34b` | Local Ollama |
| `openrouter/<model>` | `openrouter/anthropic/claude-3-5-sonnet` | OpenRouter |
| Native | `gpt-4o`, `claude-3-5-sonnet` | Auto-detected provider |

---

## 10. Session Replay

### 10.1 Replay Architecture

```go
// pkg/experiment/replay.go

type Replayer struct {
    store        *storage.Store
    runner       *Runner
}

// ReplayConfig specifies how to replay a session.
type ReplayConfig struct {
    SourceSessionID string
    NewModelID      string
    NewProvider     string
    NewSystemPrompt *string
    NewTemperature  *float64
    // If true, replay tool calls with same inputs
    // If false, let model decide tool usage
    DeterministicTools bool
}

// Replay re-executes a session with different configuration.
func (r *Replayer) Replay(ctx context.Context, cfg ReplayConfig) (*Run, error) {
    // Load original session
    session, err := r.store.GetSession(cfg.SourceSessionID)
    if err != nil {
        return nil, fmt.Errorf("load session: %w", err)
    }

    messages, err := r.store.GetAllMessages(cfg.SourceSessionID)
    if err != nil {
        return nil, fmt.Errorf("load messages: %w", err)
    }

    // Extract the original user prompts
    var userPrompts []string
    for _, msg := range messages {
        if msg.Role == "user" {
            userPrompts = append(userPrompts, msg.Content)
        }
    }

    if len(userPrompts) == 0 {
        return nil, fmt.Errorf("no user prompts in session")
    }

    // Create experiment with single variant
    exp := &Experiment{
        ID:   ulid.Make().String(),
        Name: fmt.Sprintf("Replay %s with %s", cfg.SourceSessionID[:8], cfg.NewModelID),
        Task: Task{
            Prompt: userPrompts[0], // Primary task
        },
        Variants: []Variant{{
            ID:           ulid.Make().String(),
            Name:         "replay",
            ModelID:      cfg.NewModelID,
            Provider:     cfg.NewProvider,
            SystemPrompt: cfg.NewSystemPrompt,
            Temperature:  cfg.NewTemperature,
        }},
    }

    // If deterministic, inject original tool responses
    if cfg.DeterministicTools {
        exp.Task.Context = map[string]string{
            "replay_mode":       "deterministic",
            "source_session_id": cfg.SourceSessionID,
        }
    }

    // Run and return the single result
    if err := r.runner.RunExperiment(ctx, exp); err != nil {
        return nil, err
    }

    runs, _ := r.store.GetRunsForExperiment(exp.ID)
    if len(runs) == 0 {
        return nil, fmt.Errorf("no runs created")
    }

    return runs[0], nil
}
```

---

## 11. Telemetry Aggregation

### 11.1 Metrics Collector

```go
// pkg/experiment/metrics.go

type MetricsCollector struct {
    hub   *telemetry.Hub
    store *Store

    mu              sync.Mutex
    currentMetrics  map[string]*RunMetrics // sessionID -> metrics
}

func NewMetricsCollector(hub *telemetry.Hub, store *Store) *MetricsCollector {
    c := &MetricsCollector{
        hub:            hub,
        store:          store,
        currentMetrics: make(map[string]*RunMetrics),
    }

    // Subscribe to telemetry
    events, unsub := hub.Subscribe()
    go c.processEvents(events, unsub)

    return c
}

func (c *MetricsCollector) processEvents(events <-chan telemetry.Event, unsub func()) {
    defer unsub()

    for event := range events {
        c.mu.Lock()

        runKey := event.SessionID
        if runKey == "" {
            runKey = event.TaskID
        }
        if runKey == "" {
            c.mu.Unlock()
            continue
        }

        metrics, ok := c.currentMetrics[runKey]
        if !ok {
            metrics = &RunMetrics{}
            c.currentMetrics[runKey] = metrics
        }

        switch event.Type {
        case telemetry.EventToolStarted:
            metrics.ToolCalls++

        case telemetry.EventToolCompleted:
            metrics.ToolSuccesses++

        case telemetry.EventToolFailed:
            metrics.ToolFailures++

        case telemetry.EventCostUpdated:
            if cost, ok := event.Data["cost"].(float64); ok {
                metrics.TotalCost = cost
            }

        case telemetry.EventTokenUsageUpdated:
            if prompt, ok := event.Data["prompt_tokens"].(int); ok {
                metrics.PromptTokens = prompt
            }
            if completion, ok := event.Data["completion_tokens"].(int); ok {
                metrics.CompletionTokens = completion
            }
        }

        c.mu.Unlock()
    }
}

// Flush persists accumulated metrics for a session/run key.
func (c *MetricsCollector) Flush(runKey string) error {
    c.mu.Lock()
    metrics, ok := c.currentMetrics[runKey]
    if !ok {
        c.mu.Unlock()
        return nil
    }
    delete(c.currentMetrics, runKey)
    c.mu.Unlock()

    return c.store.UpdateRunMetricsBySession(runKey, metrics)
}
```

---

## 12. Configuration

Add to `pkg/config/config.go`:

```go
type Config struct {
    // ... existing fields

    Experiment ExperimentConfig `yaml:"experiment"`
}

type ExperimentConfig struct {
    Enabled         bool          `yaml:"enabled"`
    MaxConcurrent   int           `yaml:"max_concurrent"`
    DefaultTimeout  time.Duration `yaml:"default_timeout"`
    WorktreeRoot    string        `yaml:"worktree_root"`
    CleanupOnDone   bool          `yaml:"cleanup_on_done"`

    // Budget limits
    MaxCostPerRun   float64       `yaml:"max_cost_per_run"`
    MaxTokensPerRun int           `yaml:"max_tokens_per_run"`
}
```

Environment overrides are handled in `applyEnvOverrides` (not struct tags), so wire any new
`BUCKLEY_EXPERIMENT_*` keys there.

Default config:

```yaml
experiment:
  enabled: true
  max_concurrent: 4
  default_timeout: 30m
  worktree_root: .buckley/experiments/
  cleanup_on_done: true
  max_cost_per_run: 1.00
  max_tokens_per_run: 100000

providers:
  ollama:
    enabled: true
    base_url: http://localhost:11434

  litellm:
    enabled: false  # Enable when proxy is running
    base_url: http://localhost:4000
```

---

## 13. Implementation Plan

### MVP Release (48 hours)

**This is what ships. Everything else is post-launch.**

#### MVP-1: CLI Command (8 hours)
```
cmd/buckley/experiment.go
├── experimentCmd (cobra.Command)
├── experimentRunCmd
│   ├── --models (-m) flag ([]string)
│   ├── --prompt (-p) flag (string)
│   ├── --timeout flag (duration)
│   └── --max-concurrent flag (int, default 4)
└── Wire to pkg/parallel/Orchestrator
```

- [x] Create `cmd/buckley/experiment.go`
- [x] Add `experiment run` subcommand
- [x] Parse model flags into `parallel.AgentTask` list
- [x] Create `TaskExecutor` that invokes model + tools
- [x] Print results as markdown table

#### MVP-2: Ollama Provider (8 hours)
```
pkg/model/provider_ollama.go
├── OllamaProvider struct
├── FetchCatalog() - GET /api/tags
├── ChatCompletion() - POST /api/chat
└── ChatCompletionStream() - POST /api/chat with stream:true
```

- [x] Implement `pkg/model/provider_ollama.go` (~200 lines)
- [x] Add to provider registry in `manager.go`
- [x] Add config: `providers.ollama.enabled`, `providers.ollama.base_url`
- [ ] Test with llama3, codellama

#### MVP-3: Basic Reporter (4 hours)
```
pkg/experiment/reporter.go
├── FormatMarkdownTable(results []parallel.AgentResult) string
└── PrintSummary(results) - to stdout
```

- [x] Create `pkg/experiment/reporter.go`
- [x] Generate markdown table: Model | Success | Duration | Tokens | Files
- [x] Print to stdout after run completes

#### MVP-4: Integration + Release (4 hours)
- [ ] End-to-end test with 2+ models
- [ ] Update README with `buckley experiment run` examples
- [ ] Tag release

**MVP Total: ~24 hours of focused work**

---

### Post-MVP Phases

#### Phase A: Persistence (Medium Priority)
- [x] Add `experiments`, `experiment_variants`, `experiment_runs` tables
- [x] Implement `pkg/experiment/store.go`
- [x] Add `buckley experiment list/show` commands
- [x] Persist results for later comparison

#### Phase B: LiteLLM Integration (Medium Priority)
- [x] Implement `pkg/model/provider_litellm.go`
- [x] Add configuration with fallback chains
- [ ] Document proxy deployment
- [ ] Test with 5+ providers

#### Phase C: Advanced Comparison (Low Priority)
- [x] Implement `pkg/experiment/comparator.go`
- [x] Add success criteria evaluation
- [ ] Semantic diff of outputs
- [ ] Cost/performance charts

#### Phase D: TUI Widget (Low Priority)
- [x] Add experiment widget to sidebar
- [x] Real-time progress per variant
- [ ] Inline result comparison

#### Phase E: Replay (Low Priority)
- [x] Implement `pkg/experiment/replay.go`
- [x] Add `buckley experiment replay` command
- [ ] Deterministic vs non-deterministic modes

---

## 14. Success Criteria

### MVP Ship Criteria (Required)

1. **`buckley experiment run -m claude-3.5-sonnet -m ollama/codellama -p "Add dark mode"`** executes both in parallel with isolated worktrees

2. **Markdown table** prints to stdout: Model | Success | Duration | Tokens

3. **Ollama works** identically to cloud models (same tool interface)

4. **Tests pass** for new code

### Full Platform Criteria (Post-MVP)

5. **TUI sidebar** shows real-time progress of all variants

6. **Replay** can re-run any session with different model/config

7. **LiteLLM** provides access to 100+ providers

8. **Budget limits** prevent runaway costs

9. **80%+ coverage** on new packages

---

## 15. Release Checklist

```bash
# Pre-release verification
./scripts/test.sh                           # All tests pass
go build -o buckley ./cmd/buckley           # Builds cleanly
./buckley experiment run --help             # CLI works

# Smoke test
export OPENROUTER_API_KEY=...
./buckley experiment run "test" \
    -m openrouter/anthropic/claude-3.5-sonnet \
    -m ollama/llama3 \
    -p "Write a hello world function"

# Release
git tag v0.X.0
git push origin v0.X.0

# Post-release
# - Update CHANGELOG.md
# - Post to relevant communities
# - Monitor issue tracker
```

---

## Appendix A: Example Usage

```bash
# Compare Claude vs GPT-4o vs local Codellama on a refactoring task
buckley experiment run "refactor-auth" \
    -m anthropic/claude-3.5-sonnet \
    -m openai/gpt-4o \
    -m ollama/codellama:34b \
    -p "Refactor the authentication module to use JWT instead of sessions" \
# Criteria flags are post-MVP
    --criteria "test_pass:go test ./pkg/auth/..." \
    --criteria "file_exists:pkg/auth/jwt.go" \
    --timeout 20m

# Use LiteLLM for access to exotic models
buckley experiment run "multilingual-i18n" \
    -m litellm/anthropic/claude-3-5-sonnet \
    -m litellm/together/meta-llama/Llama-3-70b-chat-hf \
    -m litellm/bedrock/anthropic.claude-3-sonnet \
    -p "Add internationalization support for French and German"

# View results
buckley experiment show refactor-auth

# Replay best result with different temperature
buckley experiment replay refactor-auth \
    --variant claude-3.5-sonnet \
    --temperature 0.2

# List all experiments
buckley experiment list --status completed
```

---

## Appendix B: TUI Mockup

```
┌─────────────────────────────────────────────────────────────────────────┐
│ Buckley                                              Experiment Running │
├─────────────────────────────────────────────────────┬───────────────────┤
│                                                     │ Experiment        │
│  Running experiment: refactor-auth                  │                   │
│                                                     │ ● claude-3.5      │
│  Task: Refactor authentication module to use JWT    │   Running... 2m   │
│                                                     │   $0.0234 | 4.2k  │
│  ┌─────────────────────────────────────────────┐   │                   │
│  │ ✓ claude-3.5-sonnet  COMPLETED   $0.0234    │   │ ● gpt-4o          │
│  │   Score: 100% | 4.2k tokens | 3m 42s        │   │   Running... 1m   │
│  │   ✓ test_pass  ✓ file_exists                │   │   $0.0156 | 3.1k  │
│  ├─────────────────────────────────────────────┤   │                   │
│  │ ◐ gpt-4o             RUNNING                │   │ ○ codellama       │
│  │   $0.0156 | 3.1k tokens | 1m 23s            │   │   Pending         │
│  ├─────────────────────────────────────────────┤   │                   │
│  │ ○ ollama/codellama   PENDING                │   ├───────────────────┤
│  │   Waiting for slot...                       │   │ Criteria          │
│  └─────────────────────────────────────────────┘   │                   │
│                                                     │ ✓ test_pass       │
│                                                     │ ✓ file_exists     │
├─────────────────────────────────────────────────────┴───────────────────┤
│ > _                                                                     │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## Appendix C: LiteLLM Model Naming Reference

| Provider | LiteLLM Format | Example |
|----------|----------------|---------|
| Anthropic | `anthropic/<model>` | `anthropic/claude-3-5-sonnet-20241022` |
| OpenAI | `openai/<model>` | `openai/gpt-4o` |
| Together | `together_ai/<model>` | `together_ai/meta-llama/Llama-3-70b-chat-hf` |
| AWS Bedrock | `bedrock/<model>` | `bedrock/anthropic.claude-3-sonnet-20240229-v1:0` |
| Google Vertex | `vertex_ai/<model>` | `vertex_ai/gemini-pro` |
| Azure | `azure/<deployment>` | `azure/gpt-4-deployment` |
| Cohere | `cohere/<model>` | `cohere/command-r-plus` |
| Mistral | `mistral/<model>` | `mistral/mistral-large-latest` |
| Groq | `groq/<model>` | `groq/llama3-70b-8192` |
| Replicate | `replicate/<model>` | `replicate/meta/llama-2-70b-chat` |
| Ollama (via LiteLLM) | `ollama/<model>` | `ollama/codellama:34b` |
| HuggingFace | `huggingface/<model>` | `huggingface/bigcode/starcoder` |
| vLLM | `openai/<model>` + custom base | Custom deployment |

See [LiteLLM Provider Docs](https://docs.litellm.ai/docs/providers) for complete list.

---

## Appendix D: Project Status Summary

### Codebase Metrics (as of d143dfd)

| Metric | Value |
|--------|-------|
| Go code | ~216K lines |
| Go files | 719 |
| Packages | 67 |
| Web UI (TSX/TS) | ~8.6K lines |
| Test coverage (avg) | ~65% |
| Test coverage (core) | 80%+ |

### Architecture Validation

| Principle | Status | Evidence |
|-----------|--------|----------|
| DDD Bounded Contexts | ✅ | 67 packages with clear domain boundaries |
| Clean Architecture | ✅ | Dependencies flow inward (domain → infra) |
| Provider Abstraction | ✅ | 4 providers (OpenRouter, OpenAI, Anthropic, Google) |
| Event-Driven | ✅ | Telemetry hub with 20+ event types |
| Persistence | ✅ | SQLite with 20+ tables, WAL mode |

### Test Coverage by Package (Recent)

| Package | Coverage | Notes |
|---------|----------|-------|
| agentserver | 100% | |
| utils | 100% | |
| telemetry | 100% | |
| discovery | 96.1% | |
| pubsub | 94.3% | |
| diff | 95.5% | |
| context | 90.9% | |
| coordination/security | 86.5% | |
| coordination/capabilities | 80.0% | |
| cost | 79.2% | |
| conversation | 70.2% | |
| model | 61.3% | |
| storage | 60.6% | |

### What Makes This Shippable

1. **Core infrastructure is battle-tested** — Orchestrator, telemetry, storage all have real usage
2. **Dual UI (TUI + Web)** — Not locked to one interface
3. **Provider-agnostic** — OpenRouter gives 100+ models day one
4. **Self-healing executor** — Retries with loop detection
5. **Mission control approvals** — Human-in-the-loop for file changes
6. **Clean Architecture** — Easy to extend without breaking things

### What's NOT Shippable Yet

1. **No `buckley experiment` command** — Parallel orchestrator exists but isn't CLI-accessible
2. **No local LLM support** — Ollama provider not implemented
3. **No experiment results** — Reporter not implemented

### Ship Decision

**Ship when:**
- [ ] `buckley experiment run` works end-to-end
- [ ] Ollama provider passes smoke test
- [ ] README documents the feature
- [ ] No P0 bugs in existing functionality

**Don't block on:**
- LiteLLM (100+ providers via OpenRouter already)
- TUI experiment widget (CLI is enough)
- Session replay (post-launch feature)
- Semantic comparison (markdown table is enough)

---

## Appendix E: Quick Reference

### CLI Commands (Current)

```bash
buckley                     # Interactive TUI
buckley serve               # Start IPC server (headless)
buckley serve --browser     # Start with web UI
buckley commit              # AI-generated commit message
buckley pr                  # AI-generated PR description
buckley doctor              # System health check
```

### CLI Commands (After MVP)

```bash
buckley experiment run "name" -m model1 -m model2 -p "prompt"
buckley experiment list
buckley experiment show <id>
```

### Environment Variables

```bash
OPENROUTER_API_KEY          # Required for cloud models
OPENAI_API_KEY             # Optional (if OpenAI provider enabled)
ANTHROPIC_API_KEY          # Optional (if Anthropic provider enabled)
GOOGLE_API_KEY             # Optional (if Google provider enabled)
BUCKLEY_DB_PATH             # SQLite database location
BUCKLEY_LOG_DIR             # Telemetry log directory
BUCKLEY_EXPERIMENT_ENABLED  # Planned: enable experiments (wire in applyEnvOverrides)
BUCKLEY_OLLAMA_ENABLED      # Planned: enable local LLM support
BUCKLEY_OLLAMA_BASE_URL     # Planned: Ollama server URL (default: http://localhost:11434)
BUCKLEY_LITELLM_ENABLED     # Planned: enable LiteLLM provider
LITELLM_BASE_URL            # Planned: LiteLLM proxy URL
LITELLM_API_KEY             # Planned: LiteLLM proxy API key
```

### Key Files for MVP Implementation

```
cmd/buckley/experiment.go       # NEW - CLI command
pkg/model/provider_ollama.go    # NEW - Ollama provider
pkg/experiment/reporter.go      # NEW - Markdown reporter
pkg/parallel/agents.go          # EXISTS - Wire to this
pkg/model/manager.go            # EXISTS - Add Ollama here
```
