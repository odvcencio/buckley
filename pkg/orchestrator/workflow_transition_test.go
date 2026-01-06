package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/odvcencio/buckley/pkg/artifact"
	"github.com/odvcencio/buckley/pkg/config"
	orchestratorMocks "github.com/odvcencio/buckley/pkg/orchestrator/mocks"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
)

func newTestWorkflowManager(t *testing.T, modelClient ModelClient) (*WorkflowManager, *storage.Store, func()) {
	t.Helper()
	tempDir := t.TempDir()

	cfg := config.DefaultConfig()
	cfg.Artifacts.PlanningDir = filepath.Join(tempDir, "docs", "plans")
	cfg.Artifacts.ExecutionDir = filepath.Join(tempDir, "docs", "execution")
	cfg.Artifacts.ReviewDir = filepath.Join(tempDir, "docs", "reviews")
	if err := os.MkdirAll(cfg.Artifacts.PlanningDir, 0o755); err != nil {
		t.Fatalf("mkdir planning dir: %v", err)
	}
	if err := os.MkdirAll(cfg.Artifacts.ExecutionDir, 0o755); err != nil {
		t.Fatalf("mkdir execution dir: %v", err)
	}
	if err := os.MkdirAll(cfg.Artifacts.ReviewDir, 0o755); err != nil {
		t.Fatalf("mkdir review dir: %v", err)
	}

	storePath := filepath.Join(tempDir, "buckley.db")
	store, err := storage.New(storePath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}

	registry := tool.NewRegistry()
	docsRoot := filepath.Join(tempDir, "docs")
	projectRoot := tempDir

	manager := NewWorkflowManager(cfg, modelClient, registry, store, docsRoot, projectRoot, nil)

	cleanup := func() {
		_ = store.Close()
	}
	return manager, store, cleanup
}

func TestWorkflowManagerSetCurrentPlanLinksSession(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockModel := orchestratorMocks.NewMockModelClient(ctrl)

	w, store, cleanup := newTestWorkflowManager(t, mockModel)
	defer cleanup()

	session := &storage.Session{
		ID:          "session-123",
		Status:      storage.SessionStatusActive,
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
		ProjectPath: ".",
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	w.SetSessionID(session.ID)
	plan := &Plan{ID: "plan-abc", FeatureName: "Test Feature"}

	w.SetCurrentPlan(plan)

	if w.planID != plan.ID {
		t.Fatalf("expected planID %s, got %s", plan.ID, w.planID)
	}
	if w.planRef != plan {
		t.Fatalf("expected planRef to point to plan")
	}

	linkedPlan, err := store.GetSessionPlanID(session.ID)
	if err != nil {
		t.Fatalf("GetSessionPlanID: %v", err)
	}
	if linkedPlan != plan.ID {
		t.Fatalf("expected feature session to link plan %s, got %s", plan.ID, linkedPlan)
	}
}

func TestWorkflowManagerSystemPromptFollowsPhase(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockModel := orchestratorMocks.NewMockModelClient(ctrl)

	w, _, cleanup := newTestWorkflowManager(t, mockModel)
	defer cleanup()
	w.SetSteeringNotes("stay aligned to product vision")
	w.SetAutonomyLevel("balanced")

	w.currentPhase = WorkflowPhasePlanning
	if prompt := w.GetSystemPrompt(); prompt == "" {
		t.Fatalf("planning prompt should not be empty")
	}
	if !strings.Contains(w.GetSystemPrompt(), "STEERING NOTES") && !strings.Contains(w.GetSystemPrompt(), "Steering Notes") {
		t.Fatalf("planning prompt should include steering notes section")
	}
	if !strings.Contains(strings.ToLower(w.GetSystemPrompt()), "balanced") {
		t.Fatalf("planning prompt should include autonomy level")
	}

	w.currentPhase = WorkflowPhaseExecution
	w.planningArtifact = &artifact.PlanningArtifact{
		Artifact: artifact.Artifact{
			FilePath: "/tmp/docs/plans/plan.md",
		},
	}
	execPrompt := w.GetSystemPrompt()
	if !strings.Contains(execPrompt, "/tmp/docs/plans/plan.md") {
		t.Fatalf("execution prompt should mention planning artifact path, got: %s", execPrompt)
	}

	tracker := artifact.NewExecutionTracker(w.config.Artifacts.ExecutionDir, w.planningArtifact.FilePath, "feature", 1)
	if err := tracker.Initialize(); err != nil {
		t.Fatalf("Initialize execution tracker: %v", err)
	}
	w.executionTracker = tracker
	w.currentPhase = WorkflowPhaseReview

	reviewPrompt := w.GetSystemPrompt()
	if !strings.Contains(reviewPrompt, tracker.GetFilePath()) {
		t.Fatalf("review prompt should include execution artifact path, got: %s", reviewPrompt)
	}
}

func TestWorkflowManagerSteeringPersistence(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockModel := orchestratorMocks.NewMockModelClient(ctrl)

	w, store, cleanup := newTestWorkflowManager(t, mockModel)
	defer cleanup()

	session := &storage.Session{
		ID:          "session-steer",
		Status:      storage.SessionStatusActive,
		CreatedAt:   time.Now(),
		LastActive:  time.Now(),
		ProjectPath: ".",
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	w.SetSessionID(session.ID)
	w.SetSteeringNotes("persist me")
	w.SetAutonomyLevel("autonomous")

	// Rebuild workflow manager against the same store to ensure settings are loaded
	cfg := w.config
	registry := tool.NewRegistry()
	docsRoot := filepath.Dir(cfg.Artifacts.PlanningDir)
	projectRoot := w.projectRoot
	w2 := NewWorkflowManager(cfg, mockModel, registry, store, docsRoot, projectRoot, nil)
	w2.SetSessionID(session.ID)

	if w2.steeringNotes != "persist me" {
		t.Fatalf("expected steering notes to be persisted, got %q", w2.steeringNotes)
	}
	if w2.autonomyLevel != "autonomous" {
		t.Fatalf("expected autonomy level to be persisted, got %q", w2.autonomyLevel)
	}
}
