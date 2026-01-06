package orchestrator

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/orchestrator/mocks"
	"github.com/odvcencio/buckley/pkg/storage"
	"go.uber.org/mock/gomock"
)

func TestNewPlanner(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	mockPlanStore := NewMockPlanStore(ctrl)
	cfg := config.DefaultConfig()

	// Create a temporary store for testing
	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, mockPlanStore)

	if planner == nil {
		t.Fatal("NewPlanner returned nil")
	}
	if planner.planStore != mockPlanStore {
		t.Error("planStore not set correctly")
	}
}

func TestNewPlanner_CreatesDefaultPlanStore(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	cfg := config.DefaultConfig()
	cfg.Artifacts.PlanningDir = "docs/plans"

	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, nil)

	if planner == nil {
		t.Fatal("NewPlanner returned nil")
	}
	if planner.planStore == nil {
		t.Error("planStore should be created when nil is passed")
	}
}

func TestPlanner_SavePlan_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	mockPlanStore := NewMockPlanStore(ctrl)
	cfg := config.DefaultConfig()

	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, mockPlanStore)

	plan := &Plan{
		ID:          "test-plan-123",
		FeatureName: "Test Feature",
		Description: "Test description",
		CreatedAt:   time.Now(),
		Tasks: []Task{
			{
				ID:          "1",
				Title:       "Task 1",
				Description: "First task",
				Type:        TaskTypeImplementation,
			},
		},
	}

	mockPlanStore.EXPECT().SavePlan(plan).Return(nil)

	err = planner.SavePlan(plan)
	if err != nil {
		t.Errorf("SavePlan() error = %v", err)
	}
}

func TestPlanner_SavePlan_Error(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	mockPlanStore := NewMockPlanStore(ctrl)
	cfg := config.DefaultConfig()

	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, mockPlanStore)

	plan := &Plan{
		ID:          "test-plan-456",
		FeatureName: "Test Feature",
	}

	expectedError := errors.New("disk full")
	mockPlanStore.EXPECT().SavePlan(plan).Return(expectedError)

	err = planner.SavePlan(plan)
	if err == nil {
		t.Error("SavePlan() should return error")
	}
	if err != expectedError {
		t.Errorf("SavePlan() error = %v, want %v", err, expectedError)
	}
}

func TestPlanner_LoadPlan_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	mockPlanStore := NewMockPlanStore(ctrl)
	cfg := config.DefaultConfig()

	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, mockPlanStore)

	planID := "test-plan-789"
	expectedPlan := &Plan{
		ID:          planID,
		FeatureName: "Loaded Feature",
		Description: "Loaded description",
		Tasks: []Task{
			{ID: "1", Title: "Task 1"},
			{ID: "2", Title: "Task 2"},
		},
	}

	mockPlanStore.EXPECT().LoadPlan(planID).Return(expectedPlan, nil)

	plan, err := planner.LoadPlan(planID)
	if err != nil {
		t.Errorf("LoadPlan() error = %v", err)
	}
	if plan == nil {
		t.Fatal("LoadPlan() returned nil plan")
	}
	if plan.ID != planID {
		t.Errorf("LoadPlan() ID = %s, want %s", plan.ID, planID)
	}
	if len(plan.Tasks) != 2 {
		t.Errorf("LoadPlan() task count = %d, want 2", len(plan.Tasks))
	}
}

func TestPlanner_LoadPlan_NotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	mockPlanStore := NewMockPlanStore(ctrl)
	cfg := config.DefaultConfig()

	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, mockPlanStore)

	planID := "nonexistent-plan"
	expectedError := errors.New("plan not found")

	mockPlanStore.EXPECT().LoadPlan(planID).Return(nil, expectedError)

	plan, err := planner.LoadPlan(planID)
	if err == nil {
		t.Error("LoadPlan() should return error for nonexistent plan")
	}
	if plan != nil {
		t.Error("LoadPlan() should return nil plan on error")
	}
}

func TestPlanner_ListPlans_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	mockPlanStore := NewMockPlanStore(ctrl)
	cfg := config.DefaultConfig()

	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, mockPlanStore)

	expectedPlans := []Plan{
		{ID: "plan1", FeatureName: "Feature 1"},
		{ID: "plan2", FeatureName: "Feature 2"},
		{ID: "plan3", FeatureName: "Feature 3"},
	}

	mockPlanStore.EXPECT().ListPlans().Return(expectedPlans, nil)

	plans, err := planner.ListPlans()
	if err != nil {
		t.Errorf("ListPlans() error = %v", err)
	}
	if len(plans) != 3 {
		t.Errorf("ListPlans() count = %d, want 3", len(plans))
	}
	if plans[0].ID != "plan1" {
		t.Errorf("ListPlans()[0].ID = %s, want plan1", plans[0].ID)
	}
}

