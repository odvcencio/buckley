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
	"github.com/odvcencio/buckley/pkg/paths"
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
	currentPhase      WorkflowPhase
	feature           string
	planningArtifact  *artifact.PlanningArtifact
	executionArtifact *artifact.ExecutionArtifact
	reviewArtifact    *artifact.ReviewArtifact

	// Activity tracking
	activityTracker *tool.ActivityTracker
	intentHistory   *tool.IntentHistory

	researchAgent *ResearchAgent

	latestResearchBrief *artifact.ResearchBrief

	currentAgent  string
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
		w.skillMessages = append(w.skillMessages, msg)
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
	w.sessionID = strings.TrimSpace(sessionID)
	w.loadSteeringSettings()
	w.RestorePauseStateFromSession()
}

// SetCurrentPlan stores the active plan reference and ID for telemetry snapshots.
func (w *WorkflowManager) SetCurrentPlan(plan *Plan) {
	if w == nil {
		return
	}
	w.planRef = plan
	if plan != nil {
		w.planID = plan.ID
		if w.store != nil && w.sessionID != "" {
			if err := w.store.LinkSessionToPlan(w.sessionID, plan.ID); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to link session %s to plan %s: %v\n", w.sessionID, plan.ID, err)
			}
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
	if w.planningArtifact != nil {
		w.planningArtifact.Context.ExistingPatterns = patterns
		w.planningArtifact.Context.ArchitectureStyle = archStyle
		w.planningArtifact.Context.RelevantFiles = files
	}
}

// AddArchitectureDecision adds an ADR to the planning artifact
func (w *WorkflowManager) AddArchitectureDecision(decision artifact.ArchitectureDecision) {
	if w.planningArtifact != nil {
		w.planningArtifact.Decisions = append(w.planningArtifact.Decisions, decision)
	}
}

// AddCodeContract adds a code contract to the planning artifact
func (w *WorkflowManager) AddCodeContract(contract artifact.CodeContract) {
	if w.planningArtifact != nil {
		w.planningArtifact.CodeContracts = append(w.planningArtifact.CodeContracts, contract)
	}
}

// SetLayerMap sets the layer mapping for the planning artifact
func (w *WorkflowManager) SetLayerMap(layerMap artifact.LayerMap) {
	if w.planningArtifact != nil {
		w.planningArtifact.LayerMap = layerMap
	}
}

// AddTask adds a task to the planning artifact
func (w *WorkflowManager) AddTask(task artifact.TaskBreakdown) {
	if w.planningArtifact != nil {
		w.planningArtifact.Tasks = append(w.planningArtifact.Tasks, task)
	}
}

// SetCrossCuttingConcerns sets cross-cutting concerns for the planning artifact
func (w *WorkflowManager) SetCrossCuttingConcerns(concerns artifact.CrossCuttingConcerns) {
	if w.planningArtifact != nil {
		w.planningArtifact.CrossCuttingScope = concerns
	}
}

// FinalizePlanning generates and saves the planning artifact
func (w *WorkflowManager) FinalizePlanning() (string, error) {
	if w.planningArtifact == nil {
		return "", fmt.Errorf("no planning artifact to finalize")
	}

	w.planningArtifact.Status = "completed"
	w.planningArtifact.UpdatedAt = time.Now()

	// Generate artifact file
	if w.artifacts == nil {
		return "", fmt.Errorf("artifact pipeline unavailable")
	}
	filePath, err := w.artifacts.planningGenerator().Generate(w.planningArtifact)
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
	if w.reviewArtifact != nil {
		w.reviewArtifact.ValidationStrategy = strategy
	}
}

// AddValidationResult adds a validation result to the review
func (w *WorkflowManager) AddValidationResult(result artifact.ValidationResult) {
	if w.reviewArtifact != nil {
		w.reviewArtifact.ValidationResults = append(w.reviewArtifact.ValidationResults, result)
	}
}

// AddIssue adds an issue found during review
func (w *WorkflowManager) AddIssue(issue artifact.Issue) {
	if w.reviewArtifact != nil {
		w.reviewArtifact.IssuesFound = append(w.reviewArtifact.IssuesFound, issue)
	}
}

// AddReviewIteration adds a review iteration record
func (w *WorkflowManager) AddReviewIteration(iteration artifact.ReviewIteration) {
	if w.reviewArtifact != nil {
		w.reviewArtifact.Iterations = append(w.reviewArtifact.Iterations, iteration)
	}
}

// AddOpportunisticImprovement adds an opportunistic improvement suggestion
func (w *WorkflowManager) AddOpportunisticImprovement(improvement artifact.Improvement) {
	if w.reviewArtifact != nil {
		w.reviewArtifact.OpportunisticImprovements = append(w.reviewArtifact.OpportunisticImprovements, improvement)
	}
}

// ApproveReview marks the review as approved
func (w *WorkflowManager) ApproveReview(approval artifact.Approval) error {
	if w.reviewArtifact == nil {
		return fmt.Errorf("no review in progress")
	}

	w.reviewArtifact.Approval = &approval
	w.reviewArtifact.Status = "approved"
	w.reviewArtifact.UpdatedAt = time.Now()

	// Generate review artifact
	if w.artifacts == nil {
		return fmt.Errorf("artifact pipeline unavailable")
	}
	_, err := w.artifacts.reviewGenerator().Generate(w.reviewArtifact)
	if err != nil {
		return fmt.Errorf("failed to generate review artifact: %w", err)
	}

	return nil
}

// GetReviewArtifact exposes the current review artifact for read-only consumers.
func (w *WorkflowManager) GetReviewArtifact() *artifact.ReviewArtifact {
	return w.reviewArtifact
}

// GetCurrentPhase returns the current workflow phase
func (w *WorkflowManager) GetCurrentPhase() WorkflowPhase {
	return w.currentPhase
}

// GetSystemPrompt generates the appropriate system prompt for the current phase
func (w *WorkflowManager) GetSystemPrompt() string {
	systemTime := time.Now()
	steering := w.steeringNotes
	autonomy := w.autonomyLevel

	var prompt string
	switch w.currentPhase {
	case WorkflowPhasePlanning:
		prompt = w.promptGenerator.Generate(prompts.PromptConfig{
			Phase:         prompts.PhasePlanning,
			SystemTime:    systemTime,
			SteeringNotes: steering,
			AutonomyLevel: autonomy,
		})
	case WorkflowPhaseExecution:
		planPath := ""
		if w.planningArtifact != nil {
			planPath = w.planningArtifact.FilePath
		}
		prompt = w.promptGenerator.Generate(prompts.PromptConfig{
			Phase:            prompts.PhaseExecution,
			SystemTime:       systemTime,
			PlanningArtifact: planPath,
			SteeringNotes:    steering,
			AutonomyLevel:    autonomy,
		})
	case WorkflowPhaseReview:
		planPath := ""
		execPath := ""
		if w.planningArtifact != nil {
			planPath = w.planningArtifact.FilePath
		}
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
	for _, msg := range w.skillMessages {
		if strings.TrimSpace(msg) == "" {
			continue
		}
		prompt += "\n\n" + msg
	}

	return prompt
}

// PersonaProvider exposes persona definitions for downstream agents.
func (w *WorkflowManager) PersonaProvider() *personality.PersonaProvider {
	if w == nil {
		return nil
	}
	return w.personaProvider
}

// ProjectRoot returns the repository root Buckley is operating in.
func (w *WorkflowManager) ProjectRoot() string {
	if w == nil {
		return ""
	}
	return w.projectRoot
}

// ReloadPersonas rebuilds the persona provider from disk/config and reapplies overrides.
func (w *WorkflowManager) ReloadPersonas() (*personality.PersonaProvider, error) {
	if w == nil {
		return nil, fmt.Errorf("workflow manager unavailable")
	}
	provider := BuildPersonaProvider(w.config, w.projectRoot)
	if provider == nil {
		return nil, fmt.Errorf("failed to rebuild persona provider")
	}
	w.personaProvider = provider
	w.promptGenerator = prompts.NewGenerator(prompts.WithPersonaProvider(provider))
	w.loadPersonaOverrides()
	return provider, nil
}

// PersonaSection renders persona guidance for the provided stage.
func (w *WorkflowManager) PersonaSection(stage string) string {
	if w == nil || w.personaProvider == nil {
		return ""
	}
	return w.personaProvider.SectionForPhase(stage)
}

// PersonaOverrides returns the current runtime overrides.
func (w *WorkflowManager) PersonaOverrides() map[string]string {
	if w == nil || w.personaProvider == nil {
		return map[string]string{}
	}
	return w.personaProvider.RuntimeOverrides()
}

// SetSteeringNotes updates runtime steering guidance for multi-turn conversations.
func (w *WorkflowManager) SetSteeringNotes(notes string) {
	if w == nil {
		return
	}
	w.steeringNotes = strings.TrimSpace(notes)
	w.persistSteeringSettings()
}

// SetAutonomyLevel updates the autonomy/trust preference used in prompts.
func (w *WorkflowManager) SetAutonomyLevel(level string) {
	if w == nil {
		return
	}
	w.autonomyLevel = strings.TrimSpace(level)
	w.persistSteeringSettings()
}

// TaskPhases returns the configured task-level phases.
func (w *WorkflowManager) TaskPhases() []TaskPhase {
	if w == nil {
		return nil
	}
	out := make([]TaskPhase, 0, len(w.taskPhases))
	out = append(out, w.taskPhases...)
	return out
}

// SetPersonaOverride persists and applies a persona override for the given phase/stage.
func (w *WorkflowManager) SetPersonaOverride(stage, personaID string) error {
	if w == nil || w.personaProvider == nil {
		return fmt.Errorf("persona provider unavailable")
	}
	normalized := NormalizePersonaStage(stage)
	if normalized == "" {
		return fmt.Errorf("unknown phase: %s", stage)
	}
	personaID = strings.TrimSpace(personaID)
	if personaID != "" && w.personaProvider.Profile(personaID) == nil {
		return fmt.Errorf("persona %s not found", personaID)
	}
	if err := w.personaProvider.SetRuntimeOverride(normalized, personaID); err != nil {
		return err
	}
	if w.store != nil {
		key := PersonaSettingKey(normalized)
		if err := w.store.SetSetting(key, personaID); err != nil {
			return err
		}
	}
	return nil
}

func (w *WorkflowManager) loadPersonaOverrides() {
	if w == nil || w.personaProvider == nil || w.store == nil {
		return
	}
	keys := make([]string, len(PersonaStages))
	for i, stage := range PersonaStages {
		keys[i] = PersonaSettingKey(stage)
	}
	existing, err := w.store.GetSettings(keys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load persona overrides: %v\n", err)
		return
	}
	for _, stage := range PersonaStages {
		key := PersonaSettingKey(stage)
		value := strings.TrimSpace(existing[key])
		_ = w.personaProvider.SetRuntimeOverride(stage, value)
	}
}

// RecordIntent records an intent statement
func (w *WorkflowManager) RecordIntent(intent tool.Intent) {
	w.intentHistory.Add(intent)
}

// GetLatestIntent returns the most recent intent
func (w *WorkflowManager) GetLatestIntent() *tool.Intent {
	return w.intentHistory.GetLatest()
}

// GetActivityGroups returns current activity groups
func (w *WorkflowManager) GetActivityGroups() []tool.ActivityGroup {
	return w.activityTracker.GetGroups()
}

// RecordToolCall records a tool call for activity tracking
// AuthorizeToolCall inspects tool usage for risky operations.
func (w *WorkflowManager) AuthorizeToolCall(t tool.Tool, params map[string]any) error {
	if w == nil || t == nil {
		return nil
	}

	if strings.EqualFold(t.Name(), "run_shell") {
		if cmd, ok := params["command"].(string); ok {
			// Skip authorization for safe commands (tests, builds, etc.)
			if isSafeCommand(cmd) {
				return nil
			}
			// Check for elevation requirements
			if requiresElevation(cmd) {
				return w.pauseWorkflow("Permission Escalation", fmt.Sprintf("Shell command requires elevated privileges: %s", summarizeCommand(cmd)))
			}
		}
	}
	return nil
}

// RecordToolCall records a tool call for activity tracking.
func (w *WorkflowManager) RecordToolCall(t tool.Tool, params map[string]any, result any, startTime, endTime time.Time) {
	if w == nil || w.activityTracker == nil || t == nil {
		return
	}

	var converted *builtin.Result
	switch v := result.(type) {
	case *builtin.Result:
		converted = v
	case builtin.Result:
		converted = &v
	}

	w.activityTracker.RecordCall(t, params, converted, startTime, endTime)
}

func (w *WorkflowManager) pauseWorkflow(reason, question string) error {
	if w == nil {
		return &WorkflowPauseError{Reason: reason, Question: question}
	}

	fmt.Fprintf(os.Stderr, "⚠️  %s: %s\n", reason, question)

	now := time.Now()
	w.pauseMu.Lock()
	w.paused = true
	w.pauseReason = reason
	w.pauseQuestion = question
	w.pauseAt = now
	w.pauseMu.Unlock()

	// Persist pause state to database for recovery across restarts
	if w.store != nil && w.sessionID != "" {
		if err := w.store.UpdateSessionPauseState(w.sessionID, reason, question, &now); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to persist pause state: %v\n", err)
		}
	}

	if w.executionTracker != nil {
		pause := artifact.ExecutionPause{
			Reason:    reason,
			Question:  question,
			Timestamp: now,
		}
		if err := w.executionTracker.AddPause(pause); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to record execution pause: %v\n", err)
		}
	}

	return &WorkflowPauseError{Reason: reason, Question: question}
}

func isSafeCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return true
	}

	// Safe command prefixes that don't require authorization
	safeCommands := []string{
		"go test",
		"go build",
		"go run",
		"go mod",
		"go get",
		"npm test",
		"npm run",
		"npm install",
		"cargo test",
		"cargo build",
		"make test",
		"pytest",
		"python -m pytest",
		"jest",
		"mocha",
		"ls",
		"cat",
		"grep",
		"find",
		"echo",
		"pwd",
		"which",
		"git ",
		"gh ",
	}

	for _, safe := range safeCommands {
		if strings.HasPrefix(cmd, safe) {
			return true
		}
	}

	return false
}

func requiresElevation(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}

	segments := splitShellSegments(cmd)
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		if strings.HasPrefix(segment, "sudo ") || segment == "sudo" {
			return true
		}
		if strings.HasPrefix(segment, "sudo\t") {
			return true
		}
	}
	return false
}

func splitShellSegments(cmd string) []string {
	separators := []string{"&&", "||", ";", "\n"}
	segments := []string{cmd}
	for _, sep := range separators {
		var next []string
		for _, segment := range segments {
			next = append(next, strings.Split(segment, sep)...)
		}
		segments = next
	}
	return segments
}

func summarizeCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	const maxLen = 80
	if len(cmd) <= maxLen {
		return cmd
	}
	return cmd[:maxLen] + "…"
}

