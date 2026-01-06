package parallel

import (
	"context"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/worktree"
)

// ==== Scope Validator Tests ====

func TestNewScopeValidator(t *testing.T) {
	v := NewScopeValidator()
	if v == nil {
		t.Fatal("NewScopeValidator() returned nil")
	}
}

func TestScopeValidator_ExtractScope_Nil(t *testing.T) {
	v := NewScopeValidator()
	scope := v.ExtractScope(nil)
	if scope != nil {
		t.Error("ExtractScope(nil) should return nil")
	}
}

func TestScopeValidator_ExtractScope_Files(t *testing.T) {
	v := NewScopeValidator()
	task := &AgentTask{
		ID: "task-1",
		Context: map[string]string{
			"files": "pkg/auth/login.go, pkg/auth/logout.go",
		},
	}

	scope := v.ExtractScope(task)
	if scope == nil {
		t.Fatal("ExtractScope() returned nil")
	}
	if scope.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", scope.TaskID, "task-1")
	}
	if len(scope.Files) != 2 {
		t.Errorf("Files count = %d, want 2", len(scope.Files))
	}
}

func TestScopeValidator_ExtractScope_Globs(t *testing.T) {
	v := NewScopeValidator()
	task := &AgentTask{
		ID: "task-1",
		Context: map[string]string{
			"scope": "pkg/auth/..., pkg/model/*.go",
		},
	}

	scope := v.ExtractScope(task)
	if scope == nil {
		t.Fatal("ExtractScope() returned nil")
	}
	if len(scope.Globs) != 2 {
		t.Errorf("Globs count = %d, want 2", len(scope.Globs))
	}
}

func TestScopeValidator_CheckConflicts_NoConflicts(t *testing.T) {
	v := NewScopeValidator()
	scopes := []*TaskScope{
		{TaskID: "task-1", Files: []string{"pkg/auth/login.go"}},
		{TaskID: "task-2", Files: []string{"pkg/model/user.go"}},
	}

	conflicts := v.CheckConflicts(scopes)
	if len(conflicts) != 0 {
		t.Errorf("Got %d conflicts, want 0", len(conflicts))
	}
}

func TestScopeValidator_CheckConflicts_FileOverlap(t *testing.T) {
	v := NewScopeValidator()
	scopes := []*TaskScope{
		{TaskID: "task-1", Files: []string{"pkg/auth/login.go", "pkg/auth/shared.go"}},
		{TaskID: "task-2", Files: []string{"pkg/auth/shared.go", "pkg/auth/logout.go"}},
	}

	conflicts := v.CheckConflicts(scopes)
	if len(conflicts) != 1 {
		t.Errorf("Got %d conflicts, want 1", len(conflicts))
	}
	if len(conflicts) > 0 {
		if conflicts[0].TaskA != "task-1" || conflicts[0].TaskB != "task-2" {
			t.Error("Wrong task IDs in conflict")
		}
		if len(conflicts[0].OverlapFiles) != 1 {
			t.Errorf("OverlapFiles = %v, want 1 file", conflicts[0].OverlapFiles)
		}
	}
}

func TestScopeValidator_CheckConflicts_GlobOverlap(t *testing.T) {
	v := NewScopeValidator()
	scopes := []*TaskScope{
		{TaskID: "task-1", Globs: []string{"pkg/auth/..."}},
		{TaskID: "task-2", Globs: []string{"pkg/auth/login/..."}},
	}

	conflicts := v.CheckConflicts(scopes)
	if len(conflicts) != 1 {
		t.Errorf("Got %d conflicts, want 1", len(conflicts))
	}
}

func TestScopeValidator_CheckConflicts_GlobToFileOverlap(t *testing.T) {
	v := NewScopeValidator()
	scopes := []*TaskScope{
		{TaskID: "task-1", Globs: []string{"pkg/auth/..."}},
		{TaskID: "task-2", Files: []string{"pkg/auth/login.go"}},
	}

	conflicts := v.CheckConflicts(scopes)
	if len(conflicts) != 1 {
		t.Errorf("Got %d conflicts, want 1", len(conflicts))
	}
}

