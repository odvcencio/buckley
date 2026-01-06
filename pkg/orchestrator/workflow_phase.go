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
	w.currentPhase = WorkflowPhasePlanning
	w.feature = featureName
	w.ClearPause()
	w.SetActiveAgent("Planning")

	if w.skillManager != nil {
		if err := w.skillManager.ActivatePhaseSkills("planning"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to activate planning skills: %v\n", err)
		}
	}

	w.planningArtifact = &artifact.PlanningArtifact{
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
			w.planningArtifact.Context.RelevantFiles = files
			w.latestResearchBrief = brief
			summary := strings.TrimSpace(brief.Summary)
			if summary == "" {
				summary = fmt.Sprintf("Research completed for %s", featureName)
			}
			w.planningArtifact.Context.ResearchSummary = summary
			risks := brief.Risks
			if len(risks) > 3 {
				risks = risks[:3]
			}
			w.planningArtifact.Context.ResearchRisks = append([]string{}, risks...)
			w.planningArtifact.Context.ResearchLogPath = researchLogPath(featureName)
			w.planningArtifact.Context.ResearchLoggedAt = time.Now()
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
	if w.planningArtifact == nil {
		return fmt.Errorf("no planning artifact available")
	}

	w.currentPhase = WorkflowPhaseExecution
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

	totalTasks := len(w.planningArtifact.Tasks)
	w.executionTracker = artifact.NewExecutionTracker(
		w.config.Artifacts.ExecutionDir,
		planningArtifactPath,
		w.feature,
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
	w.currentPhase = WorkflowPhaseReview
	w.SetActiveAgent("Review")

	if w.skillManager != nil {
		if err := w.skillManager.DeactivatePhaseSkills("execute"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to deactivate execution skills: %v\n", err)
		}
		if err := w.skillManager.ActivatePhaseSkills("review"); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to activate review skills: %v\n", err)
		}
	}

	w.reviewArtifact = &artifact.ReviewArtifact{
		Artifact: artifact.Artifact{
			Type:      artifact.ArtifactTypeReview,
			Feature:   w.feature,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
			Status:    "in_progress",
		},
		PlanningArtifactPath:  planningPath,
		ExecutionArtifactPath: executionPath,
		ReviewedAt:            time.Now(),
		ReviewerModel:         w.config.Models.Review,
	}
	return nil
}