// SetActiveAgent updates the current agent label.
func (w *WorkflowManager) SetActiveAgent(agent string) {
	if w == nil {
		return
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		agent = string(w.currentPhase)
	}
	w.currentAgent = agent
}

// GetActiveAgent returns the current agent label.
func (w *WorkflowManager) GetActiveAgent() string {
	if w == nil {
		return ""
	}
	if w.currentAgent == "" {
		return string(w.currentPhase)
	}
	return w.currentAgent
}

// ClearPause resets pause metadata in memory and database.
func (w *WorkflowManager) ClearPause() {
	if w == nil {
		return
	}
	w.pauseMu.Lock()
	w.paused = false
	w.pauseReason = ""
	w.pauseQuestion = ""
	w.pauseAt = time.Time{}
	w.pauseMu.Unlock()

	// Clear pause state from database
	if w.store != nil && w.sessionID != "" {
		if err := w.store.UpdateSessionPauseState(w.sessionID, "", "", nil); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clear pause state: %v\n", err)
		}
	}
}

// GetPauseInfo reports current pause state.
func (w *WorkflowManager) GetPauseInfo() (bool, string, string, time.Time) {
	if w == nil {
		return false, "", "", time.Time{}
	}
	w.pauseMu.RLock()
	defer w.pauseMu.RUnlock()
	if !w.paused {
		return false, "", "", time.Time{}
	}
	return true, w.pauseReason, w.pauseQuestion, w.pauseAt
}