func TestScopeValidator_PartitionTasks_NoConflicts(t *testing.T) {
	v := NewScopeValidator()
	scopes := []*TaskScope{
		{TaskID: "task-1", Files: []string{"pkg/auth/login.go"}},
		{TaskID: "task-2", Files: []string{"pkg/model/user.go"}},
		{TaskID: "task-3", Files: []string{"pkg/config/config.go"}},
	}

	partitions := v.PartitionTasks(scopes)
	if len(partitions) != 1 {
		t.Errorf("Got %d partitions, want 1 (all can run in parallel)", len(partitions))
	}
	if len(partitions) > 0 && len(partitions[0].TaskIDs) != 3 {
		t.Errorf("First partition has %d tasks, want 3", len(partitions[0].TaskIDs))
	}
}

func TestScopeValidator_PartitionTasks_WithConflicts(t *testing.T) {
	v := NewScopeValidator()
	scopes := []*TaskScope{
		{TaskID: "task-1", Files: []string{"pkg/shared.go"}},
		{TaskID: "task-2", Files: []string{"pkg/shared.go"}},
		{TaskID: "task-3", Files: []string{"pkg/other.go"}},
	}

	partitions := v.PartitionTasks(scopes)
	if len(partitions) < 2 {
		t.Errorf("Got %d partitions, want >= 2 (conflicting tasks need waves)", len(partitions))
	}
}

func TestScopeValidator_PartitionTasks_Empty(t *testing.T) {
	v := NewScopeValidator()
	partitions := v.PartitionTasks(nil)
	if partitions != nil {
		t.Error("PartitionTasks(nil) should return nil")
	}
}

func TestScopeValidator_HasConflicts(t *testing.T) {
	v := NewScopeValidator()

	noConflict := []*TaskScope{
		{TaskID: "task-1", Files: []string{"a.go"}},
		{TaskID: "task-2", Files: []string{"b.go"}},
	}
	if v.HasConflicts(noConflict) {
		t.Error("HasConflicts() should be false for non-overlapping scopes")
	}

	conflict := []*TaskScope{
		{TaskID: "task-1", Files: []string{"a.go"}},
		{TaskID: "task-2", Files: []string{"a.go"}},
	}
	if !v.HasConflicts(conflict) {
		t.Error("HasConflicts() should be true for overlapping scopes")
	}
}

func TestScopeValidator_ConflictReport(t *testing.T) {
	v := NewScopeValidator()

	// No conflicts
	report := v.ConflictReport(nil)
	if report == "" {
		t.Error("ConflictReport(nil) should return a message")
	}

	// With conflicts
	conflicts := []Conflict{
		{
			TaskA:        "task-1",
			TaskB:        "task-2",
			OverlapFiles: []string{"pkg/shared.go"},
		},
	}
	report = v.ConflictReport(conflicts)
	if !containsString(report, "task-1") || !containsString(report, "task-2") {
		t.Error("ConflictReport should mention conflicting tasks")
	}
}

func TestScopeValidator_PartitionReport(t *testing.T) {
	v := NewScopeValidator()

	// Empty
	report := v.PartitionReport(nil)
	if report == "" {
		t.Error("PartitionReport(nil) should return a message")
	}

	// With partitions
	partitions := []TaskPartition{
		{Group: 0, TaskIDs: []string{"task-1", "task-2"}},
		{Group: 1, TaskIDs: []string{"task-3"}, WaitFor: []string{"task-1", "task-2"}},
	}
	report = v.PartitionReport(partitions)
	if !containsString(report, "Wave") {
		t.Error("PartitionReport should mention waves")
	}
}

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		file    string
		want    bool
	}{
		{"pkg/auth/...", "pkg/auth/login.go", true},
		{"pkg/auth/...", "pkg/auth/nested/deep.go", true},
		{"pkg/auth/...", "pkg/model/user.go", false},
		{"pkg/*.go", "pkg/main.go", true},
		{"pkg/*.go", "pkg/sub/main.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.file, func(t *testing.T) {
			got := matchGlob(tt.pattern, tt.file)
			if got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.file, got, tt.want)
			}
		})
	}
}

func TestGlobsOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"pkg/auth/...", "pkg/auth/login/...", true},
		{"pkg/auth/...", "pkg/model/...", false},
		{"pkg/...", "pkg/auth/...", true},
	}

	for _, tt := range tests {
		t.Run(tt.a+"_"+tt.b, func(t *testing.T) {
			got := globsOverlap(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("globsOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// ==== File Lock Manager Tests ====

func TestNewFileLockManager(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)
	if m == nil {
		t.Fatal("NewFileLockManager() returned nil")
	}
}

func TestFileLockManager_Acquire_Basic(t *testing.T) {
	cfg := DefaultFileLockConfig()
	cfg.CleanupPeriod = 1 * time.Hour // Don't interfere with test
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	result, err := m.Acquire(ctx, "agent-1", "task-1", "file.go", 1*time.Minute)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if !result.Acquired {
		t.Error("Acquire() should succeed for unlocked file")
	}
	if result.Lock == nil {
		t.Error("Lock should not be nil")
	}
}

func TestFileLockManager_Acquire_EmptyPath(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	_, err := m.Acquire(ctx, "agent-1", "task-1", "", 1*time.Minute)
	if err == nil {
		t.Error("Acquire() with empty path should fail")
	}
}

func TestFileLockManager_Acquire_EmptyAgentID(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	_, err := m.Acquire(ctx, "", "task-1", "file.go", 1*time.Minute)
	if err == nil {
		t.Error("Acquire() with empty agentID should fail")
	}
}

func TestFileLockManager_Acquire_SameAgentExtends(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	result1, _ := m.Acquire(ctx, "agent-1", "task-1", "file.go", 1*time.Minute)
	result2, _ := m.Acquire(ctx, "agent-1", "task-1", "file.go", 2*time.Minute)

	if !result1.Acquired || !result2.Acquired {
		t.Error("Same agent should be able to extend lock")
	}
}

func TestFileLockManager_Release(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	m.Acquire(ctx, "agent-1", "task-1", "file.go", 1*time.Minute)

	err := m.Release("agent-1", "file.go")
	if err != nil {
		t.Errorf("Release() error = %v", err)
	}

	// Should be able to acquire again
	result, _ := m.Acquire(ctx, "agent-2", "task-2", "file.go", 1*time.Minute)
	if !result.Acquired {
		t.Error("Should be able to acquire after release")
	}
}

func TestFileLockManager_Release_WrongAgent(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	m.Acquire(ctx, "agent-1", "task-1", "file.go", 1*time.Minute)

	err := m.Release("agent-2", "file.go")
	if err == nil {
		t.Error("Release() by wrong agent should fail")
	}
}

func TestFileLockManager_ReleaseAll(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	m.Acquire(ctx, "agent-1", "task-1", "file1.go", 1*time.Minute)
	m.Acquire(ctx, "agent-1", "task-1", "file2.go", 1*time.Minute)
	m.Acquire(ctx, "agent-2", "task-2", "file3.go", 1*time.Minute)

	released := m.ReleaseAll("agent-1")
	if released != 2 {
		t.Errorf("ReleaseAll() released %d, want 2", released)
	}
}

func TestFileLockManager_IsLocked(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	if m.IsLocked("file.go") {
		t.Error("IsLocked() should be false for unlocked file")
	}

	ctx := context.Background()
	m.Acquire(ctx, "agent-1", "task-1", "file.go", 1*time.Minute)

	if !m.IsLocked("file.go") {
		t.Error("IsLocked() should be true for locked file")
	}
}

func TestFileLockManager_GetLock(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	lock := m.GetLock("file.go")
	if lock != nil {
		t.Error("GetLock() should return nil for unlocked file")
	}

	ctx := context.Background()
	m.Acquire(ctx, "agent-1", "task-1", "file.go", 1*time.Minute)

	lock = m.GetLock("file.go")
	if lock == nil {
		t.Error("GetLock() should return lock for locked file")
	}
	if lock != nil && lock.AgentID != "agent-1" {
		t.Errorf("Lock.AgentID = %q, want %q", lock.AgentID, "agent-1")
	}
}

func TestFileLockManager_ListLocks(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	m.Acquire(ctx, "agent-1", "task-1", "file1.go", 1*time.Minute)
	m.Acquire(ctx, "agent-2", "task-2", "file2.go", 1*time.Minute)

	locks := m.ListLocks()
	if len(locks) != 2 {
		t.Errorf("ListLocks() returned %d locks, want 2", len(locks))
	}
}

func TestFileLockManager_ListLocksForAgent(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	m.Acquire(ctx, "agent-1", "task-1", "file1.go", 1*time.Minute)
	m.Acquire(ctx, "agent-1", "task-1", "file2.go", 1*time.Minute)
	m.Acquire(ctx, "agent-2", "task-2", "file3.go", 1*time.Minute)

	locks := m.ListLocksForAgent("agent-1")
	if len(locks) != 2 {
		t.Errorf("ListLocksForAgent() returned %d locks, want 2", len(locks))
	}
}

func TestFileLockManager_Heartbeat(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	m.Acquire(ctx, "agent-1", "task-1", "file.go", 1*time.Minute)

	err := m.Heartbeat("agent-1", "file.go", 2*time.Minute)
	if err != nil {
		t.Errorf("Heartbeat() error = %v", err)
	}
}

func TestFileLockManager_Heartbeat_NoLock(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	err := m.Heartbeat("agent-1", "file.go", 1*time.Minute)
	if err == nil {
		t.Error("Heartbeat() on unlocked file should fail")
	}
}

func TestFileLockManager_Stats(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	m.Acquire(ctx, "agent-1", "task-1", "file.go", 1*time.Minute)

	stats := m.Stats()
	if stats.ActiveLocks != 1 {
		t.Errorf("ActiveLocks = %d, want 1", stats.ActiveLocks)
	}
}

func TestFileLockManager_AcquireMultiple(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	paths := []string{"file1.go", "file2.go", "file3.go"}

	result, err := m.AcquireMultiple(ctx, "agent-1", "task-1", paths, 1*time.Minute)
	if err != nil {
		t.Fatalf("AcquireMultiple() error = %v", err)
	}
	if !result.Acquired {
		t.Error("AcquireMultiple() should succeed for all unlocked files")
	}
}

func TestFileLockManager_AcquireMultiple_Empty(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	result, err := m.AcquireMultiple(ctx, "agent-1", "task-1", nil, 1*time.Minute)
	if err != nil {
		t.Fatalf("AcquireMultiple() error = %v", err)
	}
	if !result.Acquired {
		t.Error("AcquireMultiple(nil) should succeed")
	}
}

func TestFileLockManager_ReleaseMultiple(t *testing.T) {
	cfg := DefaultFileLockConfig()
	m := NewFileLockManager(cfg)

	ctx := context.Background()
	paths := []string{"file1.go", "file2.go"}
	m.AcquireMultiple(ctx, "agent-1", "task-1", paths, 1*time.Minute)

	m.ReleaseMultiple("agent-1", paths)

	for _, path := range paths {
		if m.IsLocked(path) {
			t.Errorf("File %s should be unlocked after ReleaseMultiple", path)
		}
	}
}

// ==== Merge Orchestrator Tests ====

func TestNewMergeOrchestrator(t *testing.T) {
	m := NewMergeOrchestrator("/tmp/repo")
	if m == nil {
		t.Fatal("NewMergeOrchestrator() returned nil")
	}
}

func TestDefaultMergeConfig(t *testing.T) {
	cfg := DefaultMergeConfig()
	if cfg.TargetBranch != "main" {
		t.Errorf("TargetBranch = %q, want %q", cfg.TargetBranch, "main")
	}
	if cfg.Strategy != MergeStrategyPause {
		t.Errorf("Strategy = %v, want MergeStrategyPause", cfg.Strategy)
	}
}

func TestMergeReport_Markdown(t *testing.T) {
	report := &MergeReport{
		TargetBranch: "main",
		TotalTasks:   3,
		Merged:       2,
		Conflicts:    1,
		Duration:     1 * time.Second,
	}

	md := report.Markdown()
	if !containsString(md, "main") {
		t.Error("Markdown should contain target branch")
	}
	if !containsString(md, "Merged") {
		t.Error("Markdown should contain merged count")
	}
}

func TestExtractConflictFiles(t *testing.T) {
	output := `CONFLICT (content): Merge conflict in pkg/auth/login.go
CONFLICT (content): Merge conflict in pkg/model/user.go
Automatic merge failed`

	files := extractConflictFiles(output)
	if len(files) != 2 {
		t.Errorf("extractConflictFiles() returned %d files, want 2", len(files))
	}
}

// ==== Coordinator Tests ====

func TestNewCoordinator(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()
	cfg := DefaultCoordinatorConfig("/tmp/repo")

	c := NewCoordinator(mock, executor, cfg)
	if c == nil {
		t.Fatal("NewCoordinator() returned nil")
	}
}

func TestDefaultCoordinatorConfig(t *testing.T) {
	cfg := DefaultCoordinatorConfig("/tmp/repo")
	if cfg.RepoPath != "/tmp/repo" {
		t.Errorf("RepoPath = %q, want %q", cfg.RepoPath, "/tmp/repo")
	}
	if cfg.MaxAgents != 4 {
		t.Errorf("MaxAgents = %d, want 4", cfg.MaxAgents)
	}
}

func TestCoordinator_PreviewExecution(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()
	cfg := DefaultCoordinatorConfig("/tmp/repo")
	c := NewCoordinator(mock, executor, cfg)

	tasks := []*AgentTask{
		{ID: "task-1", Context: map[string]string{"files": "a.go"}},
		{ID: "task-2", Context: map[string]string{"files": "b.go"}},
	}

	preview := c.PreviewExecution(tasks)
	if preview == nil {
		t.Fatal("PreviewExecution() returned nil")
	}
	if preview.TotalTasks != 2 {
		t.Errorf("TotalTasks = %d, want 2", preview.TotalTasks)
	}
	if !preview.CanParallel {
		t.Error("CanParallel should be true for non-overlapping tasks")
	}
}

func TestCoordinator_PreviewExecution_WithConflicts(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()
	cfg := DefaultCoordinatorConfig("/tmp/repo")
	c := NewCoordinator(mock, executor, cfg)

	tasks := []*AgentTask{
		{ID: "task-1", Context: map[string]string{"files": "shared.go"}},
		{ID: "task-2", Context: map[string]string{"files": "shared.go"}},
	}

	preview := c.PreviewExecution(tasks)
	if preview.CanParallel {
		t.Error("CanParallel should be false for overlapping tasks")
	}
	if !preview.RequiresWaves {
		t.Error("RequiresWaves should be true for conflicting tasks")
	}
}

func TestExecutionPreview_Markdown(t *testing.T) {
	preview := &ExecutionPreview{
		TotalTasks:    2,
		CanParallel:   false,
		RequiresWaves: true,
		Partitions: []TaskPartition{
			{Group: 0, TaskIDs: []string{"task-1"}},
			{Group: 1, TaskIDs: []string{"task-2"}},
		},
	}

	md := preview.Markdown()
	if !containsString(md, "Wave") {
		t.Error("Markdown should contain wave information")
	}
}

func TestExecutionReport_Markdown(t *testing.T) {
	report := &ExecutionReport{
		Duration:     1 * time.Second,
		TargetBranch: "main",
		Results: []*AgentResult{
			{TaskID: "task-1", Success: true, Duration: 500 * time.Millisecond},
			{TaskID: "task-2", Success: false, Duration: 300 * time.Millisecond},
		},
	}

	md := report.Markdown()
	if !containsString(md, "main") {
		t.Error("Markdown should contain target branch")
	}
	if !containsString(md, "task-1") {
		t.Error("Markdown should contain task IDs")
	}
}

func TestCoordinator_SetHandlers(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()
	cfg := DefaultCoordinatorConfig("/tmp/repo")
	c := NewCoordinator(mock, executor, cfg)

	conflictCalled := false
	partitionCalled := false
	mergeCalled := false

	c.SetConflictHandler(func(e ConflictEvent) { conflictCalled = true })
	c.SetPartitionHandler(func(e PartitionEvent) { partitionCalled = true })
	c.SetMergeHandler(func(e MergeEvent) { mergeCalled = true })

	// Handlers should be set without error
	_ = conflictCalled
	_ = partitionCalled
	_ = mergeCalled
}

func TestCoordinator_GetLockManager(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()
	cfg := DefaultCoordinatorConfig("/tmp/repo")
	c := NewCoordinator(mock, executor, cfg)

	lm := c.GetLockManager()
	if lm == nil {
		t.Error("GetLockManager() returned nil")
	}
}

func TestCoordinator_GetScopeValidator(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()
	cfg := DefaultCoordinatorConfig("/tmp/repo")
	c := NewCoordinator(mock, executor, cfg)

	sv := c.GetScopeValidator()
	if sv == nil {
		t.Error("GetScopeValidator() returned nil")
	}
}

func TestCoordinator_ExecuteParallel_Empty(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()
	cfg := DefaultCoordinatorConfig("/tmp/repo")
	c := NewCoordinator(mock, executor, cfg)

	ctx := context.Background()
	report, err := c.ExecuteParallel(ctx, nil, "main")
	if err != nil {
		t.Errorf("ExecuteParallel() error = %v", err)
	}
	if report == nil {
		t.Error("ExecuteParallel() returned nil report")
	}
}

func TestCoordinator_ExecuteParallel_Single(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()
	executor.execTime = 10 * time.Millisecond
	cfg := DefaultCoordinatorConfig("/tmp/repo")
	c := NewCoordinator(mock, executor, cfg)

	tasks := []*AgentTask{
		{ID: "task-1", Name: "Test", Context: map[string]string{"files": "a.go"}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	report, err := c.ExecuteParallel(ctx, tasks, "")
	if err != nil {
		t.Fatalf("ExecuteParallel() error = %v", err)
	}
	if len(report.Results) != 1 {
		t.Errorf("Results count = %d, want 1", len(report.Results))
	}
}

// ==== Config Tests ====

func TestDefaultFileLockConfig(t *testing.T) {
	cfg := DefaultFileLockConfig()
	if cfg.DefaultTTL != 5*time.Minute {
		t.Errorf("DefaultTTL = %v, want 5m", cfg.DefaultTTL)
	}
	if cfg.MaxWaitTime != 30*time.Second {
		t.Errorf("MaxWaitTime = %v, want 30s", cfg.MaxWaitTime)
	}
}

// ==== Edge Cases ====

func TestScopeValidator_NilScopes(t *testing.T) {
	v := NewScopeValidator()

	conflicts := v.CheckConflicts(nil)
	if len(conflicts) != 0 {
		t.Error("CheckConflicts(nil) should return empty")
	}

	conflicts = v.CheckConflicts([]*TaskScope{nil, nil})
	if len(conflicts) != 0 {
		t.Error("CheckConflicts with nil scopes should not panic")
	}
}

func TestFileLock_Fields(t *testing.T) {
	now := time.Now()
	lock := FileLock{
		Path:       "file.go",
		AgentID:    "agent-1",
		TaskID:     "task-1",
		AcquiredAt: now,
		ExpiresAt:  now.Add(1 * time.Minute),
		Heartbeat:  now,
	}

	if lock.Path != "file.go" {
		t.Errorf("Path = %q, want %q", lock.Path, "file.go")
	}
	if lock.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want %q", lock.AgentID, "agent-1")
	}
}

func TestLockResult_Fields(t *testing.T) {
	result := LockResult{
		Acquired:   true,
		WaitedFor:  100 * time.Millisecond,
		HeldBy:     "other-agent",
		QueueDepth: 2,
	}

	if !result.Acquired {
		t.Error("Acquired should be true")
	}
	if result.WaitedFor != 100*time.Millisecond {
		t.Errorf("WaitedFor = %v, want 100ms", result.WaitedFor)
	}
}

func TestMergeResult_Fields(t *testing.T) {
	result := MergeResult{
		TaskID:      "task-1",
		Branch:      "feature-1",
		Success:     true,
		AutoMerged:  true,
		MergeCommit: "abc123",
		Duration:    500 * time.Millisecond,
	}

	if result.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", result.TaskID, "task-1")
	}
	if !result.AutoMerged {
		t.Error("AutoMerged should be true")
	}
}

func TestConflict_Fields(t *testing.T) {
	conflict := Conflict{
		TaskA:        "task-1",
		TaskB:        "task-2",
		OverlapFiles: []string{"shared.go"},
		OverlapGlobs: []string{"pkg/..."},
	}

	if conflict.TaskA != "task-1" {
		t.Errorf("TaskA = %q, want %q", conflict.TaskA, "task-1")
	}
	if len(conflict.OverlapFiles) != 1 {
		t.Errorf("OverlapFiles count = %d, want 1", len(conflict.OverlapFiles))
	}
}

func TestTaskPartition_Fields(t *testing.T) {
	partition := TaskPartition{
		Group:   1,
		TaskIDs: []string{"task-1", "task-2"},
		WaitFor: []string{"task-0"},
	}

	if partition.Group != 1 {
		t.Errorf("Group = %d, want 1", partition.Group)
	}
	if len(partition.TaskIDs) != 2 {
		t.Errorf("TaskIDs count = %d, want 2", len(partition.TaskIDs))
	}
}

// testCoordinatorExecutor is a mock for coordinator tests
type testCoordinatorExecutor struct {
	*mockExecutor
}

func (e *testCoordinatorExecutor) Execute(ctx context.Context, task *AgentTask, wtPath string) (*AgentResult, error) {
	return e.mockExecutor.Execute(ctx, task, wtPath)
}

// testCoordinatorManager wraps mockWorktreeManager
type testCoordinatorManager struct {
	*mockWorktreeManager
}

func (m *testCoordinatorManager) Create(branch string) (*worktree.Worktree, error) {
	return m.mockWorktreeManager.Create(branch)
}

func (m *testCoordinatorManager) Remove(branch string, force bool) error {
	return m.mockWorktreeManager.Remove(branch, force)
}