func TestPlanner_ListPlans_Empty(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	mockPlanStore := NewMockPlanStore(ctrl)
	cfg := config.DefaultConfig()

	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, mockPlanStore)

	mockPlanStore.EXPECT().ListPlans().Return([]Plan{}, nil)

	plans, err := planner.ListPlans()
	if err != nil {
		t.Errorf("ListPlans() error = %v", err)
	}
	if len(plans) != 0 {
		t.Errorf("ListPlans() count = %d, want 0", len(plans))
	}
}

func TestPlanner_ListPlans_Error(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	mockPlanStore := NewMockPlanStore(ctrl)
	cfg := config.DefaultConfig()

	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, mockPlanStore)

	expectedError := errors.New("directory not found")
	mockPlanStore.EXPECT().ListPlans().Return(nil, expectedError)

	plans, err := planner.ListPlans()
	if err == nil {
		t.Error("ListPlans() should return error")
	}
	if plans != nil {
		t.Error("ListPlans() should return nil on error")
	}
}

func TestPlanner_UpdatePlan_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	mockPlanStore := NewMockPlanStore(ctrl)
	cfg := config.DefaultConfig()

	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, mockPlanStore)

	plan := &Plan{
		ID:          "test-plan-update",
		FeatureName: "Updated Feature",
		Tasks: []Task{
			{ID: "1", Title: "Updated Task", Status: TaskCompleted},
		},
	}

	// UpdatePlan calls SavePlan internally
	mockPlanStore.EXPECT().SavePlan(plan).Return(nil)

	err = planner.UpdatePlan(plan)
	if err != nil {
		t.Errorf("UpdatePlan() error = %v", err)
	}
}

func TestPlanner_UpdatePlan_Error(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	mockPlanStore := NewMockPlanStore(ctrl)
	cfg := config.DefaultConfig()

	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, mockPlanStore)

	plan := &Plan{
		ID:          "test-plan-error",
		FeatureName: "Error Feature",
	}

	expectedError := errors.New("permission denied")
	mockPlanStore.EXPECT().SavePlan(plan).Return(expectedError)

	err = planner.UpdatePlan(plan)
	if err == nil {
		t.Error("UpdatePlan() should return error")
	}
	if err != expectedError {
		t.Errorf("UpdatePlan() error = %v, want %v", err, expectedError)
	}
}

func TestTaskType_Constants(t *testing.T) {
	if TaskTypeImplementation != "implementation" {
		t.Errorf("TaskTypeImplementation = %s, want implementation", TaskTypeImplementation)
	}
	if TaskTypeAnalysis != "analysis" {
		t.Errorf("TaskTypeAnalysis = %s, want analysis", TaskTypeAnalysis)
	}
	if TaskTypeValidation != "validation" {
		t.Errorf("TaskTypeValidation = %s, want validation", TaskTypeValidation)
	}
}

