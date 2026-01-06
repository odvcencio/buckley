package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFilePlanStore_SaveAndLoad(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	plan := &Plan{
		ID:          "test-plan-save-load",
		FeatureName: "Test Feature",
		Description: "Test description for save/load",
		CreatedAt:   time.Now(),
		Tasks: []Task{
			{
				ID:          "1",
				Title:       "Task 1",
				Description: "First task description",
				Type:        TaskTypeImplementation,
				Status:      TaskPending,
			},
			{
				ID:          "2",
				Title:       "Task 2",
				Description: "Second task description",
				Type:        TaskTypeValidation,
				Status:      TaskPending,
			},
		},
	}

	// Save plan
	err := store.SavePlan(plan)
	if err != nil {
		t.Fatalf("SavePlan() error = %v", err)
	}

	// Verify JSON file exists
	jsonPath := filepath.Join(tempDir, "test-plan-save-load.json")
	if _, err := os.Stat(jsonPath); os.IsNotExist(err) {
		t.Error("JSON plan file was not created")
	}

	// Verify Markdown file exists
	mdPath := filepath.Join(tempDir, "test-plan-save-load.md")
	if _, err := os.Stat(mdPath); os.IsNotExist(err) {
		t.Error("Markdown plan file was not created")
	}

	// Load plan
	loadedPlan, err := store.LoadPlan("test-plan-save-load")
	if err != nil {
		t.Fatalf("LoadPlan() error = %v", err)
	}

	// Verify loaded plan matches original
	if loadedPlan.ID != plan.ID {
		t.Errorf("Loaded plan ID = %s, want %s", loadedPlan.ID, plan.ID)
	}

	if loadedPlan.FeatureName != plan.FeatureName {
		t.Errorf("Loaded plan FeatureName = %s, want %s", loadedPlan.FeatureName, plan.FeatureName)
	}

	if len(loadedPlan.Tasks) != len(plan.Tasks) {
		t.Errorf("Loaded plan has %d tasks, want %d", len(loadedPlan.Tasks), len(plan.Tasks))
	}
}

func TestFilePlanStore_SavePlan_NilPlan(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	err := store.SavePlan(nil)
	if err == nil {
		t.Error("SavePlan(nil) should return error")
	}
}

func TestFilePlanStore_LoadPlan_NotFound(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	_, err := store.LoadPlan("nonexistent-plan")
	if err == nil {
		t.Error("LoadPlan() should return error for nonexistent plan")
	}
}

func TestFilePlanStore_LoadPlan_EmptyID(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	_, err := store.LoadPlan("")
	if err == nil {
		t.Error("LoadPlan(\"\") should return error")
	}

	_, err = store.LoadPlan("  ")
	if err == nil {
		t.Error("LoadPlan(whitespace) should return error")
	}
}

func TestFilePlanStore_ListPlans_Empty(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	plans, err := store.ListPlans()
	if err != nil {
		t.Fatalf("ListPlans() error = %v", err)
	}

	if len(plans) != 0 {
		t.Errorf("Empty directory returned %d plans, want 0", len(plans))
	}
}

func TestFilePlanStore_ListPlans_NonexistentDirectory(t *testing.T) {
	store := NewFilePlanStore("/nonexistent/directory/that/does/not/exist")

	plans, err := store.ListPlans()
	if err != nil {
		t.Fatalf("ListPlans() error = %v", err)
	}

	// Should return empty list, not error
	if len(plans) != 0 {
		t.Errorf("Nonexistent directory returned %d plans, want 0", len(plans))
	}
}

