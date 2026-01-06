package orchestrator

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"github.com/odvcencio/buckley/pkg/artifact"
	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
)

func TestAuthorizeToolCallDetectsSudo(t *testing.T) {
	cfg := config.DefaultConfig()
	w := &WorkflowManager{
		config:          cfg,
		activityTracker: tool.NewActivityTracker(tool.DefaultActivityGroupingConfig()),
	}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	shell := NewMockTool(ctrl)
	shell.EXPECT().Name().Return("run_shell").AnyTimes()

	err := w.AuthorizeToolCall(shell, map[string]any{"command": "sudo apt update"})
	if err == nil {
		t.Fatalf("expected pause error")
	}
	if !errors.Is(err, ErrWorkflowPaused) {
		t.Fatalf("expected ErrWorkflowPaused, got %v", err)
	}
}

func TestWorkflowPauseError_Error(t *testing.T) {
	err := &WorkflowPauseError{
		Reason:   "testing",
		Question: "test question",
	}
	expected := "testing: test question"
	if err.Error() != expected {
		t.Errorf("Error() = %q, want %q", err.Error(), expected)
	}

	// Test Unwrap
	if !errors.Is(err, ErrWorkflowPaused) {
		t.Error("Unwrap() should return ErrWorkflowPaused")
	}
}

func TestWorkflowManager_SetSkillManager(t *testing.T) {
	w := &WorkflowManager{}
	sm := &SkillManager{}
	w.SetSkillManager(sm)
	if w.skillManager != sm {
		t.Error("SetSkillManager did not set skill manager")
	}
}

func TestWorkflowManager_InitializeDocumentation(t *testing.T) {
	w := &WorkflowManager{}
	err := w.InitializeDocumentation()
	if err == nil || err.Error() != "artifact pipeline unavailable" {
		t.Errorf("Expected artifact pipeline unavailable error, got: %v", err)
	}
}

func TestWorkflowManager_StartPlanning_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	err := w.StartPlanning(nil, "feature", "goal")
	if err == nil || err.Error() != "planning phase controller not initialized" {
		t.Errorf("Expected planning phase not initialized error, got: %v", err)
	}
}

func TestWorkflowManager_StartPlanning_NilPhase(t *testing.T) {
	w := &WorkflowManager{}
	err := w.StartPlanning(nil, "feature", "goal")
	if err == nil || err.Error() != "planning phase controller not initialized" {
		t.Errorf("Expected planning phase not initialized error, got: %v", err)
	}
}

func TestWorkflowManager_AddPlanningContext_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.AddPlanningContext([]string{"pattern"}, "style", []string{"file"})
}

func TestWorkflowManager_AddArchitectureDecision_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.AddArchitectureDecision(artifact.ArchitectureDecision{})
}

func TestWorkflowManager_AddCodeContract_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.AddCodeContract(artifact.CodeContract{})
}

func TestWorkflowManager_SetLayerMap_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.SetLayerMap(artifact.LayerMap{})
}

func TestWorkflowManager_AddTask_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.AddTask(artifact.TaskBreakdown{})
}

func TestWorkflowManager_SetCrossCuttingConcerns_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.SetCrossCuttingConcerns(artifact.CrossCuttingConcerns{})
}

func TestWorkflowManager_FinalizePlanning_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	_, err := w.FinalizePlanning()
	if err == nil || err.Error() != "no planning artifact to finalize" {
		t.Errorf("Expected no planning artifact error, got: %v", err)
	}
}

func TestWorkflowManager_StartExecution_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	err := w.StartExecution("path")
	if err == nil || err.Error() != "execution phase controller not initialized" {
		t.Errorf("Expected execution phase not initialized error, got: %v", err)
	}
}

func TestWorkflowManager_StartExecution_NilPhase(t *testing.T) {
	w := &WorkflowManager{}
	err := w.StartExecution("path")
	if err == nil || err.Error() != "execution phase controller not initialized" {
		t.Errorf("Expected execution phase not initialized error, got: %v", err)
	}
}

