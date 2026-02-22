package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/artifact"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/personality"
	"github.com/odvcencio/buckley/pkg/prompts"
	"github.com/odvcencio/buckley/pkg/skill"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// WorkflowPhase represents the current phase of the workflow
type WorkflowPhase string

const (
	WorkflowPhasePlanning  WorkflowPhase = "planning"
	WorkflowPhaseExecution WorkflowPhase = "execution"
	WorkflowPhaseReview    WorkflowPhase = "review"
)

// WorkflowManager manages the three-phase development workflow with artifacts
type WorkflowManager struct {
	config          *config.Config
	modelClient     ModelClient
	toolRegistry    *tool.Registry
	store           *storage.Store
	promptGenerator *prompts.Generator
	artifacts       *artifactPipeline

	executionTracker *artifact.ExecutionTracker

	// Current workflow state
	stateMu              sync.RWMutex // Protects currentPhase, feature, planningArtifact, planRef, planID, currentAgent, reviewArtifact, latestResearchBrief, steeringNotes, autonomyLevel, skillMessages, sessionID
	currentPhase         WorkflowPhase
	feature              string
	planningArtifact     *artifact.PlanningArtifact
	reviewArtifact       *artifact.ReviewArtifact
	currentAgent         string
	latestResearchBrief  *artifact.ResearchBrief

	// Activity tracking
	activityTracker *tool.ActivityTracker
	intentHistory   *tool.IntentHistory

	researchAgent *ResearchAgent
	pauseMu       sync.RWMutex // Protects pause state
	paused        bool
	pauseReason   string
	pauseQuestion string
	pauseAt       time.Time

	// Skill management
	skillManager  *SkillManager
	skillRegistry *skill.Registry
	skillState    *skill.RuntimeState
	skillMessages []string
	skillInjector func(string)

	// Progress streaming
	progressMu   sync.Mutex
	progressChan chan string

	telemetry *telemetry.Hub
	sessionID string
	planID    string
	planRef   *Plan

	planningPhase  *planningController
	executionPhase *executionController
	reviewPhase    *reviewController

	personaProvider *personality.PersonaProvider
	projectRoot     string
	taskPhases      []TaskPhase

	steeringNotes string
	autonomyLevel string
}

// ErrWorkflowPaused indicates automation halted pending user input.
var ErrWorkflowPaused = errors.New("workflow paused for manual review")

// WorkflowPauseError wraps pause metadata.
type WorkflowPauseError struct {
	Reason   string
	Question string
}

func (e *WorkflowPauseError) Error() string {
	return fmt.Sprintf("%s: %s", e.Reason, e.Question)
}

func (e *WorkflowPauseError) Unwrap() error {
	return ErrWorkflowPaused
}

// NewWorkflowManager creates a new workflow manager
func NewWorkflowManager(
	cfg *config.Config,
	mgr ModelClient,
	registry *tool.Registry,
	store *storage.Store,
	docsRoot string,
	projectRoot string,
	telemetryHub *telemetry.Hub,
) *WorkflowManager {
	personaProvider := BuildPersonaProvider(cfg, projectRoot)
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		root = config.ResolveProjectRoot(cfg)
	}
	w := &WorkflowManager{
		config:          cfg,
		modelClient:     mgr,
		toolRegistry:    registry,
		store:           store,
		promptGenerator: prompts.NewGenerator(prompts.WithPersonaProvider(personaProvider)),
		artifacts:       newArtifactPipeline(cfg, docsRoot),
		activityTracker: tool.NewActivityTracker(tool.DefaultActivityGroupingConfig()),
		intentHistory:   tool.NewIntentHistory(),
		currentPhase:    WorkflowPhasePlanning,
		currentAgent:    "Planning",
		telemetry:       telemetryHub,
		personaProvider: personaProvider,
		projectRoot:     root,
		taskPhases:      resolveTaskPhases(cfg),
		autonomyLevel:   cfg.Orchestrator.TrustLevel,
	}
	w.loadPersonaOverrides()

	w.researchAgent = NewResearchAgent(store, mgr, projectRoot, filepath.Join(docsRoot, "research"), cfg.Models.Planning, w, cfg.Encoding.UseToon)
	w.planningPhase = newPlanningController(w)
	w.executionPhase = newExecutionController(w)
	w.reviewPhase = newReviewController(w)

	skills := skill.NewRegistry()
	if err := skills.LoadAll(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load skills: %v\n", err)
	}
	w.skillRegistry = skills
	w.skillInjector = func(msg string) {
		w.stateMu.Lock()
		w.skillMessages = append(w.skillMessages, msg)
		w.stateMu.Unlock()
	}
	w.skillState = skill.NewRuntimeState(w.skillInjector)
	w.skillManager = NewSkillManager(skills, w.skillState)
	if registry != nil {
		registry.Register(&builtin.SkillActivationTool{
			Registry:     skills,
			Conversation: w.skillState,
		})
		createTool := &builtin.CreateSkillTool{Registry: skills}
		if strings.TrimSpace(root) != "" {
			createTool.SetWorkDir(root)
		}
		registry.Register(createTool)
	}

	return w
}