func TestFilePlanStore_ListPlans_Multiple(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	// Save multiple plans
	plans := []*Plan{
		{
			ID:          "plan-alpha",
			FeatureName: "Feature Alpha",
			CreatedAt:   time.Now(),
		},
		{
			ID:          "plan-beta",
			FeatureName: "Feature Beta",
			CreatedAt:   time.Now(),
		},
		{
			ID:          "plan-gamma",
			FeatureName: "Feature Gamma",
			CreatedAt:   time.Now(),
		},
	}

	for _, plan := range plans {
		if err := store.SavePlan(plan); err != nil {
			t.Fatalf("SavePlan(%s) error = %v", plan.ID, err)
		}
	}

	// List all plans
	listedPlans, err := store.ListPlans()
	if err != nil {
		t.Fatalf("ListPlans() error = %v", err)
	}

	if len(listedPlans) != 3 {
		t.Errorf("ListPlans() returned %d plans, want 3", len(listedPlans))
	}

	// Verify all plan IDs are present
	foundIDs := make(map[string]bool)
	for _, p := range listedPlans {
		foundIDs[p.ID] = true
	}

	for _, originalPlan := range plans {
		if !foundIDs[originalPlan.ID] {
			t.Errorf("ListPlans() missing plan ID %s", originalPlan.ID)
		}
	}
}

func TestFilePlanStore_ListPlans_IgnoresNonJSON(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	// Create a valid plan
	validPlan := &Plan{
		ID:          "valid-plan",
		FeatureName: "Valid Feature",
		CreatedAt:   time.Now(),
	}
	if err := store.SavePlan(validPlan); err != nil {
		t.Fatalf("SavePlan() error = %v", err)
	}

	// Create non-JSON files that should be ignored
	os.WriteFile(filepath.Join(tempDir, "readme.txt"), []byte("test"), 0644)
	os.WriteFile(filepath.Join(tempDir, "notes.md"), []byte("# Notes"), 0644)
	os.Mkdir(filepath.Join(tempDir, "subdir"), 0755)

	// List plans
	plans, err := store.ListPlans()
	if err != nil {
		t.Fatalf("ListPlans() error = %v", err)
	}

	// Should only find the valid plan
	if len(plans) != 1 {
		t.Errorf("ListPlans() returned %d plans, want 1", len(plans))
	}

	if plans[0].ID != "valid-plan" {
		t.Errorf("ListPlans()[0].ID = %s, want valid-plan", plans[0].ID)
	}
}

func TestFilePlanStore_ListPlans_IgnoresCorruptedJSON(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	// Create a valid plan
	validPlan := &Plan{
		ID:          "valid-plan",
		FeatureName: "Valid Feature",
		CreatedAt:   time.Now(),
	}
	if err := store.SavePlan(validPlan); err != nil {
		t.Fatalf("SavePlan() error = %v", err)
	}

	// Create a corrupted JSON file
	corruptedPath := filepath.Join(tempDir, "corrupted.json")
	os.WriteFile(corruptedPath, []byte("{invalid json"), 0644)

	// List plans - should skip corrupted file and continue
	plans, err := store.ListPlans()
	if err != nil {
		t.Fatalf("ListPlans() error = %v", err)
	}

	// Should only find the valid plan
	if len(plans) != 1 {
		t.Errorf("ListPlans() returned %d plans, want 1 (corrupted file should be skipped)", len(plans))
	}
}

func TestFilePlanStore_DefaultDirectory(t *testing.T) {
	store := NewFilePlanStore("")

	// Should use default directory
	if store.planDir != filepath.Join("docs", "plans") {
		t.Errorf("Default planDir = %s, want docs/plans", store.planDir)
	}
}

func TestFilePlanStore_UpdatePlan(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	// Save initial plan
	plan := &Plan{
		ID:          "update-test",
		FeatureName: "Original Feature",
		Description: "Original description",
		CreatedAt:   time.Now(),
		Tasks:       []Task{{ID: "1", Title: "Original Task"}},
	}

	if err := store.SavePlan(plan); err != nil {
		t.Fatalf("Initial SavePlan() error = %v", err)
	}

	// Update plan
	plan.FeatureName = "Updated Feature"
	plan.Description = "Updated description"
	plan.Tasks = append(plan.Tasks, Task{ID: "2", Title: "New Task"})

	if err := store.SavePlan(plan); err != nil {
		t.Fatalf("Update SavePlan() error = %v", err)
	}

	// Load and verify updates
	loadedPlan, err := store.LoadPlan("update-test")
	if err != nil {
		t.Fatalf("LoadPlan() error = %v", err)
	}

	if loadedPlan.FeatureName != "Updated Feature" {
		t.Errorf("Updated FeatureName = %s, want 'Updated Feature'", loadedPlan.FeatureName)
	}

	if loadedPlan.Description != "Updated description" {
		t.Errorf("Updated Description = %s, want 'Updated description'", loadedPlan.Description)
	}

	if len(loadedPlan.Tasks) != 2 {
		t.Errorf("Updated plan has %d tasks, want 2", len(loadedPlan.Tasks))
	}
}

