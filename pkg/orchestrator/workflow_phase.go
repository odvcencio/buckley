package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/artifact"
)

type planningController struct {
	manager *WorkflowManager
}

func newPlanningController(manager *WorkflowManager) *planningController {
	return &planningController{manager: manager}
}

func (c *planningController) start(ctx context.Context, featureName, userGoal string) error {
	w := c.manager
	w.stateMu.Lock()
	w.currentPhase = WorkflowPhasePlanning
	w.feature = featureName
	w.stateMu.Unlock()
	w.ClearPause()
	w.SetActiveAgent("Planning")

	if w.skillManager != nil {
		if err := w.skillManager.ActivatePhaseSkills("planning"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to activate planning skills: %v\n", err)
		}
	}

	pa := &artifact.PlanningArtifact{
		Artifact: artifact.Artifact{
			Type:      artifact.ArtifactTypePlanning,
			Feature:   featureName,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Status:    "in_progress",
		},
		Context: artifact.ContextSection{
			UserGoal: userGoal,
		},
	}
	w.stateMu.Lock()
	w.planningArtifact = pa
	w.stateMu.Unlock()

	if w.researchAgent != nil {
		researchCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		previousAgent := w.GetActiveAgent()
		w.SetActiveAgent("Research")
		defer w.SetActiveAgent(previousAgent)
		brief, err := w.researchAgent.Run(researchCtx, featureName, userGoal)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: research phase failed: %v\n", err)
		}
		if brief != nil {
			files := make([]string, 0, len(brief.RelevantFiles))
			for _, rf := range brief.RelevantFiles {
				files = append(files, rf.Path)
			}
			summary := strings.TrimSpace(brief.Summary)
			if summary == "" {
				summary = fmt.Sprintf("Research completed for %s", featureName)
			}
			risks := brief.Risks
			if len(risks) > 3 {
				risks = risks[:3]
			}
			w.stateMu.Lock()
			w.planningArtifact.Context.RelevantFiles = files
			w.latestResearchBrief = brief
			w.planningArtifact.Context.ResearchSummary = summary
			w.planningArtifact.Context.ResearchRisks = append([]string{}, risks...)
			w.planningArtifact.Context.ResearchLogPath = researchLogPath(featureName)
			w.planningArtifact.Context.ResearchLoggedAt = time.Now()
			w.stateMu.Unlock()
		}
	}
	return nil
}

type executionController struct {
	manager *WorkflowManager
}

func newExecutionController(manager *WorkflowManager) *executionController {
	return &executionController{manager: manager}
}

func (c *executionController) start(planningArtifactPath string) error {
	w := c.manager
	w.stateMu.RLock()
	pa := w.planningArtifact
	w.stateMu.RUnlock()
	if pa == nil {
		return fmt.Errorf("no planning artifact available")
	}

	w.stateMu.Lock()
	w.currentPhase = WorkflowPhaseExecution
	w.stateMu.Unlock()
	w.ClearPause()
	w.SetActiveAgent("Execution")

	if w.skillManager != nil {
		if err := w.skillManager.DeactivatePhaseSkills("planning"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to deactivate planning skills: %v\n", err)
		}
		if err := w.skillManager.ActivatePhaseSkills("execute"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to activate execution skills: %v\n", err)
		}
	}

	w.stateMu.RLock()
	totalTasks := len(w.planningArtifact.Tasks)
	feat := w.feature
	w.stateMu.RUnlock()
	w.executionTracker = artifact.NewExecutionTracker(
		w.config.Artifacts.ExecutionDir,
		planningArtifactPath,
		feat,
		totalTasks,
	)

	if err := w.executionTracker.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize execution artifact: %w", err)
	}

	return nil
}

type reviewController struct {
	manager *WorkflowManager
}

func newReviewController(manager *WorkflowManager) *reviewController {
	return &reviewController{manager: manager}
}

func (c *reviewController) start(planningPath, executionPath string) error {
	w := c.manager
	w.stateMu.Lock()
	w.currentPhase = WorkflowPhaseReview
	feat := w.feature
	w.stateMu.Unlock()
	w.SetActiveAgent("Review")

	if w.skillManager != nil {
		if err := w.skillManager.DeactivatePhaseSkills("execute"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to deactivate execution skills: %v\n", err)
		}
		if err := w.skillManager.ActivatePhaseSkills("review"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to activate review skills: %v\n", err)
		}
	}

	w.stateMu.Lock()
	w.reviewArtifact = &artifact.ReviewArtifact{
		Artifact: artifact.Artifact{
			Type:      artifact.ArtifactTypeReview,
			Feature:   feat,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Status:    "in_progress",
		},
		PlanningArtifactPath:  planningPath,
		ExecutionArtifactPath: executionPath,
		ReviewedAt:            time.Now(),
		ReviewerModel:         w.config.Models.Review,
	}
	w.stateMu.Unlock()
	return nil
}