// SetSessionID assigns the active session for telemetry purposes.
// Also restores any paused workflow state from the database.
func (w *WorkflowManager) SetSessionID(sessionID string) {
	if w == nil {
		return
	}
	w.stateMu.Lock()
	w.sessionID = strings.TrimSpace(sessionID)
	w.stateMu.Unlock()
	w.loadSteeringSettings()
	w.RestorePauseStateFromSession()
}

// SetCurrentPlan stores the active plan reference and ID for telemetry snapshots.
func (w *WorkflowManager) SetCurrentPlan(plan *Plan) {
	if w == nil {
		return
	}
	w.stateMu.Lock()
	w.planRef = plan
	var planID string
	if plan != nil {
		w.planID = plan.ID
		planID = plan.ID
	}
	sessionID := w.sessionID
	w.stateMu.Unlock()

	if planID != "" && w.store != nil && sessionID != "" {
		if err := w.store.LinkSessionToPlan(sessionID, planID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to link session %s to plan %s: %v\n", sessionID, planID, err)
		}
	}
}

// InitializeDocumentation creates the documentation hierarchy if it doesn't exist
func (w *WorkflowManager) InitializeDocumentation() error {
	if w.artifacts == nil {
		return fmt.Errorf("artifact pipeline unavailable")
	}
	return w.artifacts.ensureDocs()
}

// SetSkillManager sets the skill manager for workflow skill activation
func (w *WorkflowManager) SetSkillManager(sm *SkillManager) {
	w.skillManager = sm
}

// StartPlanning initiates the planning phase for a new feature
func (w *WorkflowManager) StartPlanning(ctx context.Context, featureName, userGoal string) error {
	if w == nil || w.planningPhase == nil {
		return fmt.Errorf("planning phase controller not initialized")
	}
	return w.planningPhase.start(ctx, featureName, userGoal)
}

// AddPlanningContext adds context information discovered during planning
func (w *WorkflowManager) AddPlanningContext(patterns []string, archStyle string, files []string) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.planningArtifact != nil {
		w.planningArtifact.Context.ExistingPatterns = patterns
		w.planningArtifact.Context.ArchitectureStyle = archStyle
		w.planningArtifact.Context.RelevantFiles = files
	}
}

// AddArchitectureDecision adds an ADR to the planning artifact
func (w *WorkflowManager) AddArchitectureDecision(decision artifact.ArchitectureDecision) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.planningArtifact != nil {
		w.planningArtifact.Decisions = append(w.planningArtifact.Decisions, decision)
	}
}

// AddCodeContract adds a code contract to the planning artifact
func (w *WorkflowManager) AddCodeContract(contract artifact.CodeContract) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.planningArtifact != nil {
		w.planningArtifact.CodeContracts = append(w.planningArtifact.CodeContracts, contract)
	}
}

// SetLayerMap sets the layer mapping for the planning artifact
func (w *WorkflowManager) SetLayerMap(layerMap artifact.LayerMap) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.planningArtifact != nil {
		w.planningArtifact.LayerMap = layerMap
	}
}