func TestTaskStatus_Constants(t *testing.T) {
	if TaskPending != 0 {
		t.Errorf("TaskPending = %d, want 0", TaskPending)
	}
	if TaskInProgress != 1 {
		t.Errorf("TaskInProgress = %d, want 1", TaskInProgress)
	}
	if TaskCompleted != 2 {
		t.Errorf("TaskCompleted = %d, want 2", TaskCompleted)
	}
	if TaskFailed != 3 {
		t.Errorf("TaskFailed = %d, want 3", TaskFailed)
	}
	if TaskSkipped != 4 {
		t.Errorf("TaskSkipped = %d, want 4", TaskSkipped)
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Hello World", "hello-world"},
		{"test", "test"},
		{"Test Feature 123", "test-feature-123"},
		{"foo@bar#baz", "foobarbaz"},
		{"a-b-c", "a-b-c"},
		{"", ""},
		{"UPPER CASE", "upper-case"},
		{"  spaces  ", "--spaces--"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := slugify(tc.input)
			if got != tc.expected {
				t.Errorf("slugify(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestTruncateSummary(t *testing.T) {
	tests := []struct {
		input    string
		max      int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10c", 10, "exactly10c"},
		{"this is longer", 10, "this is..."},
		{"", 5, ""},
		{"abc", 100, "abc"},
	}

	for _, tc := range tests {
		got := truncateSummary(tc.input, tc.max)
		if got != tc.expected {
			t.Errorf("truncateSummary(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.expected)
		}
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10c", 10, "exactly10c"},
		{"this is a longer string", 10, "this is a ..."},
		{"", 5, ""},
	}

	for _, tc := range tests {
		got := truncateString(tc.input, tc.maxLen)
		if got != tc.expected {
			t.Errorf("truncateString(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.expected)
		}
	}
}

func TestSanitizeJSONString(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "valid json unchanged",
			input:    `{"key": "value"}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "fixes invalid pipe escape",
			input:    `foo\|bar`,
			expected: `foo|bar`,
		},
		{
			name:     "fixes invalid parentheses escape",
			input:    `\(test\)`,
			expected: `(test)`,
		},
		{
			name:     "preserves valid escapes",
			input:    `"test\n\r\t"`,
			expected: `"test\n\r\t"`,
		},
		{
			name:     "fixes multiple invalid escapes",
			input:    `\[\]\{\}\<\>`,
			expected: `[]{}<>`,
		},
		{
			name:     "handles underscore and asterisk",
			input:    `\_test\*`,
			expected: `_test*`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeJSONString(tc.input)
			if got != tc.expected {
				t.Errorf("sanitizeJSONString(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestContainsAnalysisKeywords(t *testing.T) {
	tests := []struct {
		title       string
		description string
		expected    bool
	}{
		{"Analyze code coverage", "", true},
		{"Run analysis", "", true},
		{"Generate coverage report", "", true},
		{"Investigate issue", "", true},
		{"Examine the logs", "", true},
		{"Check status", "", true},
		{"Run coverage", "", true},
		{"Gather data", "", true},
		{"Collect metrics", "", true},
		{"Measure performance", "", true},
		{"Get statistics", "", true},
		{"Scan for issues", "", true},
		{"Detect problems", "", true},
		{"Build feature", "", false},
		{"Fix bug", "", false},
		{"", "analyze this", true},
	}

	for _, tc := range tests {
		t.Run(tc.title, func(t *testing.T) {
			got := containsAnalysisKeywords(tc.title, tc.description)
			if got != tc.expected {
				t.Errorf("containsAnalysisKeywords(%q, %q) = %v, want %v", tc.title, tc.description, got, tc.expected)
			}
		})
	}
}

func TestContainsValidationKeywords(t *testing.T) {
	tests := []struct {
		title       string
		description string
		expected    bool
	}{
		{"Run tests", "", true},
		{"Validate input", "", true},
		{"Verify behavior", "", true},
		{"Check errors", "", true},
		{"Lint code", "", true},
		{"Quality check", "", true},
		{"Ensure correctness", "", true},
		{"Confirm results", "", true},
		{"Assert values", "", true},
		{"Code review", "", true},
		{"Build feature", "", false},
		{"Fix bug", "", false},
		{"", "test this", true},
	}

	for _, tc := range tests {
		t.Run(tc.title, func(t *testing.T) {
			got := containsValidationKeywords(tc.title, tc.description)
			if got != tc.expected {
				t.Errorf("containsValidationKeywords(%q, %q) = %v, want %v", tc.title, tc.description, got, tc.expected)
			}
		})
	}
}

func TestPlanner_SetPersonaProvider(t *testing.T) {
	// Test nil planner safety
	var nilPlanner *Planner
	nilPlanner.SetPersonaProvider(nil) // Should not panic

	// Test with valid planner
	planner := &Planner{
		config: &config.Config{},
	}
	planner.SetPersonaProvider(nil)
	if planner.personaProvider != nil {
		t.Error("Expected personaProvider to be nil")
	}
}

// Plan validation tests

func TestPlanner_GeneratePlan_EmptyFeatureName(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	mockPlanStore := NewMockPlanStore(ctrl)
	cfg := config.DefaultConfig()

	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, mockPlanStore)

	_, err = planner.GeneratePlan("", "description")
	if err == nil {
		t.Error("GeneratePlan should return error for empty feature name")
	}
	if err.Error() != "feature name cannot be empty" {
		t.Errorf("Expected 'feature name cannot be empty' error, got: %v", err)
	}
}

func TestPlanner_GeneratePlan_EmptyDescription(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := mocks.NewMockModelClient(ctrl)
	mockPlanStore := NewMockPlanStore(ctrl)
	cfg := config.DefaultConfig()

	tempDB := t.TempDir() + "/test.db"
	store, err := storage.New(tempDB)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	defer store.Close()

	planner := NewPlanner(mockClient, cfg, store, nil, mockPlanStore)

	_, err = planner.GeneratePlan("Feature", "")
	if err == nil {
		t.Error("GeneratePlan should return error for empty description")
	}
	if err.Error() != "description cannot be empty" {
		t.Errorf("Expected 'description cannot be empty' error, got: %v", err)
	}
}

func TestPlanner_SavePlan_NilPlanStore(t *testing.T) {
	planner := &Planner{planStore: nil}
	err := planner.SavePlan(&Plan{ID: "test"})
	if err == nil {
		t.Error("SavePlan should return error when planStore is nil")
	}
	if err.Error() != "plan store not initialized" {
		t.Errorf("Expected 'plan store not initialized' error, got: %v", err)
	}
}

func TestPlanner_LoadPlan_NilPlanStore(t *testing.T) {
	planner := &Planner{planStore: nil}
	_, err := planner.LoadPlan("test")
	if err == nil {
		t.Error("LoadPlan should return error when planStore is nil")
	}
	if err.Error() != "plan store not initialized" {
		t.Errorf("Expected 'plan store not initialized' error, got: %v", err)
	}
}

func TestPlanner_ListPlans_NilPlanStore(t *testing.T) {
	planner := &Planner{planStore: nil}
	_, err := planner.ListPlans()
	if err == nil {
		t.Error("ListPlans should return error when planStore is nil")
	}
	if err.Error() != "plan store not initialized" {
		t.Errorf("Expected 'plan store not initialized' error, got: %v", err)
	}
}

func TestPlanValidation_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		plan        *Plan
		expectValid bool
		errSubstr   string
	}{
		{
			name: "valid plan with tasks",
			plan: &Plan{
				ID:          "test-plan",
				FeatureName: "Test Feature",
				Description: "Test description",
				Tasks: []Task{
					{ID: "1", Title: "Task 1", Type: TaskTypeImplementation},
				},
			},
			expectValid: true,
		},
		{
			name: "valid plan with multiple task types",
			plan: &Plan{
				ID:          "multi-task-plan",
				FeatureName: "Multi Task Feature",
				Tasks: []Task{
					{ID: "1", Title: "Implement", Type: TaskTypeImplementation},
					{ID: "2", Title: "Analyze", Type: TaskTypeAnalysis},
					{ID: "3", Title: "Validate", Type: TaskTypeValidation},
				},
			},
			expectValid: true,
		},
		{
			name: "valid plan with dependencies",
			plan: &Plan{
				ID:          "dep-plan",
				FeatureName: "Dependency Feature",
				Tasks: []Task{
					{ID: "1", Title: "First", Dependencies: []string{}},
					{ID: "2", Title: "Second", Dependencies: []string{"1"}},
					{ID: "3", Title: "Third", Dependencies: []string{"1", "2"}},
				},
			},
			expectValid: true,
		},
		{
			name: "plan with all task statuses",
			plan: &Plan{
				ID:          "status-plan",
				FeatureName: "Status Feature",
				Tasks: []Task{
					{ID: "1", Title: "Pending", Status: TaskPending},
					{ID: "2", Title: "In Progress", Status: TaskInProgress},
					{ID: "3", Title: "Completed", Status: TaskCompleted},
					{ID: "4", Title: "Failed", Status: TaskFailed},
					{ID: "5", Title: "Skipped", Status: TaskSkipped},
				},
			},
			expectValid: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store := NewFilePlanStore(tempDir)

			err := store.SavePlan(tc.plan)
			if tc.expectValid {
				if err != nil {
					t.Errorf("Expected valid plan, got error: %v", err)
				}
			} else {
				if err == nil {
					t.Error("Expected error for invalid plan")
				}
			}
		})
	}
}

func TestParsePlan_JSONExtraction(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantTasks int
		wantErr   bool
	}{
		{
			name: "plain json",
			content: `{
				"description": "Test",
				"tasks": [{"id": "1", "title": "Task 1", "description": "Test"}]
			}`,
			wantTasks: 1,
			wantErr:   false,
		},
		{
			name: "json in markdown code block",
			content: "Here's the plan:\n```json\n" + `{
				"description": "Test",
				"tasks": [{"id": "1", "title": "Task 1", "description": "Test"}]
			}` + "\n```\n",
			wantTasks: 1,
			wantErr:   false,
		},
		{
			name: "json in generic code block",
			content: "```\n" + `{
				"description": "Test",
				"tasks": [{"id": "1", "title": "Task 1", "description": "Test"}]
			}` + "\n```",
			wantTasks: 1,
			wantErr:   false,
		},
		{
			name:      "invalid json",
			content:   `{invalid json}`,
			wantTasks: 0,
			wantErr:   true,
		},
		{
			name:      "empty tasks",
			content:   `{"description": "Test", "tasks": []}`,
			wantTasks: 0,
			wantErr:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &Planner{}
			plan, err := p.parsePlan(tc.content, "Test Feature")

			if tc.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if plan != nil && len(plan.Tasks) != tc.wantTasks {
					t.Errorf("Tasks count = %d, want %d", len(plan.Tasks), tc.wantTasks)
				}
			}
		})
	}
}

func TestParsePlan_TaskTypeInference(t *testing.T) {
	tests := []struct {
		name         string
		title        string
		description  string
		hasFiles     bool
		expectedType TaskType
	}{
		{
			name:         "task with files gets implementation type",
			title:        "Add feature",
			hasFiles:     true,
			expectedType: TaskTypeImplementation,
		},
		{
			name:         "analyze task",
			title:        "Analyze code coverage",
			hasFiles:     false,
			expectedType: TaskTypeAnalysis,
		},
		{
			name:         "run tests task",
			title:        "Run tests",
			hasFiles:     false,
			expectedType: TaskTypeValidation,
		},
		{
			name:         "validate task",
			title:        "Validate output",
			hasFiles:     false,
			expectedType: TaskTypeValidation,
		},
		{
			name:         "investigate task",
			title:        "Investigate issue",
			hasFiles:     false,
			expectedType: TaskTypeAnalysis,
		},
		{
			name:         "default to implementation",
			title:        "Do something",
			hasFiles:     false,
			expectedType: TaskTypeImplementation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := []string{}
			if tc.hasFiles {
				files = []string{"file.go"}
			}

			content := `{
				"description": "Test",
				"tasks": [{"id": "1", "title": "` + tc.title + `", "description": "` + tc.description + `", "files": ` + marshalFiles(files) + `}]
			}`

			p := &Planner{}
			plan, err := p.parsePlan(content, "Test Feature")
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if len(plan.Tasks) == 0 {
				t.Fatal("Expected at least one task")
			}

			if plan.Tasks[0].Type != tc.expectedType {
				t.Errorf("Task type = %s, want %s", plan.Tasks[0].Type, tc.expectedType)
			}
		})
	}
}