func TestWorkflowManager_RecordTaskProgress_NilTracker(t *testing.T) {
	w := &WorkflowManager{}
	err := w.RecordTaskProgress(artifact.TaskProgress{})
	if err == nil || err.Error() != "execution not started" {
		t.Errorf("Expected execution not started error, got: %v", err)
	}
}

func TestWorkflowManager_RecordExecutionPause_NilTracker(t *testing.T) {
	w := &WorkflowManager{}
	err := w.RecordExecutionPause(artifact.ExecutionPause{})
	if err == nil || err.Error() != "execution not started" {
		t.Errorf("Expected execution not started error, got: %v", err)
	}
}

func TestWorkflowManager_AddReviewChecklistItem_NilTracker(t *testing.T) {
	w := &WorkflowManager{}
	err := w.AddReviewChecklistItem("item")
	if err == nil || err.Error() != "execution not started" {
		t.Errorf("Expected execution not started error, got: %v", err)
	}
}

func TestWorkflowManager_CompleteExecution_NilTracker(t *testing.T) {
	w := &WorkflowManager{}
	err := w.CompleteExecution()
	if err == nil || err.Error() != "execution not started" {
		t.Errorf("Expected execution not started error, got: %v", err)
	}
}

func TestWorkflowManager_StartReview_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	err := w.StartReview("planning", "execution")
	if err == nil || err.Error() != "review phase controller not initialized" {
		t.Errorf("Expected review phase not initialized error, got: %v", err)
	}
}

func TestWorkflowManager_StartReview_NilPhase(t *testing.T) {
	w := &WorkflowManager{}
	err := w.StartReview("planning", "execution")
	if err == nil || err.Error() != "review phase controller not initialized" {
		t.Errorf("Expected review phase not initialized error, got: %v", err)
	}
}

func TestWorkflowManager_SetValidationStrategy_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.SetValidationStrategy(artifact.ValidationStrategy{})
}

func TestWorkflowManager_AddValidationResult_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.AddValidationResult(artifact.ValidationResult{})
}

func TestWorkflowManager_AddIssue_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.AddIssue(artifact.Issue{})
}

func TestWorkflowManager_AddReviewIteration_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.AddReviewIteration(artifact.ReviewIteration{})
}

func TestWorkflowManager_AddOpportunisticImprovement_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.AddOpportunisticImprovement(artifact.Improvement{})
}

func TestWorkflowManager_ApproveReview_NilArtifact(t *testing.T) {
	w := &WorkflowManager{}
	err := w.ApproveReview(artifact.Approval{})
	if err == nil || err.Error() != "no review in progress" {
		t.Errorf("Expected no review in progress error, got: %v", err)
	}
}

func TestWorkflowManager_GetReviewArtifact_Nil(t *testing.T) {
	w := &WorkflowManager{}
	if w.GetReviewArtifact() != nil {
		t.Error("Expected nil review artifact")
	}
}

func TestWorkflowManager_GetCurrentPhase(t *testing.T) {
	tests := []struct {
		name     string
		phase    WorkflowPhase
		expected WorkflowPhase
	}{
		{"planning phase", WorkflowPhasePlanning, WorkflowPhasePlanning},
		{"execution phase", WorkflowPhaseExecution, WorkflowPhaseExecution},
		{"review phase", WorkflowPhaseReview, WorkflowPhaseReview},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &WorkflowManager{currentPhase: tc.phase}
			if got := w.GetCurrentPhase(); got != tc.expected {
				t.Errorf("GetCurrentPhase() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestWorkflowManager_SetActiveAgent(t *testing.T) {
	tests := []struct {
		name          string
		agentName     string
		currentPhase  WorkflowPhase
		expectedAgent string
	}{
		{"set builder agent", "Builder", WorkflowPhasePlanning, "Builder"},
		{"set review agent", "Review", WorkflowPhaseReview, "Review"},
		{"empty string uses phase", "", WorkflowPhaseExecution, "execution"},
		{"whitespace uses phase", "   ", WorkflowPhasePlanning, "planning"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &WorkflowManager{currentPhase: tc.currentPhase}
			w.SetActiveAgent(tc.agentName)
			if got := w.GetActiveAgent(); got != tc.expectedAgent {
				t.Errorf("GetActiveAgent() = %v, want %v", got, tc.expectedAgent)
			}
		})
	}
}

func TestWorkflowManager_SetActiveAgent_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	// Should not panic
	w.SetActiveAgent("test")
}

func TestWorkflowManager_GetActiveAgent_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	if got := w.GetActiveAgent(); got != "" {
		t.Errorf("GetActiveAgent() on nil = %v, want empty", got)
	}
}