// RestorePauseStateFromSession loads pause state from the database session.
// Call this after SetSessionID to restore pause state across restarts.
func (w *WorkflowManager) RestorePauseStateFromSession() {
	if w == nil || w.store == nil || w.sessionID == "" {
		return
	}

	session, err := w.store.GetSession(w.sessionID)
	if err != nil || session == nil {
		return
	}

	if session.Status == storage.SessionStatusPaused && (session.PauseReason != "" || session.PauseQuestion != "") {
		w.pauseMu.Lock()
		w.paused = true
		w.pauseReason = session.PauseReason
		w.pauseQuestion = session.PauseQuestion
		if session.PausedAt != nil {
			w.pauseAt = *session.PausedAt
		}
		w.pauseMu.Unlock()

		fmt.Fprintf(os.Stderr, "▶️  Workflow paused: %s\n", session.PauseReason)
		if session.PauseQuestion != "" {
			fmt.Fprintf(os.Stderr, "   %s\n", session.PauseQuestion)
		}
	}
}

// GetCurrentPlan returns the active plan reference (if any).
func (w *WorkflowManager) GetCurrentPlan() *Plan {
	if w == nil {
		return nil
	}
	return w.planRef
}

// GetActivitySummaries returns recent tool activity summaries.
func (w *WorkflowManager) GetActivitySummaries() []string {
	if w == nil || w.activityTracker == nil {
		return nil
	}
	var summaries []string
	for _, group := range w.activityTracker.GetGroups() {
		if group.Summary != "" {
			summaries = append(summaries, group.Summary)
		}
	}
	return summaries
}