func TestFilePlanStore_ReadLog_PlanNotFound(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	_, _, err := store.ReadLog("nonexistent-plan", "builder", 10)
	if err == nil {
		t.Error("ReadLog() should return error for nonexistent plan")
	}
}

func TestFilePlanStore_ReadLog_UnknownLogKind(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	// Create a plan
	plan := &Plan{
		ID:          "log-test",
		FeatureName: "Test Feature",
		CreatedAt:   time.Now(),
	}

	if err := store.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan() error = %v", err)
	}

	_, _, err := store.ReadLog("log-test", "unknown-kind", 10)
	if err == nil {
		t.Error("ReadLog() should return error for unknown log kind")
	}
}

func TestFilePlanStore_ReadLog_EmptyLog(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	// Create a plan
	plan := &Plan{
		ID:          "empty-log-test",
		FeatureName: "Test Feature",
		CreatedAt:   time.Now(),
	}

	if err := store.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan() error = %v", err)
	}

	// Try to read log (file doesn't exist yet)
	entries, logPath, err := store.ReadLog("empty-log-test", "builder", 10)
	if err != nil {
		t.Fatalf("ReadLog() error = %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("ReadLog() returned %d entries for nonexistent log, want 0", len(entries))
	}

	if logPath == "" {
		t.Error("ReadLog() should return log path even for nonexistent log")
	}
}

// Table-driven tests for FilePlanStore

