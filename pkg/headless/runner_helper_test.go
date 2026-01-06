package headless

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func TestAnyToInt(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected int
		ok       bool
	}{
		{name: "float64", input: float64(42), expected: 42, ok: true},
		{name: "float32", input: float32(42), expected: 42, ok: true},
		{name: "int", input: 42, expected: 42, ok: true},
		{name: "int64", input: int64(42), expected: 42, ok: true},
		{name: "int32", input: int32(42), expected: 42, ok: true},
		{name: "json.Number int", input: json.Number("42"), expected: 42, ok: true},
		{name: "string int", input: "42", expected: 42, ok: true},
		{name: "string with whitespace", input: "  42  ", expected: 42, ok: true},
		{name: "empty string", input: "", expected: 0, ok: false},
		{name: "whitespace only string", input: "   ", expected: 0, ok: false},
		{name: "invalid string", input: "not a number", expected: 0, ok: false},
		{name: "json.Number float", input: json.Number("42.5"), expected: 0, ok: false},
		{name: "nil", input: nil, expected: 0, ok: false},
		{name: "bool", input: true, expected: 0, ok: false},
		{name: "slice", input: []int{1, 2}, expected: 0, ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := anyToInt(tc.input)
			if ok != tc.ok {
				t.Errorf("anyToInt(%v): ok = %v, want %v", tc.input, ok, tc.ok)
			}
			if ok && got != tc.expected {
				t.Errorf("anyToInt(%v) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestTruncateOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		expected string
	}{
		{name: "short string unchanged", input: "hello", maxLen: 10, expected: "hello"},
		{name: "exact length unchanged", input: "hello", maxLen: 5, expected: "hello"},
		{name: "long string truncated", input: "hello world", maxLen: 5, expected: "hello..."},
		{name: "empty string", input: "", maxLen: 10, expected: ""},
		{name: "zero max length", input: "hello", maxLen: 0, expected: "..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateOutput(tc.input, tc.maxLen)
			if got != tc.expected {
				t.Errorf("truncateOutput(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.expected)
			}
		})
	}
}

func TestGetMessageContent(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected string
	}{
		{name: "string content", input: "hello world", expected: "hello world"},
		{name: "empty string", input: "", expected: ""},
		{name: "content parts with text", input: []model.ContentPart{
			{Type: "text", Text: "hello"},
			{Type: "text", Text: "world"},
		}, expected: "hello\nworld"},
		{name: "content parts with non-text", input: []model.ContentPart{
			{Type: "text", Text: "hello"},
			{Type: "image", Text: ""},
			{Type: "text", Text: "world"},
		}, expected: "hello\nworld"},
		{name: "empty content parts", input: []model.ContentPart{}, expected: ""},
		{name: "content parts with empty text", input: []model.ContentPart{
			{Type: "text", Text: ""},
		}, expected: ""},
		{name: "integer", input: 42, expected: "42"},
		{name: "nil", input: nil, expected: "<nil>"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getMessageContent(tc.input)
			if got != tc.expected {
				t.Errorf("getMessageContent(%v) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestFormatToolResult(t *testing.T) {
	runner := &Runner{}

	tests := []struct {
		name     string
		result   *builtin.Result
		expected string
	}{
		{name: "nil result", result: nil, expected: "No result"},
		{name: "error result", result: &builtin.Result{Success: false, Error: "something failed"}, expected: "Error: something failed"},
		{name: "success with display message", result: &builtin.Result{
			Success:     true,
			DisplayData: map[string]any{"message": "File created"},
		}, expected: "File created"},
		{name: "success with data", result: &builtin.Result{
			Success: true,
			Data:    map[string]any{"foo": "bar"},
		}, expected: "{\n  \"foo\": \"bar\"\n}"},
		{name: "success with no data", result: &builtin.Result{Success: true}, expected: "Success"},
		{name: "success with empty display message", result: &builtin.Result{
			Success:     true,
			DisplayData: map[string]any{"message": ""},
		}, expected: "Success"},
		{name: "success with empty data", result: &builtin.Result{
			Success: true,
			Data:    map[string]any{},
		}, expected: "Success"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runner.formatToolResult(tc.result)
			if got != tc.expected {
				t.Errorf("formatToolResult() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestPlanTaskStatus(t *testing.T) {
	tests := []struct {
		status   orchestrator.TaskStatus
		expected string
	}{
		{orchestrator.TaskPending, "[ ]"},
		{orchestrator.TaskInProgress, "[\u2192]"},
		{orchestrator.TaskCompleted, "[\u2713]"},
		{orchestrator.TaskFailed, "[\u2717]"},
		{orchestrator.TaskSkipped, "[-]"},
		{orchestrator.TaskStatus(99), "[?]"}, // Unknown status value
	}

	for _, tc := range tests {
		t.Run(tc.expected, func(t *testing.T) {
			got := planTaskStatus(tc.status)
			if got != tc.expected {
				t.Errorf("planTaskStatus(%v) = %q, want %q", tc.status, got, tc.expected)
			}
		})
	}
}

func TestFormatPlanSummary(t *testing.T) {
	t.Run("nil plan", func(t *testing.T) {
		got := formatPlanSummary(nil, nil)
		if got != "Plan unavailable." {
			t.Errorf("expected 'Plan unavailable.', got %q", got)
		}
	})

	t.Run("plan with tasks", func(t *testing.T) {
		plan := &orchestrator.Plan{
			ID:          "test-plan-123",
			FeatureName: "Test Feature",
			Tasks: []orchestrator.Task{
				{ID: "1", Title: "Task 1"},
				{ID: "2", Title: "Task 2"},
			},
			CreatedAt: time.Now(),
		}
		cfg := &config.Config{
			Artifacts: config.ArtifactsConfig{
				PlanningDir: "/docs/plans",
			},
		}

		got := formatPlanSummary(plan, cfg)
		if got == "" {
			t.Error("expected non-empty summary")
		}
		// Check key content is present
		if !contains(got, "Test Feature") {
			t.Error("expected feature name in summary")
		}
		if !contains(got, "test-plan-123") {
			t.Error("expected plan ID in summary")
		}
		if !contains(got, "Task 1") {
			t.Error("expected task 1 in summary")
		}
		if !contains(got, "Task 2") {
			t.Error("expected task 2 in summary")
		}
	})

	t.Run("plan without config", func(t *testing.T) {
		plan := &orchestrator.Plan{
			ID:          "test-plan",
			FeatureName: "Feature",
			Tasks:       []orchestrator.Task{},
		}
		got := formatPlanSummary(plan, nil)
		if got == "" {
			t.Error("expected non-empty summary")
		}
	})
}

func TestFormatCommandError(t *testing.T) {
	runner := &Runner{}

	t.Run("nil error", func(t *testing.T) {
		got := runner.formatCommandError(nil)
		if got != "" {
			t.Errorf("expected empty string for nil error, got %q", got)
		}
	})

	t.Run("non-nil error", func(t *testing.T) {
		err := fmt.Errorf("session not found")
		got := runner.formatCommandError(err)
		expected := "Error: session not found"
		if got != expected {
			t.Errorf("formatCommandError() = %q, want %q", got, expected)
		}
	})
}

func TestClampTimeoutSeconds(t *testing.T) {
	tests := []struct {
		name       string
		args       map[string]any
		key        string
		maxSeconds int
		expected   int
	}{
		{
			name:       "no existing value sets max",
			args:       map[string]any{},
			key:        "timeout_seconds",
			maxSeconds: 30,
			expected:   30,
		},
		{
			name:       "value under max unchanged",
			args:       map[string]any{"timeout_seconds": 20},
			key:        "timeout_seconds",
			maxSeconds: 30,
			expected:   20,
		},
		{
			name:       "value over max clamped",
			args:       map[string]any{"timeout_seconds": 60},
			key:        "timeout_seconds",
			maxSeconds: 30,
			expected:   30,
		},
		{
			name:       "zero value clamped to max",
			args:       map[string]any{"timeout_seconds": 0},
			key:        "timeout_seconds",
			maxSeconds: 30,
			expected:   30,
		},
		{
			name:       "negative value clamped to max",
			args:       map[string]any{"timeout_seconds": -5},
			key:        "timeout_seconds",
			maxSeconds: 30,
			expected:   30,
		},
		{
			name:       "float64 value",
			args:       map[string]any{"timeout_seconds": float64(20)},
			key:        "timeout_seconds",
			maxSeconds: 30,
			expected:   20,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clampTimeoutSeconds(tc.args, tc.key, tc.maxSeconds)
			got, ok := anyToInt(tc.args[tc.key])
			if !ok {
				t.Fatalf("expected integer value, got %T", tc.args[tc.key])
			}
			if got != tc.expected {
				t.Errorf("args[%q] = %d, want %d", tc.key, got, tc.expected)
			}
		})
	}

	t.Run("nil args no panic", func(t *testing.T) {
		clampTimeoutSeconds(nil, "key", 30)
	})

	t.Run("empty key no change", func(t *testing.T) {
		args := map[string]any{"timeout_seconds": 60}
		clampTimeoutSeconds(args, "", 30)
		if args["timeout_seconds"] != 60 {
			t.Error("expected no change with empty key")
		}
	})

	t.Run("zero max no change", func(t *testing.T) {
		args := map[string]any{"timeout_seconds": 60}
		clampTimeoutSeconds(args, "timeout_seconds", 0)
		if args["timeout_seconds"] != 60 {
			t.Error("expected no change with zero max")
		}
	})
}

func TestDocsRootFromConfig(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		got := docsRootFromConfig(nil)
		if got != "docs" {
			t.Errorf("docsRootFromConfig(nil) = %q, want %q", got, "docs")
		}
	})

	t.Run("empty planning dir", func(t *testing.T) {
		cfg := &config.Config{}
		got := docsRootFromConfig(cfg)
		if got != "docs" {
			t.Errorf("expected 'docs', got %q", got)
		}
	})

	t.Run("custom planning dir", func(t *testing.T) {
		cfg := &config.Config{
			Artifacts: config.ArtifactsConfig{
				PlanningDir: "custom/plans",
			},
		}
		got := docsRootFromConfig(cfg)
		if got != "custom" {
			t.Errorf("expected 'custom', got %q", got)
		}
	})
}

func TestResolveSessionConfig(t *testing.T) {
	t.Run("nil config returns default", func(t *testing.T) {
		got := resolveSessionConfig(nil, nil)
		if got == nil {
			t.Fatal("expected non-nil config")
		}
	})

	t.Run("nil session returns copy", func(t *testing.T) {
		cfg := config.DefaultConfig()
		got := resolveSessionConfig(cfg, nil)
		if got == cfg {
			t.Error("expected a copy, not the same pointer")
		}
	})

	t.Run("resolves relative paths", func(t *testing.T) {
		cfg := &config.Config{
			Artifacts: config.ArtifactsConfig{
				PlanningDir:  "docs/plans",
				ExecutionDir: "docs/execution",
			},
		}
		sess := &storage.Session{
			ProjectPath: "/home/user/project",
		}
		got := resolveSessionConfig(cfg, sess)

		if got.Artifacts.PlanningDir != "/home/user/project/docs/plans" {
			t.Errorf("PlanningDir = %q, want /home/user/project/docs/plans", got.Artifacts.PlanningDir)
		}
	})

	t.Run("absolute paths unchanged", func(t *testing.T) {
		cfg := &config.Config{
			Artifacts: config.ArtifactsConfig{
				PlanningDir: "/absolute/path/plans",
			},
		}
		sess := &storage.Session{
			ProjectPath: "/home/user/project",
		}
		got := resolveSessionConfig(cfg, sess)

		if got.Artifacts.PlanningDir != "/absolute/path/plans" {
			t.Errorf("PlanningDir = %q, want /absolute/path/plans", got.Artifacts.PlanningDir)
		}
	})

	t.Run("uses GitRepo if ProjectPath empty", func(t *testing.T) {
		cfg := &config.Config{
			Artifacts: config.ArtifactsConfig{
				PlanningDir: "docs/plans",
			},
		}
		sess := &storage.Session{
			GitRepo: "/home/user/gitrepo",
		}
		got := resolveSessionConfig(cfg, sess)

		if got.Artifacts.PlanningDir != "/home/user/gitrepo/docs/plans" {
			t.Errorf("PlanningDir = %q, expected resolved from GitRepo", got.Artifacts.PlanningDir)
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
