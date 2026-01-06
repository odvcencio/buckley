package tool

import (
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"go.uber.org/mock/gomock"
)

func TestDefaultActivityGroupingConfig(t *testing.T) {
	config := DefaultActivityGroupingConfig()

	if config.WindowSeconds != 30 {
		t.Errorf("expected window 30 seconds, got %d", config.WindowSeconds)
	}
	if !config.Enabled {
		t.Error("expected grouping to be enabled by default")
	}
}

func TestNewActivityTracker(t *testing.T) {
	config := DefaultActivityGroupingConfig()
	tracker := NewActivityTracker(config)

	if tracker == nil {
		t.Fatal("expected non-nil tracker")
	}
	if tracker.config.WindowSeconds != 30 {
		t.Error("expected config to be set")
	}
}

func TestActivityTracker_RecordCall(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	tracker := NewActivityTracker(DefaultActivityGroupingConfig())
	mockTool := NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("test_tool").AnyTimes()

	startTime := time.Now()
	endTime := startTime.Add(100 * time.Millisecond)

	tracker.RecordCall(mockTool, nil, &builtin.Result{Success: true}, startTime, endTime)

	if len(tracker.calls) != 1 {
		t.Errorf("expected 1 call recorded, got %d", len(tracker.calls))
	}

	call := tracker.calls[0]
	if call.Duration != 100*time.Millisecond {
		t.Errorf("expected duration 100ms, got %v", call.Duration)
	}
}

func TestActivityTracker_RecordCall_GroupingDisabled(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	config := DefaultActivityGroupingConfig()
	config.Enabled = false
	tracker := NewActivityTracker(config)

	mockTool := NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("test_tool").AnyTimes()

	tracker.RecordCall(mockTool, nil, &builtin.Result{Success: true}, time.Now(), time.Now())

	// Should still record call but not create groups
	if len(tracker.calls) != 1 {
		t.Errorf("expected 1 call recorded, got %d", len(tracker.calls))
	}
	if len(tracker.groups) != 0 {
		t.Errorf("expected 0 groups when grouping disabled, got %d", len(tracker.groups))
	}
}

func TestActivityTracker_GetGroups(t *testing.T) {
	tracker := NewActivityTracker(DefaultActivityGroupingConfig())

	groups := tracker.GetGroups()
	if len(groups) != 0 {
		t.Errorf("expected 0 groups initially, got %d", len(groups))
	}
}

func TestActivityTracker_GetLatestGroup_Empty(t *testing.T) {
	tracker := NewActivityTracker(DefaultActivityGroupingConfig())

	latest := tracker.GetLatestGroup()
	if latest != nil {
		t.Error("expected nil for empty tracker")
	}
}

func TestActivityTracker_GetLatestGroup_HasGroups(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	tracker := NewActivityTracker(DefaultActivityGroupingConfig())
	mockTool := NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("read_file").AnyTimes()

	tracker.RecordCall(mockTool, nil, &builtin.Result{Success: true}, time.Now(), time.Now())

	latest := tracker.GetLatestGroup()
	if latest == nil {
		t.Fatal("expected non-nil latest group")
	}
	if len(latest.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call in group, got %d", len(latest.ToolCalls))
	}
}