func TestFilePlanStore_SavePlan_TableDriven(t *testing.T) {
	tests := []struct {
		name      string
		plan      *Plan
		wantErr   bool
		errSubstr string
	}{
		{
			name:      "nil plan returns error",
			plan:      nil,
			wantErr:   true,
			errSubstr: "plan is nil",
		},
		{
			name: "valid plan with empty ID",
			plan: &Plan{
				ID:          "",
				FeatureName: "Feature",
				CreatedAt:   time.Now(),
			},
			wantErr: false,
		},
		{
			name: "valid plan with special characters in ID",
			plan: &Plan{
				ID:          "plan-with-dashes_and_underscores",
				FeatureName: "Feature",
				CreatedAt:   time.Now(),
			},
			wantErr: false,
		},
		{
			name: "valid plan with unicode in feature name",
			plan: &Plan{
				ID:          "unicode-test",
				FeatureName: "Feature with unicode \u00e9\u00e8\u00ea",
				Description: "Description with \u2605 stars",
				CreatedAt:   time.Now(),
			},
			wantErr: false,
		},
		{
			name: "valid plan with many tasks",
			plan: &Plan{
				ID:          "many-tasks",
				FeatureName: "Large Plan",
				CreatedAt:   time.Now(),
				Tasks: []Task{
					{ID: "1", Title: "Task 1", Status: TaskPending},
					{ID: "2", Title: "Task 2", Status: TaskInProgress},
					{ID: "3", Title: "Task 3", Status: TaskCompleted},
					{ID: "4", Title: "Task 4", Status: TaskFailed},
					{ID: "5", Title: "Task 5", Status: TaskSkipped},
				},
			},
			wantErr: false,
		},
		{
			name: "valid plan with all task types",
			plan: &Plan{
				ID:          "all-task-types",
				FeatureName: "Task Types Test",
				CreatedAt:   time.Now(),
				Tasks: []Task{
					{ID: "1", Title: "Impl Task", Type: TaskTypeImplementation},
					{ID: "2", Title: "Analysis Task", Type: TaskTypeAnalysis},
					{ID: "3", Title: "Validation Task", Type: TaskTypeValidation},
				},
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store := NewFilePlanStore(tempDir)

			err := store.SavePlan(tc.plan)

			if tc.wantErr {
				if err == nil {
					t.Errorf("SavePlan() expected error containing %q, got nil", tc.errSubstr)
				} else if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("SavePlan() error = %v, want error containing %q", err, tc.errSubstr)
				}
			} else {
				if err != nil {
					t.Errorf("SavePlan() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestFilePlanStore_LoadPlan_TableDriven(t *testing.T) {
	tests := []struct {
		name       string
		planID     string
		setupPlan  *Plan
		wantErr    bool
		errSubstr  string
	}{
		{
			name:      "empty plan ID",
			planID:    "",
			wantErr:   true,
			errSubstr: "plan id required",
		},
		{
			name:      "whitespace only plan ID",
			planID:    "   ",
			wantErr:   true,
			errSubstr: "plan id required",
		},
		{
			name:    "nonexistent plan",
			planID:  "does-not-exist",
			wantErr: true,
		},
		{
			name:   "existing plan",
			planID: "existing-plan",
			setupPlan: &Plan{
				ID:          "existing-plan",
				FeatureName: "Test Feature",
				Description: "Test Description",
				CreatedAt:   time.Now(),
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store := NewFilePlanStore(tempDir)

			// Setup: create the plan if specified
			if tc.setupPlan != nil {
				if err := store.SavePlan(tc.setupPlan); err != nil {
					t.Fatalf("Setup SavePlan() error = %v", err)
				}
			}

			plan, err := store.LoadPlan(tc.planID)

			if tc.wantErr {
				if err == nil {
					t.Errorf("LoadPlan() expected error, got nil")
				} else if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("LoadPlan() error = %v, want error containing %q", err, tc.errSubstr)
				}
				if plan != nil {
					t.Error("LoadPlan() should return nil plan on error")
				}
			} else {
				if err != nil {
					t.Errorf("LoadPlan() unexpected error: %v", err)
				}
				if plan == nil {
					t.Error("LoadPlan() returned nil plan")
				} else if plan.ID != tc.planID {
					t.Errorf("LoadPlan() ID = %s, want %s", plan.ID, tc.planID)
				}
			}
		})
	}
}

func TestFilePlanStore_ReadLog_TableDriven(t *testing.T) {
	tests := []struct {
		name      string
		logKind   string
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "builder log kind",
			logKind: "builder",
			wantErr: false,
		},
		{
			name:    "review log kind",
			logKind: "review",
			wantErr: false,
		},
		{
			name:    "research log kind",
			logKind: "research",
			wantErr: false,
		},
		{
			name:      "unknown log kind",
			logKind:   "unknown",
			wantErr:   true,
			errSubstr: "unknown log kind",
		},
		{
			name:      "empty log kind",
			logKind:   "",
			wantErr:   true,
			errSubstr: "unknown log kind",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store := NewFilePlanStore(tempDir)

			// Create a plan first
			plan := &Plan{
				ID:          "log-test-plan",
				FeatureName: "Test Feature",
				CreatedAt:   time.Now(),
			}
			if err := store.SavePlan(plan); err != nil {
				t.Fatalf("SavePlan() error = %v", err)
			}

			_, _, err := store.ReadLog("log-test-plan", tc.logKind, 10)

			if tc.wantErr {
				if err == nil {
					t.Errorf("ReadLog() expected error, got nil")
				} else if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("ReadLog() error = %v, want error containing %q", err, tc.errSubstr)
				}
			} else {
				if err != nil {
					t.Errorf("ReadLog() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestFilePlanStore_ReadLog_WithContent(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	// Create a plan
	plan := &Plan{
		ID:          "log-content-test",
		FeatureName: "Test Feature",
		CreatedAt:   time.Now(),
	}
	if err := store.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan() error = %v", err)
	}

	// Get the log path and create the log file with content
	_, logPath, err := store.ReadLog("log-content-test", "builder", 10)
	if err != nil {
		t.Fatalf("ReadLog() error = %v", err)
	}

	// Create log directory and write content
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	logContent := "line1\nline2\nline3\nline4\nline5"
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Test reading all lines
	entries, _, err := store.ReadLog("log-content-test", "builder", 10)
	if err != nil {
		t.Fatalf("ReadLog() error = %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("ReadLog(limit=10) returned %d entries, want 5", len(entries))
	}

	// Test reading with limit
	entries, _, err = store.ReadLog("log-content-test", "builder", 3)
	if err != nil {
		t.Fatalf("ReadLog(limit=3) error = %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("ReadLog(limit=3) returned %d entries, want 3", len(entries))
	}
	// Should return the last 3 lines
	if entries[0] != "line3" {
		t.Errorf("ReadLog(limit=3)[0] = %s, want line3", entries[0])
	}
}

func TestFilePlanStore_ListPlans_WithMixedFiles(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	// Save a valid plan
	validPlan := &Plan{
		ID:          "valid-plan",
		FeatureName: "Valid Feature",
		CreatedAt:   time.Now(),
	}
	if err := store.SavePlan(validPlan); err != nil {
		t.Fatalf("SavePlan() error = %v", err)
	}

	// Create various non-plan files (not valid JSON plans)
	testFiles := []struct {
		name    string
		content string
	}{
		{"readme.md", "# Readme"},
		{"notes.txt", "Some notes"},
		{"config.yaml", "key: value"},
		{"backup.json.bak", `{"backup": true}`}, // .bak extension, not .json
	}

	for _, f := range testFiles {
		if err := os.WriteFile(filepath.Join(tempDir, f.name), []byte(f.content), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", f.name, err)
		}
	}

	// Create a subdirectory (should be ignored)
	if err := os.Mkdir(filepath.Join(tempDir, "subdir"), 0o755); err != nil {
		t.Fatalf("Mkdir(subdir) error = %v", err)
	}

	// List plans
	plans, err := store.ListPlans()
	if err != nil {
		t.Fatalf("ListPlans() error = %v", err)
	}

	// Check that valid-plan is present
	foundValidPlan := false
	for _, p := range plans {
		if p.ID == "valid-plan" {
			foundValidPlan = true
			break
		}
	}
	if !foundValidPlan {
		t.Error("ListPlans() should include valid-plan")
	}
}

func TestFilePlanStore_SaveAndLoadPreservesAllFields(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	createdAt := time.Now().Truncate(time.Millisecond) // Truncate for comparison
	plan := &Plan{
		ID:          "full-plan",
		FeatureName: "Full Feature",
		Description: "A complete description with special chars: <>&\"'",
		CreatedAt:   createdAt,
		Context: PlanContext{
			ProjectType:     "go",
			Dependencies:    []string{"dep1", "dep2", "dep3"},
			RepoRoot:        "/path/to/repo",
			GitBranch:       "feature/test",
			GitRemoteURL:    "https://github.com/org/repo.git",
			Architecture:    "Clean Architecture",
			ResearchSummary: "Summary of research",
			ResearchRisks:   []string{"Risk 1", "Risk 2"},
		},
		Tasks: []Task{
			{
				ID:            "task-1",
				Title:         "First Task",
				Description:   "Detailed description",
				Type:          TaskTypeImplementation,
				Files:         []string{"file1.go", "file2.go"},
				Dependencies:  []string{},
				EstimatedTime: "2h",
				Verification:  []string{"Run tests", "Check coverage"},
				Status:        TaskPending,
			},
			{
				ID:            "task-2",
				Title:         "Second Task",
				Description:   "Another description",
				Type:          TaskTypeValidation,
				Files:         []string{},
				Dependencies:  []string{"task-1"},
				EstimatedTime: "30m",
				Verification:  []string{"Verify output"},
				Status:        TaskCompleted,
			},
		},
	}

	// Save
	if err := store.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan() error = %v", err)
	}

	// Load
	loaded, err := store.LoadPlan(plan.ID)
	if err != nil {
		t.Fatalf("LoadPlan() error = %v", err)
	}

	// Verify all fields
	if loaded.ID != plan.ID {
		t.Errorf("ID = %s, want %s", loaded.ID, plan.ID)
	}
	if loaded.FeatureName != plan.FeatureName {
		t.Errorf("FeatureName = %s, want %s", loaded.FeatureName, plan.FeatureName)
	}
	if loaded.Description != plan.Description {
		t.Errorf("Description = %s, want %s", loaded.Description, plan.Description)
	}
	if loaded.Context.ProjectType != plan.Context.ProjectType {
		t.Errorf("Context.ProjectType = %s, want %s", loaded.Context.ProjectType, plan.Context.ProjectType)
	}
	if len(loaded.Context.Dependencies) != len(plan.Context.Dependencies) {
		t.Errorf("Context.Dependencies length = %d, want %d", len(loaded.Context.Dependencies), len(plan.Context.Dependencies))
	}
	if loaded.Context.GitBranch != plan.Context.GitBranch {
		t.Errorf("Context.GitBranch = %s, want %s", loaded.Context.GitBranch, plan.Context.GitBranch)
	}
	if len(loaded.Tasks) != len(plan.Tasks) {
		t.Errorf("Tasks length = %d, want %d", len(loaded.Tasks), len(plan.Tasks))
	}
	if len(loaded.Tasks) > 0 {
		if loaded.Tasks[0].Type != plan.Tasks[0].Type {
			t.Errorf("Tasks[0].Type = %s, want %s", loaded.Tasks[0].Type, plan.Tasks[0].Type)
		}
		if loaded.Tasks[0].Status != plan.Tasks[0].Status {
			t.Errorf("Tasks[0].Status = %d, want %d", loaded.Tasks[0].Status, plan.Tasks[0].Status)
		}
	}
}

func TestFilePlanStore_ConcurrentAccess(t *testing.T) {
	tempDir := t.TempDir()
	store := NewFilePlanStore(tempDir)

	// Create initial plan
	plan := &Plan{
		ID:          "concurrent-plan",
		FeatureName: "Concurrent Feature",
		CreatedAt:   time.Now(),
		Tasks: []Task{
			{ID: "1", Title: "Task 1", Status: TaskPending},
		},
	}
	if err := store.SavePlan(plan); err != nil {
		t.Fatalf("SavePlan() error = %v", err)
	}

	// Concurrent reads should not fail
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			_, err := store.LoadPlan("concurrent-plan")
			if err != nil {
				t.Errorf("Concurrent LoadPlan() error = %v", err)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestReadLogTail(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		limit    int
		wantLen  int
		wantLast string
	}{
		{
			name:     "empty file",
			content:  "",
			limit:    10,
			wantLen:  0,
			wantLast: "",
		},
		{
			name:     "whitespace only",
			content:  "   \n  \n  ",
			limit:    10,
			wantLen:  0,
			wantLast: "",
		},
		{
			name:     "single line",
			content:  "line1",
			limit:    10,
			wantLen:  1,
			wantLast: "line1",
		},
		{
			name:     "multiple lines no limit",
			content:  "line1\nline2\nline3",
			limit:    0,
			wantLen:  3,
			wantLast: "line3",
		},
		{
			name:     "multiple lines with limit",
			content:  "line1\nline2\nline3\nline4\nline5",
			limit:    2,
			wantLen:  2,
			wantLast: "line5",
		},
		{
			name:     "limit larger than content",
			content:  "line1\nline2",
			limit:    100,
			wantLen:  2,
			wantLast: "line2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			path := filepath.Join(tempDir, "test.log")

			if tc.content != "" {
				if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			}

			entries, err := readLogTail(path, tc.limit)
			if err != nil {
				t.Fatalf("readLogTail() error = %v", err)
			}

			if len(entries) != tc.wantLen {
				t.Errorf("readLogTail() returned %d entries, want %d", len(entries), tc.wantLen)
			}
			if tc.wantLen > 0 && entries[len(entries)-1] != tc.wantLast {
				t.Errorf("readLogTail() last entry = %s, want %s", entries[len(entries)-1], tc.wantLast)
			}
		})
	}
}

func TestReadLogTail_EmptyPath(t *testing.T) {
	entries, err := readLogTail("", 10)
	if err != nil {
		t.Errorf("readLogTail(\"\") error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("readLogTail(\"\") returned %d entries, want 0", len(entries))
	}

	entries, err = readLogTail("   ", 10)
	if err != nil {
		t.Errorf("readLogTail(whitespace) error = %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("readLogTail(whitespace) returned %d entries, want 0", len(entries))
	}
}

func TestAssignPlanLogs(t *testing.T) {
	// Test nil plan
	assignPlanLogs(nil) // Should not panic

	// Test with valid plan
	plan := &Plan{
		ID:          "test-logs",
		FeatureName: "Test Feature",
	}
	assignPlanLogs(plan)

	if plan.Logs.BaseDir == "" {
		t.Error("Logs.BaseDir should be set")
	}
	if plan.Logs.BuilderLog == "" {
		t.Error("Logs.BuilderLog should be set")
	}
	if plan.Logs.ReviewLog == "" {
		t.Error("Logs.ReviewLog should be set")
	}
	if plan.Logs.ResearchLog == "" {
		t.Error("Logs.ResearchLog should be set")
	}
	if plan.Logs.UpdatedAt.IsZero() {
		t.Error("Logs.UpdatedAt should be set")
	}
}

func TestLogsDirectoryForPlan(t *testing.T) {
	tests := []struct {
		name        string
		plan        *Plan
		expectEmpty bool
	}{
		{
			name: "plan with ID",
			plan: &Plan{
				ID:          "my-plan-id",
				FeatureName: "Feature",
			},
			expectEmpty: false,
		},
		{
			name: "plan with empty ID but feature name",
			plan: &Plan{
				ID:          "",
				FeatureName: "My Feature",
			},
			expectEmpty: false,
		},
		{
			name: "plan with empty ID and feature name",
			plan: &Plan{
				ID:          "",
				FeatureName: "",
			},
			expectEmpty: false, // Should use "default"
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := logsDirectoryForPlan(tc.plan)
			if tc.expectEmpty && dir != "" {
				t.Errorf("logsDirectoryForPlan() = %s, want empty", dir)
			}
			if !tc.expectEmpty && dir == "" {
				t.Error("logsDirectoryForPlan() returned empty string")
			}
		})
	}
}

func TestRenderPlanTemplate(t *testing.T) {
	plan := &Plan{
		ID:          "render-test",
		FeatureName: "Render Feature",
		Description: "Test description",
		Tasks: []Task{
			{ID: "1", Title: "Pending Task", Status: TaskPending},
			{ID: "2", Title: "Completed Task", Status: TaskCompleted},
			{ID: "3", Title: "Another Completed", Status: TaskCompleted},
		},
	}

	content, err := renderPlanTemplate(plan)
	if err != nil {
		t.Fatalf("renderPlanTemplate() error = %v", err)
	}

	if content == "" {
		t.Error("renderPlanTemplate() returned empty content")
	}

	// Check that the content contains expected elements
	if !strings.Contains(content, "Render Feature") {
		t.Error("Template should contain feature name")
	}
}

func TestResolvePlanLogPath(t *testing.T) {
	tests := []struct {
		name      string
		plan      *Plan
		kind      string
		wantErr   bool
		errSubstr string
	}{
		{
			name:      "nil plan",
			plan:      nil,
			kind:      "builder",
			wantErr:   true,
			errSubstr: "plan not found",
		},
		{
			name: "unknown kind",
			plan: &Plan{
				ID: "test",
				Logs: PlanLogs{
					BuilderLog: "/path/to/builder.log",
				},
			},
			kind:      "invalid",
			wantErr:   true,
			errSubstr: "unknown log kind",
		},
		{
			name: "builder kind with path",
			plan: &Plan{
				ID: "test",
				Logs: PlanLogs{
					BuilderLog: "/path/to/builder.log",
				},
			},
			kind:    "builder",
			wantErr: false,
		},
		{
			name: "review kind with path",
			plan: &Plan{
				ID: "test",
				Logs: PlanLogs{
					ReviewLog: "/path/to/review.log",
				},
			},
			kind:    "review",
			wantErr: false,
		},
		{
			name: "research kind with path",
			plan: &Plan{
				ID: "test",
				Logs: PlanLogs{
					ResearchLog: "/path/to/research.log",
				},
			},
			kind:    "research",
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, err := resolvePlanLogPath(tc.plan, tc.kind)

			if tc.wantErr {
				if err == nil {
					t.Errorf("resolvePlanLogPath() expected error, got nil")
				} else if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("resolvePlanLogPath() error = %v, want error containing %q", err, tc.errSubstr)
				}
			} else {
				if err != nil {
					t.Errorf("resolvePlanLogPath() unexpected error: %v", err)
				}
				if path == "" {
					t.Error("resolvePlanLogPath() returned empty path")
				}
			}
		})
	}
}
