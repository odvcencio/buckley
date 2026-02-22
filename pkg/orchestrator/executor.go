package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/encoding/toon"
	"github.com/odvcencio/buckley/pkg/personality"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
)

type reviewerAgent interface {
	Review(task *Task, builderResult *BuilderResult) (*ReviewResult, error)
	SetPersonaProvider(provider *personality.PersonaProvider)
}

type verifierAgent interface {
	VerifyOutcomes(task *Task, result *VerifyResult) error
}

type Executor struct {
	plan             *Plan
	currentTask      *Task
	ctx              context.Context
	cancel           context.CancelFunc
	store            *storage.Store
	modelClient      ModelClient
	toolRegistry     *tool.Registry
	config           *config.Config
	planner          *Planner
	validator        *Validator
	verifier         verifierAgent
	builder          *BuilderAgent
	reviewer         reviewerAgent
	workflow         *WorkflowManager
	batchCoordinator *BatchCoordinator
	issuesCodec      *toon.Codec

	maxRetries      int
	maxReviewCycles int
	retryCount      int
	retryContext    *RetryContext
	taskPhases      []TaskPhase
}

func (e *Executor) baseContext() context.Context {
	if e == nil || e.ctx == nil {
		return context.Background()
	}
	return e.ctx
}

// SetPersonaProvider updates sub-agents with refreshed persona definitions.
func (e *Executor) SetPersonaProvider(provider *personality.PersonaProvider) {
	if e == nil || e.reviewer == nil {
		return
	}
	e.reviewer.SetPersonaProvider(provider)
}

type taskExecution struct {
	id               int64
	startTime        time.Time
	validationErrors string
}

// RetryContext tracks retry progress to detect dead-end loops
type RetryContext struct {
	Attempt       int
	PreviousError string
	Changed       bool // Did this retry change anything?
	FilesBefore   map[string]bool
}

func NewExecutor(plan *Plan, store *storage.Store, mgr ModelClient, registry *tool.Registry, cfg *config.Config, planner *Planner, workflow *WorkflowManager, batchCoordinator *BatchCoordinator) *Executor {
	phases := resolveTaskPhases(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	var reviewer reviewerAgent
	if rev := NewReviewAgent(plan, cfg, mgr, registry, workflow); rev != nil {
		reviewer = rev
	}
	var verifier verifierAgent
	if v := NewVerifier(registry); v != nil {
		verifier = v
	}
	return &Executor{
		plan:         plan,
		store:        store,
		modelClient:  mgr,
		toolRegistry: registry,
		config:       cfg,
		planner:      planner,
		validator: func() *Validator {
			if workflow != nil {
				return NewValidator(registry, workflow.projectRoot)
			}
			return NewValidator(registry, "")
		}(),
		verifier:         verifier,
		builder:          NewBuilderAgent(plan, cfg, mgr, registry, workflow),
		reviewer:         reviewer,
		workflow:         workflow,
		batchCoordinator: batchCoordinator,
		maxRetries:       cfg.Orchestrator.MaxSelfHealAttempts,
		maxReviewCycles:  cfg.Orchestrator.MaxReviewCycles,
		issuesCodec:      toon.New(cfg.Encoding.UseToon),
		taskPhases:       phases,
		ctx:              ctx,
		cancel:           cancel,
	}
}

// SetContext replaces the executor context (used for external cancellation).
func (e *Executor) SetContext(ctx context.Context) {
	if e == nil || ctx == nil {
		return
	}
	if e.cancel != nil {
		e.cancel()
	}
	e.ctx, e.cancel = context.WithCancel(ctx)
	if e.builder != nil {
		e.builder.SetContext(e.ctx)
	}
	if e.validator != nil {
		e.validator.SetContext(e.ctx)
	}
	if e.verifier != nil {
		if setter, ok := e.verifier.(interface{ SetContext(context.Context) }); ok {
			setter.SetContext(e.ctx)
		}
	}
	if e.reviewer != nil {
		if setter, ok := e.reviewer.(interface{ SetContext(context.Context) }); ok {
			setter.SetContext(e.ctx)
		}
	}
}

// Cancel stops any in-flight execution.
func (e *Executor) Cancel() {
	if e.cancel != nil {
		e.cancel()
	}
}

func (e *Executor) Execute() error {
	for i := range e.plan.Tasks {
		task := &e.plan.Tasks[i]

		if err := e.ctx.Err(); err != nil {
			if e.workflow != nil {
				e.workflow.SendProgress(fmt.Sprintf("⏹️ Execution cancelled: %v", err))
			}
			return fmt.Errorf("execution cancelled: %w", err)
		}

		// Skip if already completed
		if task.Status == TaskCompleted {
			continue
		}

		// Check dependencies
		if !e.dependenciesMet(task) {
			return fmt.Errorf("task %s has unmet dependencies", task.ID)
		}

		// Execute task
		e.currentTask = task
		if err := e.executeTask(task); err != nil {
			task.Status = TaskFailed
			e.planner.UpdatePlan(e.plan)
			return fmt.Errorf("task %s failed: %w", task.ID, err)
		}

		// Save progress
		if err := e.saveProgress(); err != nil {
			return fmt.Errorf("failed to save progress: %w", err)
		}
	}

	return nil
}

func (e *Executor) executeTask(task *Task) error {
	if err := e.ctx.Err(); err != nil {
		return fmt.Errorf("task %s cancelled: %w", task.ID, err)
	}
	if e.batchCoordinator != nil && e.batchCoordinator.Enabled() {
		return e.executeTaskInBatch(task)
	}
	return e.executeTaskLocal(task)
}

func (e *Executor) executeTaskLocal(task *Task) error {
	exec := e.beginTaskExecution(task)

	_, validationSummary, err := e.performValidation(task)
	if err != nil {
		return e.failExecution(exec, task, nil, err)
	}
	exec.validationErrors = validationSummary

	var builderResult *BuilderResult
	var verifyResult *VerifyResult

	for _, phase := range e.taskPhases {
		switch phase.Stage {
		case "builder":
			builderResult, err = e.runBuilderPhase(task)
			if err != nil {
				return e.failExecution(exec, task, verifyResult, err)
			}
		case "verify":
			if builderResult == nil {
				builderResult, err = e.runBuilderPhase(task)
				if err != nil {
					return e.failExecution(exec, task, verifyResult, err)
				}
			}
			verifyResult, err = e.runVerificationPhase(task)
			if err != nil {
				return e.failExecution(exec, task, verifyResult, err)
			}
		case "review":
			if builderResult == nil {
				builderResult, err = e.runBuilderPhase(task)
				if err != nil {
					return e.failExecution(exec, task, verifyResult, err)
				}
			}
			if err := e.runReviewPhase(task, builderResult); err != nil {
				return e.failExecution(exec, task, verifyResult, fmt.Errorf("review failed: %w", err))
			}
		}
	}

	return e.completeExecution(exec, task, verifyResult)
}