func TestWorkflowManager_PauseAndResume(t *testing.T) {
	w := &WorkflowManager{
		activityTracker: tool.NewActivityTracker(tool.DefaultActivityGroupingConfig()),
	}

	// Initially not paused
	paused, reason, question, at := w.GetPauseInfo()
	_ = reason   // Used below after pause
	_ = question // Used below after pause
	_ = at       // Used below after pause
	if paused {
		t.Error("Initially should not be paused")
	}

	// Pause the workflow
	err := w.Pause("test reason", "test question?")
	if err == nil {
		t.Error("Pause should return a WorkflowPauseError")
	}
	if !errors.Is(err, ErrWorkflowPaused) {
		t.Errorf("Pause error should wrap ErrWorkflowPaused, got: %v", err)
	}

	// Check pause state
	paused, reason, question, at = w.GetPauseInfo()
	if !paused {
		t.Error("Should be paused after Pause()")
	}
	if reason != "test reason" {
		t.Errorf("Pause reason = %q, want %q", reason, "test reason")
	}
	if question != "test question?" {
		t.Errorf("Pause question = %q, want %q", question, "test question?")
	}
	if at.IsZero() {
		t.Error("Pause time should be set")
	}

	// Resume the workflow
	w.Resume("resolved the issue")

	// Check resumed state
	paused, _, _, _ = w.GetPauseInfo()
	if paused {
		t.Error("Should not be paused after Resume()")
	}
}

func TestWorkflowManager_GetPauseInfo_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	paused, reason, question, at := w.GetPauseInfo()
	if paused {
		t.Error("Nil receiver should return not paused")
	}
	if reason != "" || question != "" {
		t.Error("Nil receiver should return empty strings")
	}
	if !at.IsZero() {
		t.Error("Nil receiver should return zero time")
	}
}

func TestWorkflowManager_ClearPause(t *testing.T) {
	w := &WorkflowManager{
		paused:        true,
		pauseReason:   "test",
		pauseQuestion: "question",
		pauseAt:       time.Now(),
	}

	w.ClearPause()

	if w.paused {
		t.Error("ClearPause should set paused to false")
	}
	if w.pauseReason != "" {
		t.Error("ClearPause should clear pauseReason")
	}
	if w.pauseQuestion != "" {
		t.Error("ClearPause should clear pauseQuestion")
	}
	if !w.pauseAt.IsZero() {
		t.Error("ClearPause should clear pauseAt")
	}
}

func TestWorkflowManager_ClearPause_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	// Should not panic
	w.ClearPause()
}

func TestWorkflowManager_Resume_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	// Should not panic
	w.Resume("test")
}

func TestWorkflowManager_GetCurrentPlan(t *testing.T) {
	plan := &Plan{ID: "test-plan", FeatureName: "Test"}
	w := &WorkflowManager{planRef: plan}

	if got := w.GetCurrentPlan(); got != plan {
		t.Error("GetCurrentPlan should return the plan reference")
	}
}

func TestWorkflowManager_GetCurrentPlan_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	if got := w.GetCurrentPlan(); got != nil {
		t.Error("Nil receiver should return nil plan")
	}
}

func TestWorkflowManager_SetSessionID(t *testing.T) {
	w := &WorkflowManager{}
	w.SetSessionID("  session-123  ")
	if w.sessionID != "session-123" {
		t.Errorf("SetSessionID should trim whitespace, got %q", w.sessionID)
	}
}