// AddTask adds a task to the planning artifact
func (w *WorkflowManager) AddTask(task artifact.TaskBreakdown) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.planningArtifact != nil {
		w.planningArtifact.Tasks = append(w.planningArtifact.Tasks, task)
	}
}

// SetCrossCuttingConcerns sets cross-cutting concerns for the planning artifact
func (w *WorkflowManager) SetCrossCuttingConcerns(concerns artifact.CrossCuttingConcerns) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.planningArtifact != nil {
		w.planningArtifact.CrossCuttingScope = concerns
	}
}

// FinalizePlanning generates and saves the planning artifact
func (w *WorkflowManager) FinalizePlanning() (string, error) {
	w.stateMu.Lock()
	pa := w.planningArtifact
	if pa == nil {
		w.stateMu.Unlock()
		return "", fmt.Errorf("no planning artifact to finalize")
	}

	pa.Status = "completed"
	pa.UpdatedAt = time.Now()
	w.stateMu.Unlock()

	// Generate artifact file
	if w.artifacts == nil {
		return "", fmt.Errorf("artifact pipeline unavailable")
	}
	filePath, err := w.artifacts.planningGenerator().Generate(pa)
	if err != nil {
		return "", fmt.Errorf("failed to generate planning artifact: %w", err)
	}

	return filePath, nil
}

// StartExecution initiates the execution phase
func (w *WorkflowManager) StartExecution(planningArtifactPath string) error {
	if w == nil || w.executionPhase == nil {
		return fmt.Errorf("execution phase controller not initialized")
	}
	return w.executionPhase.start(planningArtifactPath)
}

// RecordTaskProgress records progress on a task during execution
func (w *WorkflowManager) RecordTaskProgress(progress artifact.TaskProgress) error {
	if w.executionTracker == nil {
		return fmt.Errorf("execution not started")
	}

	return w.executionTracker.AddTaskProgress(progress)
}

// RecordExecutionPause records when execution paused for user input
func (w *WorkflowManager) RecordExecutionPause(pause artifact.ExecutionPause) error {
	if w.executionTracker == nil {
		return fmt.Errorf("execution not started")
	}

	return w.executionTracker.AddPause(pause)
}

// AddReviewChecklistItem adds a high-risk area for review
func (w *WorkflowManager) AddReviewChecklistItem(item string) error {
	if w.executionTracker == nil {
		return fmt.Errorf("execution not started")
	}

	return w.executionTracker.AddReviewChecklistItem(item)
}

// CompleteExecution finalizes the execution phase
func (w *WorkflowManager) CompleteExecution() error {
	if w.executionTracker == nil {
		return fmt.Errorf("execution not started")
	}

	return w.executionTracker.Complete()
}

// StartReview initiates the review phase
func (w *WorkflowManager) StartReview(planningPath, executionPath string) error {
	if w == nil || w.reviewPhase == nil {
		return fmt.Errorf("review phase controller not initialized")
	}
	return w.reviewPhase.start(planningPath, executionPath)
}

// SetValidationStrategy sets the validation strategy for review
func (w *WorkflowManager) SetValidationStrategy(strategy artifact.ValidationStrategy) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.reviewArtifact != nil {
		w.reviewArtifact.ValidationStrategy = strategy
	}
}

// AddValidationResult adds a validation result to the review
func (w *WorkflowManager) AddValidationResult(result artifact.ValidationResult) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.reviewArtifact != nil {
		w.reviewArtifact.ValidationResults = append(w.reviewArtifact.ValidationResults, result)
	}
}

// AddIssue adds an issue found during review
func (w *WorkflowManager) AddIssue(issue artifact.Issue) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.reviewArtifact != nil {
		w.reviewArtifact.IssuesFound = append(w.reviewArtifact.IssuesFound, issue)
	}
}

// AddReviewIteration adds a review iteration record
func (w *WorkflowManager) AddReviewIteration(iteration artifact.ReviewIteration) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.reviewArtifact != nil {
		w.reviewArtifact.Iterations = append(w.reviewArtifact.Iterations, iteration)
	}
}