// SendProgress sends a progress update to subscribers (non-blocking)
func (w *WorkflowManager) SendProgress(message string) {
	if w == nil || w.progressChan == nil {
		return
	}
	select {
	case w.progressChan <- message:
	default:
		// Channel full or no listener - skip
	}
}

func (w *WorkflowManager) emitTelemetry(event telemetry.Event) {
	if w == nil || w.telemetry == nil {
		return
	}
	if event.SessionID == "" {
		event.SessionID = w.sessionID
	}
	if event.PlanID == "" && w.planID != "" {
		event.PlanID = w.planID
	}
	w.telemetry.Publish(event)
}

// EmitPlanSnapshot publishes a plan-level telemetry event.
func (w *WorkflowManager) EmitPlanSnapshot(plan *Plan, eventType telemetry.EventType) {
	if plan == nil {
		return
	}
	if eventType == "" {
		eventType = telemetry.EventPlanUpdated
	}
	data := map[string]any{
		"plan":      plan,
		"feature":   plan.FeatureName,
		"taskCount": len(plan.Tasks),
	}
	w.emitTelemetry(telemetry.Event{
		Type:   eventType,
		PlanID: plan.ID,
		Data:   data,
	})
}

// EmitTaskEvent publishes a task-level telemetry event.
func (w *WorkflowManager) EmitTaskEvent(task *Task, eventType telemetry.EventType) {
	if task == nil {
		return
	}
	data := map[string]any{
		"task":   task,
		"title":  task.Title,
		"status": task.Status,
	}
	w.emitTelemetry(telemetry.Event{
		Type:   eventType,
		PlanID: w.planID,
		TaskID: task.ID,
		Data:   data,
	})
}

// PublishTelemetry allows external components to emit arbitrary telemetry events.
func (w *WorkflowManager) PublishTelemetry(event telemetry.Event) {
	if w == nil {
		return
	}
	w.emitTelemetry(event)
}

// EmitBuilderEvent emits a builder-related telemetry event.
func (w *WorkflowManager) EmitBuilderEvent(task *Task, eventType telemetry.EventType, details map[string]any) {
	if w == nil {
		return
	}
	data := make(map[string]any)
	for k, v := range details {
		data[k] = v
	}
	if task != nil {
		data["taskId"] = task.ID
		data["title"] = task.Title
	}
	w.emitTelemetry(telemetry.Event{
		Type:   eventType,
		PlanID: w.planID,
		TaskID: func() string {
			if task != nil {
				return task.ID
			}
			return ""
		}(),
		Data: data,
	})
}

// EmitResearchEvent emits a research-related telemetry event.
func (w *WorkflowManager) EmitResearchEvent(feature string, eventType telemetry.EventType, details map[string]any) {
	if w == nil {
		return
	}
	data := make(map[string]any)
	for k, v := range details {
		data[k] = v
	}
	if feature != "" {
		data["feature"] = feature
	}
	w.emitTelemetry(telemetry.Event{
		Type:   eventType,
		PlanID: w.planID,
		Data:   data,
	})
}

// GetProgressChan returns the progress channel for subscription
func (w *WorkflowManager) GetProgressChan() <-chan string {
	if w == nil {
		return nil
	}
	return w.progressChan
}

// EnableProgressStreaming creates the progress channel
func (w *WorkflowManager) EnableProgressStreaming() {
	if w != nil {
		w.progressChan = make(chan string, 100) // Buffered to prevent blocking
	}
}