func TestWorkflowManager_SetSessionID_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	// Should not panic
	w.SetSessionID("test")
}

func TestWorkflowManager_ProjectRoot(t *testing.T) {
	w := &WorkflowManager{projectRoot: "/path/to/project"}
	if got := w.ProjectRoot(); got != "/path/to/project" {
		t.Errorf("ProjectRoot() = %q, want %q", got, "/path/to/project")
	}
}

func TestWorkflowManager_ProjectRoot_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	if got := w.ProjectRoot(); got != "" {
		t.Errorf("Nil receiver ProjectRoot() = %q, want empty", got)
	}
}

func TestWorkflowManager_PersonaProvider_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	if got := w.PersonaProvider(); got != nil {
		t.Error("Nil receiver should return nil persona provider")
	}
}

func TestWorkflowManager_PersonaSection_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	if got := w.PersonaSection("planning"); got != "" {
		t.Errorf("Nil receiver PersonaSection() = %q, want empty", got)
	}
}

func TestWorkflowManager_PersonaOverrides_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	overrides := w.PersonaOverrides()
	if overrides == nil || len(overrides) != 0 {
		t.Error("Nil receiver should return empty map")
	}
}

func TestWorkflowManager_SetSteeringNotes(t *testing.T) {
	w := &WorkflowManager{}
	w.SetSteeringNotes("  focus on performance  ")
	if w.steeringNotes != "focus on performance" {
		t.Errorf("SetSteeringNotes should trim, got %q", w.steeringNotes)
	}
}

func TestWorkflowManager_SetSteeringNotes_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	// Should not panic
	w.SetSteeringNotes("test")
}

func TestWorkflowManager_SetAutonomyLevel(t *testing.T) {
	w := &WorkflowManager{}
	w.SetAutonomyLevel("  autonomous  ")
	if w.autonomyLevel != "autonomous" {
		t.Errorf("SetAutonomyLevel should trim, got %q", w.autonomyLevel)
	}
}

func TestWorkflowManager_SetAutonomyLevel_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	// Should not panic
	w.SetAutonomyLevel("test")
}

func TestWorkflowManager_TaskPhases(t *testing.T) {
	phases := []TaskPhase{
		{Name: "phase1", Stage: "builder"},
		{Name: "phase2", Stage: "review"},
	}
	w := &WorkflowManager{taskPhases: phases}

	got := w.TaskPhases()
	if len(got) != 2 {
		t.Errorf("TaskPhases() returned %d phases, want 2", len(got))
	}

	// Verify it returns a copy
	got[0].Name = "modified"
	if w.taskPhases[0].Name == "modified" {
		t.Error("TaskPhases should return a copy, not the original slice")
	}
}

func TestWorkflowManager_TaskPhases_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	if got := w.TaskPhases(); got != nil {
		t.Error("Nil receiver should return nil")
	}
}

func TestWorkflowManager_SendProgress(t *testing.T) {
	w := &WorkflowManager{}
	w.EnableProgressStreaming()
	defer w.DisableProgressStreaming()

	// Send a message
	w.SendProgress("test message")

	// Read the message
	select {
	case msg := <-w.GetProgressChan():
		if msg != "test message" {
			t.Errorf("Received message %q, want %q", msg, "test message")
		}
	default:
		t.Error("Expected message in progress channel")
	}
}

func TestWorkflowManager_SendProgress_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	// Should not panic
	w.SendProgress("test")
}

func TestWorkflowManager_SendProgress_NilChannel(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic when channel is nil
	w.SendProgress("test")
}

func TestWorkflowManager_EnableDisableProgressStreaming(t *testing.T) {
	w := &WorkflowManager{}

	// Initially nil
	if w.GetProgressChan() != nil {
		t.Error("Initially channel should be nil")
	}

	// Enable
	w.EnableProgressStreaming()
	if w.GetProgressChan() == nil {
		t.Error("After Enable, channel should not be nil")
	}

	// Disable
	w.DisableProgressStreaming()
	// Note: GetProgressChan() returns the channel even if closed
	// Just ensure it doesn't panic
}