// AddOpportunisticImprovement adds an opportunistic improvement suggestion
func (w *WorkflowManager) AddOpportunisticImprovement(improvement artifact.Improvement) {
	w.stateMu.Lock()
	defer w.stateMu.Unlock()
	if w.reviewArtifact != nil {
		w.reviewArtifact.OpportunisticImprovements = append(w.reviewArtifact.OpportunisticImprovements, improvement)
	}
}

// ApproveReview marks the review as approved
func (w *WorkflowManager) ApproveReview(approval artifact.Approval) error {
	w.stateMu.Lock()
	if w.reviewArtifact == nil {
		w.stateMu.Unlock()
		return fmt.Errorf("no review in progress")
	}

	w.reviewArtifact.Approval = &approval
	w.reviewArtifact.Status = "approved"
	w.reviewArtifact.UpdatedAt = time.Now()

	// Copy artifact for I/O operation
	ra := w.reviewArtifact
	w.stateMu.Unlock()

	// Generate review artifact (I/O operation - do not hold lock)
	if w.artifacts == nil {
		return fmt.Errorf("artifact pipeline unavailable")
	}
	_, err := w.artifacts.reviewGenerator().Generate(ra)
	if err != nil {
		return fmt.Errorf("failed to generate review artifact: %w", err)
	}

	return nil
}

// GetReviewArtifact exposes the current review artifact for read-only consumers.
func (w *WorkflowManager) GetReviewArtifact() *artifact.ReviewArtifact {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()
	return w.reviewArtifact
}

// GetCurrentPhase returns the current workflow phase
func (w *WorkflowManager) GetCurrentPhase() WorkflowPhase {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()
	return w.currentPhase
}

// GetSystemPrompt generates the appropriate system prompt for the current phase
func (w *WorkflowManager) GetSystemPrompt() string {
	systemTime := time.Now()

	// Snapshot state under read lock to avoid holding it during prompt generation
	w.stateMu.RLock()
	steering := w.steeringNotes
	autonomy := w.autonomyLevel
	phase := w.currentPhase
	var planPath string
	if w.planningArtifact != nil {
		planPath = w.planningArtifact.FilePath
	}
	// Copy skillMessages slice to avoid holding lock during iteration
	skillMsgsCopy := make([]string, len(w.skillMessages))
	copy(skillMsgsCopy, w.skillMessages)
	w.stateMu.RUnlock()

	var prompt string
	switch phase {
	case WorkflowPhasePlanning:
		prompt = w.promptGenerator.Generate(prompts.PromptConfig{
			Phase:         prompts.PhasePlanning,
			SystemTime:    systemTime,
			SteeringNotes: steering,
			AutonomyLevel: autonomy,
		})
	case WorkflowPhaseExecution:
		prompt = w.promptGenerator.Generate(prompts.PromptConfig{
			Phase:            prompts.PhaseExecution,
			SystemTime:       systemTime,
			PlanningArtifact: planPath,
			SteeringNotes:    steering,
			AutonomyLevel:    autonomy,
		})
	case WorkflowPhaseReview:
		execPath := ""
		if w.executionTracker != nil {
			execPath = w.executionTracker.GetFilePath()
		}
		prompt = w.promptGenerator.Generate(prompts.PromptConfig{
			Phase:             prompts.PhaseReview,
			SystemTime:        systemTime,
			PlanningArtifact:  planPath,
			ExecutionArtifact: execPath,
			SteeringNotes:     steering,
			AutonomyLevel:     autonomy,
		})
	default:
		prompt = w.promptGenerator.Generate(prompts.PromptConfig{
			Phase:         prompts.PhasePlanning,
			SystemTime:    systemTime,
			SteeringNotes: steering,
			AutonomyLevel: autonomy,
		})
	}

	if w.skillManager != nil {
		if desc := strings.TrimSpace(w.skillManager.GetSkillsDescription()); desc != "" {
			prompt += "\n\n" + desc
		}
	}
	for _, msg := range skillMsgsCopy {
		if strings.TrimSpace(msg) == "" {
			continue
		}
		prompt += "\n\n" + msg
	}

	return prompt
}