func TestShortPath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "short path",
			input: "file.go",
			want:  "file.go",
		},
		{
			name:  "two segments",
			input: "pkg/file.go",
			want:  "pkg/file.go",
		},
		{
			name:  "long path",
			input: "/usr/local/bin/file.go",
			want:  ".../file.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shortPath(tt.input)
			if got != tt.want {
				t.Errorf("shortPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestReplacePlaceholders(t *testing.T) {
	tests := []struct {
		name     string
		template string
		params   map[string]any
		result   *builtin.Result
		want     string
	}{
		{
			name:     "no placeholders",
			template: "simple text",
			params:   nil,
			result:   nil,
			want:     "simple text",
		},
		{
			name:     "param placeholder",
			template: "Read {file_path}",
			params:   map[string]any{"file_path": "test.go"},
			result:   nil,
			want:     "Read test.go",
		},
		{
			name:     "result placeholder",
			template: "Found {count} matches",
			params:   nil,
			result:   &builtin.Result{Data: map[string]any{"count": 5}},
			want:     "Found 5 matches",
		},
		{
			name:     "both placeholders",
			template: "Read {file} and found {lines} lines",
			params:   map[string]any{"file": "test.go"},
			result:   &builtin.Result{Data: map[string]any{"lines": 100}},
			want:     "Read test.go and found 100 lines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := replacePlaceholders(tt.template, tt.params, tt.result)
			if got != tt.want {
				t.Errorf("replacePlaceholders() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatCategoryTitle(t *testing.T) {
	tests := []struct {
		category Category
		want     string
	}{
		{CategoryFilesystem, "Filesystem Operations"},
		{CategoryCodebase, "Codebase Operations"},
		{CategoryGit, "Git Operations"},
		{CategoryTesting, "Testing Operations"},
	}

	for _, tt := range tests {
		t.Run(string(tt.category), func(t *testing.T) {
			got := formatCategoryTitle(tt.category)
			if got != tt.want {
				t.Errorf("formatCategoryTitle(%s) = %q, want %q", tt.category, got, tt.want)
			}
		})
	}
}

func TestExtractKeyParams(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   string
	}{
		{
			name:   "empty params",
			params: map[string]any{},
			want:   "",
		},
		{
			name:   "file_path",
			params: map[string]any{"file_path": "/usr/local/test.go"},
			want:   ".../test.go",
		},
		{
			name:   "pattern",
			params: map[string]any{"pattern": "test.*"},
			want:   "'test.*'",
		},
		{
			name:   "command",
			params: map[string]any{"command": "go test"},
			want:   "go test",
		},
		{
			name:   "long command",
			params: map[string]any{"command": strings.Repeat("a", 60)},
			want:   strings.Repeat("a", 47) + "...",
		},
		{
			name:   "multiple params",
			params: map[string]any{"file_path": "/test.go", "pattern": "foo"},
			want:   "/test.go 'foo'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractKeyParams(tt.params)
			if got != tt.want {
				t.Errorf("extractKeyParams() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatActivityLog(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	mockTool := NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("read_file").AnyTimes()

	group := &ActivityGroup{
		Category:  CategoryFilesystem,
		StartTime: time.Date(2025, 11, 18, 10, 30, 0, 0, time.UTC),
		EndTime:   time.Date(2025, 11, 18, 10, 30, 1, 0, time.UTC),
		ToolCalls: []ToolCall{
			{
				Tool:   mockTool,
				Params: map[string]any{"file_path": "/test.go"},
			},
		},
		Summary: "Read test.go",
	}

	result := FormatActivityLog(group)

	if !strings.Contains(result, "10:30:00") {
		t.Error("expected result to contain timestamp")
	}
	if !strings.Contains(result, "Filesystem Operations") {
		t.Error("expected result to contain category")
	}
	if !strings.Contains(result, "read_file") {
		t.Error("expected result to contain tool name")
	}
	if !strings.Contains(result, "Read test.go") {
		t.Error("expected result to contain summary")
	}
}

func TestActivityTracker_GroupingSameCategory(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	tracker := NewActivityTracker(DefaultActivityGroupingConfig())
	mockTool := NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("read_file").AnyTimes()

	now := time.Now()

	// Record two calls within time window and same category
	tracker.RecordCall(mockTool, nil, &builtin.Result{Success: true}, now, now.Add(1*time.Second))
	tracker.RecordCall(mockTool, nil, &builtin.Result{Success: true}, now.Add(2*time.Second), now.Add(3*time.Second))

	groups := tracker.GetGroups()
	if len(groups) != 1 {
		t.Errorf("expected 1 group for same category within window, got %d", len(groups))
	}
	if len(groups[0].ToolCalls) != 2 {
		t.Errorf("expected 2 calls in group, got %d", len(groups[0].ToolCalls))
	}
}

func TestActivityTracker_GroupingDifferentCategory(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	tracker := NewActivityTracker(DefaultActivityGroupingConfig())
	mockFileTool := NewMockTool(ctrl)
	mockFileTool.EXPECT().Name().Return("read_file").AnyTimes()
	mockGitTool := NewMockTool(ctrl)
	mockGitTool.EXPECT().Name().Return("git_status").AnyTimes()

	now := time.Now()

	// Record calls with different categories - should create separate groups
	tracker.RecordCall(mockFileTool, nil, &builtin.Result{Success: true}, now, now.Add(1*time.Second))
	tracker.RecordCall(mockGitTool, nil, &builtin.Result{Success: true}, now.Add(2*time.Second), now.Add(3*time.Second))

	groups := tracker.GetGroups()
	if len(groups) != 2 {
		t.Errorf("expected 2 groups for different categories, got %d", len(groups))
	}
}

func TestActivityTracker_GroupingOutsideWindow(t *testing.T) {
	ctrl := gomock.NewController(t)
	t.Cleanup(ctrl.Finish)

	config := DefaultActivityGroupingConfig()
	config.WindowSeconds = 5 // 5 second window
	tracker := NewActivityTracker(config)

	mockTool := NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("read_file").AnyTimes()

	now := time.Now()

	// Record two calls outside time window - should create separate groups
	tracker.RecordCall(mockTool, nil, &builtin.Result{Success: true}, now, now.Add(1*time.Second))
	tracker.RecordCall(mockTool, nil, &builtin.Result{Success: true}, now.Add(10*time.Second), now.Add(11*time.Second))

	groups := tracker.GetGroups()
	if len(groups) != 2 {
		t.Errorf("expected 2 groups for calls outside window, got %d", len(groups))
	}
}