func TestWorkflowManager_EnableProgressStreaming_NilReceiver(t *testing.T) {
	// Note: Calling EnableProgressStreaming on nil would panic,
	// so we just document this is expected behavior
}

func TestWorkflowManager_GetActivitySummaries(t *testing.T) {
	w := &WorkflowManager{
		activityTracker: tool.NewActivityTracker(tool.DefaultActivityGroupingConfig()),
	}

	summaries := w.GetActivitySummaries()
	// GetActivitySummaries returns nil when there are no summaries, which is acceptable
	if summaries != nil && len(summaries) != 0 {
		t.Error("Should return empty or nil slice")
	}
}

func TestWorkflowManager_GetActivitySummaries_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	if got := w.GetActivitySummaries(); got != nil {
		t.Error("Nil receiver should return nil")
	}
}

func TestWorkflowManager_RecordIntent(t *testing.T) {
	w := &WorkflowManager{
		intentHistory: tool.NewIntentHistory(),
	}

	intent := tool.Intent{
		Phase:    "execution",
		Activity: "Test intent",
	}
	w.RecordIntent(intent)

	latest := w.GetLatestIntent()
	if latest == nil {
		t.Fatal("GetLatestIntent returned nil")
	}
	if latest.Activity != "Test intent" {
		t.Errorf("Latest intent activity = %q, want %q", latest.Activity, "Test intent")
	}
}

func TestWorkflowManager_GetActivityGroups(t *testing.T) {
	w := &WorkflowManager{
		activityTracker: tool.NewActivityTracker(tool.DefaultActivityGroupingConfig()),
	}

	groups := w.GetActivityGroups()
	if groups == nil {
		t.Error("Should return empty slice, not nil")
	}
}

func TestWorkflowManager_RecordToolCall_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	// Should not panic
	w.RecordToolCall(nil, nil, nil, time.Now(), time.Now())
}

func TestWorkflowManager_AuthorizeToolCall_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	err := w.AuthorizeToolCall(nil, nil)
	if err != nil {
		t.Errorf("Nil receiver should return nil error, got: %v", err)
	}
}

func TestWorkflowManager_GetResearchHighlights_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	summary, risks := w.GetResearchHighlights()
	if summary != "" {
		t.Errorf("Nil receiver summary = %q, want empty", summary)
	}
	if risks != nil {
		t.Error("Nil receiver should return nil risks")
	}
}

func TestWorkflowManager_GetResearchBrief_Nil(t *testing.T) {
	w := &WorkflowManager{}
	if w.GetResearchBrief() != nil {
		t.Error("Should return nil when no brief set")
	}
}

func TestWorkflowManager_EnrichPlan(t *testing.T) {
	w := &WorkflowManager{
		latestResearchBrief: &artifact.ResearchBrief{
			Feature: "Test Feature",
			Summary: "Research summary",
			Risks:   []string{"Risk 1", "Risk 2"},
		},
	}

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
	}

	w.EnrichPlan(plan)

	if plan.Context.ResearchSummary != "Research summary" {
		t.Errorf("ResearchSummary = %q, want %q", plan.Context.ResearchSummary, "Research summary")
	}
	if len(plan.Context.ResearchRisks) != 2 {
		t.Errorf("ResearchRisks length = %d, want 2", len(plan.Context.ResearchRisks))
	}
}

func TestWorkflowManager_EnrichPlan_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	plan := &Plan{ID: "test"}
	// Should not panic
	w.EnrichPlan(plan)
}

func TestWorkflowManager_EnrichPlan_NilPlan(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.EnrichPlan(nil)
}

func TestWorkflowManager_PublishTelemetry_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	// Should not panic
	w.PublishTelemetry(telemetry.Event{})
}

func TestWorkflowManager_EmitPlanSnapshot_NilPlan(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.EmitPlanSnapshot(nil, telemetry.EventPlanUpdated)
}

func TestWorkflowManager_EmitTaskEvent_NilTask(t *testing.T) {
	w := &WorkflowManager{}
	// Should not panic
	w.EmitTaskEvent(nil, telemetry.EventTaskStarted)
}