// DisableProgressStreaming closes the progress channel
func (w *WorkflowManager) DisableProgressStreaming() {
	if w != nil && w.progressChan != nil {
		close(w.progressChan)
		w.progressChan = nil
	}
}

// Resume clears the pause state, typically after user intervention.
func (w *WorkflowManager) Resume(resolution string) {
	if w == nil {
		return
	}
	resolution = strings.TrimSpace(resolution)
	if resolution == "" {
		resolution = "Manual resume via CLI"
	}
	if w.executionTracker != nil {
		_ = w.executionTracker.ResolvePause("Manual resume", resolution)
	}
	fmt.Fprintf(os.Stderr, "▶️  Workflow resumed: %s\n", resolution)
	w.ClearPause()
}

// GetArtifactChain returns the artifact chain for the current feature
func (w *WorkflowManager) GetArtifactChain() (*artifact.Chain, error) {
	if w.artifacts == nil {
		return nil, fmt.Errorf("artifact pipeline unavailable")
	}
	return w.artifacts.chainManager().FindChain(w.feature)
}

// UpdateArtifactLinks updates links in the artifact chain
func (w *WorkflowManager) UpdateArtifactLinks(chain *artifact.Chain) error {
	if w.artifacts == nil {
		return fmt.Errorf("artifact pipeline unavailable")
	}
	return w.artifacts.chainManager().UpdateLinks(chain)
}

// Pause exposes manual workflow pauses (CLI/API).
func (w *WorkflowManager) Pause(reason, question string) error {
	return w.pauseWorkflow(reason, question)
}

// GetResearchHighlights returns summary + top risks from the latest brief.
func (w *WorkflowManager) GetResearchHighlights() (string, []string) {
	if w == nil || w.latestResearchBrief == nil {
		return "", nil
	}
	summary := strings.TrimSpace(w.latestResearchBrief.Summary)
	if summary == "" {
		summary = fmt.Sprintf("Research completed for %s", w.latestResearchBrief.Feature)
	}
	risks := w.latestResearchBrief.Risks
	if len(risks) > 3 {
		risks = risks[:3]
	}
	return summary, risks
}

// GetResearchBrief returns the last generated brief, if any.
func (w *WorkflowManager) GetResearchBrief() *artifact.ResearchBrief {
	return w.latestResearchBrief
}

// EnrichPlan copies workflow context (research highlights, logs) onto plan prior to saving.
func (w *WorkflowManager) EnrichPlan(plan *Plan) {
	if w == nil || plan == nil {
		return
	}

	if summary, risks := w.GetResearchHighlights(); summary != "" {
		plan.Context.ResearchSummary = summary
		plan.Context.ResearchRisks = append([]string{}, risks...)
		if brief := w.latestResearchBrief; brief != nil && !brief.Updated.IsZero() {
			plan.Context.ResearchLoggedAt = brief.Updated
		} else if plan.Context.ResearchLoggedAt.IsZero() {
			plan.Context.ResearchLoggedAt = time.Now()
		}
	}
}

func (w *WorkflowManager) steeringSettingKeys() (string, string) {
	if w.sessionID == "" {
		return "", ""
	}
	return fmt.Sprintf("session.%s.steering_notes", w.sessionID),
		fmt.Sprintf("session.%s.autonomy_level", w.sessionID)
}

func (w *WorkflowManager) loadSteeringSettings() {
	if w == nil || w.store == nil {
		return
	}
	steerKey, autoKey := w.steeringSettingKeys()
	if steerKey == "" {
		return
	}
	settings, err := w.store.GetSettings([]string{steerKey, autoKey})
	if err != nil {
		return
	}
	if val, ok := settings[steerKey]; ok {
		w.steeringNotes = strings.TrimSpace(val)
	}
	if val, ok := settings[autoKey]; ok {
		w.autonomyLevel = strings.TrimSpace(val)
	}
}

func (w *WorkflowManager) persistSteeringSettings() {
	if w == nil || w.store == nil {
		return
	}
	steerKey, autoKey := w.steeringSettingKeys()
	if steerKey == "" {
		return
	}
	_ = w.store.SetSetting(steerKey, w.steeringNotes)
	_ = w.store.SetSetting(autoKey, w.autonomyLevel)
}

func researchLogPath(feature string) string {
	identifier := SanitizeIdentifier(feature)
	if identifier == "" {
		identifier = "default"
	}
	return filepath.Join(paths.BuckleyLogsDir(identifier), "research.jsonl")
}