func marshalFiles(files []string) string {
	if len(files) == 0 {
		return "[]"
	}
	result := "["
	for i, f := range files {
		if i > 0 {
			result += ","
		}
		result += `"` + f + `"`
	}
	result += "]"
	return result
}

func TestDetectProjectType(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(dir string)
		expected string
	}{
		{
			name: "go project",
			setup: func(dir string) {
				os.WriteFile(dir+"/go.mod", []byte("module test"), 0644)
			},
			expected: "go",
		},
		{
			name: "node project",
			setup: func(dir string) {
				os.WriteFile(dir+"/package.json", []byte(`{"name": "test"}`), 0644)
			},
			expected: "node",
		},
		{
			name: "rust project",
			setup: func(dir string) {
				os.WriteFile(dir+"/Cargo.toml", []byte("[package]"), 0644)
			},
			expected: "rust",
		},
		{
			name:     "unknown project",
			setup:    func(dir string) {},
			expected: "unknown",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			tc.setup(tempDir)

			result := detectProjectType(tempDir)
			if result != tc.expected {
				t.Errorf("detectProjectType() = %s, want %s", result, tc.expected)
			}
		})
	}
}

func TestDetectDependencies(t *testing.T) {
	tests := []struct {
		name      string
		goModFile string
		wantDeps  int
	}{
		{
			name:      "no go.mod",
			goModFile: "",
			wantDeps:  0,
		},
		{
			name: "go.mod with deps",
			goModFile: `module test

go 1.21

require (
	github.com/pkg/errors v0.9.1
	golang.org/x/sync v0.3.0
)`,
			wantDeps: 2,
		},
		{
			name: "go.mod with many deps (limited to 5)",
			goModFile: `module test

go 1.21

require (
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.8.0
	github.com/spf13/cobra v1.7.0
	github.com/spf13/viper v1.16.0
	golang.org/x/sync v0.3.0
	golang.org/x/tools v0.12.0
	golang.org/x/text v0.11.0
)`,
			wantDeps: 5, // Limited to top 5
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			if tc.goModFile != "" {
				os.WriteFile(tempDir+"/go.mod", []byte(tc.goModFile), 0644)
			}

			deps := detectDependencies(tempDir)
			if len(deps) != tc.wantDeps {
				t.Errorf("detectDependencies() returned %d deps, want %d", len(deps), tc.wantDeps)
			}
		})
	}
}

func TestSanitizeRemoteURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"   ", ""},
		{"git@github.com:user/repo.git", "git@github.com:user/repo.git"},
		{"https://github.com/user/repo.git", "https://github.com/user/repo.git"},
		{"https://user:pass@github.com/user/repo.git", "https://github.com/user/repo.git"},
		{"http://user:token@gitlab.com/group/project", "http://gitlab.com/group/project"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := sanitizeRemoteURL(tc.input)
			if got != tc.expected {
				t.Errorf("sanitizeRemoteURL(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestSafeModelName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "the planning model"},
		{"   ", "the planning model"},
		{"gpt-4", "gpt-4"},
		{"claude-3-opus", "claude-3-opus"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := safeModelName(tc.input)
			if got != tc.expected {
				t.Errorf("safeModelName(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestDefaultPlanningSystemPrompt(t *testing.T) {
	// Test without persona section
	prompt := defaultPlanningSystemPrompt(false, "")
	if prompt == "" {
		t.Error("defaultPlanningSystemPrompt should not return empty string")
	}
	if !strings.Contains(prompt, "software architect") {
		t.Error("Prompt should contain 'software architect'")
	}
	if !strings.Contains(prompt, "JSON") {
		t.Error("Prompt should mention JSON format")
	}

	// Test with persona section
	promptWithPersona := defaultPlanningSystemPrompt(false, "Be concise and direct.")
	if !strings.Contains(promptWithPersona, "Be concise and direct.") {
		t.Error("Prompt should include persona section")
	}
	if !strings.Contains(promptWithPersona, "Persona Voice") {
		t.Error("Prompt should include Persona Voice header")
	}
}

func TestBuildPlanningPrompt(t *testing.T) {
	p := &Planner{}
	ctx := PlanContext{
		ProjectType:  "go",
		GitBranch:    "main",
		Dependencies: []string{"github.com/pkg/errors"},
	}

	prompt := p.buildPlanningPrompt("Test Feature", "Add new functionality", ctx, "")

	if !strings.Contains(prompt, "Test Feature") {
		t.Error("Prompt should contain feature name")
	}
	if !strings.Contains(prompt, "Add new functionality") {
		t.Error("Prompt should contain description")
	}
	if !strings.Contains(prompt, "go") {
		t.Error("Prompt should contain project type")
	}
	if !strings.Contains(prompt, "main") {
		t.Error("Prompt should contain git branch")
	}
	if !strings.Contains(prompt, "github.com/pkg/errors") {
		t.Error("Prompt should contain dependencies")
	}
}

func TestBuildPlanningPrompt_WithIndexHints(t *testing.T) {
	p := &Planner{}
	ctx := PlanContext{
		ProjectType: "go",
		GitBranch:   "feature",
	}

	indexHints := "- pkg/api/server.go -- Main API server\n- pkg/model/user.go -- User model"
	prompt := p.buildPlanningPrompt("API Feature", "Add endpoint", ctx, indexHints)

	if !strings.Contains(prompt, "Relevant files from project index") {
		t.Error("Prompt should contain index hints section")
	}
	if !strings.Contains(prompt, "pkg/api/server.go") {
		t.Error("Prompt should contain file paths from hints")
	}
}

func TestTruncateSummary_EdgeCases(t *testing.T) {
	// Note: truncateSummary expects max > len("...") (3), so we test realistic cases
	tests := []struct {
		name     string
		input    string
		max      int
		expected string
	}{
		{"empty string", "", 10, ""},
		{"short string, large max", "abc", 100, "abc"},
		{"exactly max", "test", 4, "test"},
		{"one over max", "test1", 4, "t..."},
		{"long string truncated", "this is a long string", 10, "this is..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateSummary(tc.input, tc.max)
			if got != tc.expected {
				t.Errorf("truncateSummary(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.expected)
			}
		})
	}
}