func TestWorkflowManager_EmitBuilderEvent_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	// Should not panic
	w.EmitBuilderEvent(nil, telemetry.EventBuilderStarted, nil)
}

func TestWorkflowManager_EmitResearchEvent_NilReceiver(t *testing.T) {
	var w *WorkflowManager
	// Should not panic
	w.EmitResearchEvent("feature", telemetry.EventResearchStarted, nil)
}

func TestIsSafeCommand(t *testing.T) {
	tests := []struct {
		cmd      string
		expected bool
	}{
		{"go test ./...", true},
		{"go build -o app ./cmd/app", true},
		{"go run main.go", true},
		{"go mod tidy", true},
		{"go get github.com/pkg/errors", true},
		{"npm test", true},
		{"npm run build", true},
		{"npm install", true},
		{"cargo test", true},
		{"cargo build", true},
		{"make test", true},
		{"pytest", true},
		{"python -m pytest tests/", true},
		{"jest", true},
		{"mocha", true},
		{"ls -la", true},
		{"cat file.txt", true},
		{"grep pattern file", true},
		{"find . -name '*.go'", true},
		{"echo hello", true},
		{"pwd", true},
		{"which go", true},
		{"git status", true},
		{"git log", true},
		{"gh pr list", true},
		{"rm -rf /", false},
		{"sudo apt install", false},
		{"curl http://example.com", false},
		{"wget http://example.com", false},
		{"docker run", false},
		{"", true}, // Empty is considered safe
	}

	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			got := isSafeCommand(tc.cmd)
			if got != tc.expected {
				t.Errorf("isSafeCommand(%q) = %v, want %v", tc.cmd, got, tc.expected)
			}
		})
	}
}

func TestRequiresElevation(t *testing.T) {
	tests := []struct {
		cmd      string
		expected bool
	}{
		{"sudo apt update", true},
		{"sudo", true},
		{"sudo\tapt install", true},
		{"echo hello && sudo rm -rf /", true},
		{"ls || sudo cat /etc/passwd", true},
		{"go test; sudo whoami", true},
		{"go test ./...", false},
		{"rm file.txt", false},
		{"", false},
		{"   ", false},
		{"SUDO=false go test", false}, // SUDO in value, not command
	}

	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			got := requiresElevation(tc.cmd)
			if got != tc.expected {
				t.Errorf("requiresElevation(%q) = %v, want %v", tc.cmd, got, tc.expected)
			}
		})
	}
}

func TestSplitShellSegments(t *testing.T) {
	tests := []struct {
		cmd      string
		expected int // number of segments
	}{
		{"single command", 1},
		{"cmd1 && cmd2", 2},
		{"cmd1 || cmd2", 2},
		{"cmd1; cmd2", 2},
		{"cmd1\ncmd2", 2},
		{"cmd1 && cmd2 || cmd3; cmd4", 4},
		{"", 1},
	}

	for _, tc := range tests {
		t.Run(tc.cmd, func(t *testing.T) {
			segments := splitShellSegments(tc.cmd)
			if len(segments) != tc.expected {
				t.Errorf("splitShellSegments(%q) = %d segments, want %d", tc.cmd, len(segments), tc.expected)
			}
		})
	}
}

func TestSummarizeCommand(t *testing.T) {
	tests := []struct {
		cmd      string
		expected string
	}{
		{"short", "short"},
		{"  whitespace  ", "whitespace"},
		{strings.Repeat("a", 80), strings.Repeat("a", 80)},
		{strings.Repeat("a", 81), strings.Repeat("a", 80) + "…"},
		{strings.Repeat("a", 100), strings.Repeat("a", 80) + "…"},
	}

	for _, tc := range tests {
		testName := tc.cmd
		if len(testName) > 20 {
			testName = testName[:20]
		}
		t.Run(testName, func(t *testing.T) {
			got := summarizeCommand(tc.cmd)
			if got != tc.expected {
				t.Errorf("summarizeCommand() = %q, want %q", got, tc.expected)
			}
		})
	}
}
