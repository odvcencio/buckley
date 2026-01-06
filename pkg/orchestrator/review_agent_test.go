package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/tool"
)

func TestReviewAgentReviewApproved(t *testing.T) {
	tmpDir := t.TempDir()
	origWD, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(origWD)

	// Prepare file under review.
	if err := os.MkdirAll("pkg", 0o755); err != nil {
		t.Fatalf("mkdir pkg: %v", err)
	}
	targetFile := filepath.Join("pkg", "foo.go")
	if err := os.WriteFile(targetFile, []byte("package foo\n\nfunc Bar() {}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.Artifacts.PlanningDir = filepath.Join(tmpDir, "docs", "plans")
	cfg.Artifacts.ExecutionDir = filepath.Join(tmpDir, "docs", "execution")
	cfg.Artifacts.ReviewDir = filepath.Join(tmpDir, "docs", "reviews")

	plan := &Plan{
		ID:          "plan-123",
		FeatureName: "Sample Feature",
		Description: "Ensure foo works",
	}

	registry := tool.NewRegistry()

	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()

	mockModel.EXPECT().SupportsReasoning(cfg.Models.Review).Return(false)
	jsonResponse := `{
		"summary": "All checks passed",
		"validation_strategy": {
			"critical_path": ["security"],
			"high_risk_areas": []
		},
		"validation_results": [
			{
				"category": "Security",
				"status": "pass",
				"checks": [
					{
						"name": "Input validation",
						"status": "pass",
						"description": "All inputs validated"
					}
				]
			}
		],
		"issues": [],
		"opportunistic_improvements": [],
		"approval": {
			"status": "approved",
			"summary": "Looks good",
			"ready_for_pr": true,
			"remaining_work": []
		}
	}`
	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).Return(mockChatResponse(jsonResponse), nil)

	agent := NewReviewAgent(plan, cfg, mockModel, registry, nil)
	if agent == nil {
		t.Fatal("expected review agent")
	}

	task := &Task{
		ID:          "1",
		Title:       "Add foo",
		Description: "Implement foo behavior",
		Files:       []string{targetFile},
	}

	builderResult := &BuilderResult{
		Implementation: "```go\npackage foo\nfunc Bar() {}\n```",
		Files: []BuilderFile{
			{Path: targetFile, LinesAdded: 2},
		},
	}

	result, err := agent.Review(task, builderResult)
	if err != nil {
		t.Fatalf("review failed: %v", err)
	}
	if result == nil {
		t.Fatal("expected review result")
	}
	if !result.Approved {
		t.Fatalf("expected review approval, got %+v", result)
	}
	if result.ArtifactPath == "" {
		t.Fatal("expected artifact path in review result")
	}
	if _, err := os.Stat(result.ArtifactPath); err != nil {
		t.Fatalf("expected artifact file: %v", err)
	}
}

func TestDetermineApproval(t *testing.T) {
	tests := []struct {
		name      string
		resp      reviewAgentResponse
		allowNits bool
		expected  bool
	}{
		{
			name: "approved status",
			resp: reviewAgentResponse{
				Approval: reviewApprovalPayload{Status: "approved"},
			},
			allowNits: false,
			expected:  true,
		},
		{
			name: "approved uppercase",
			resp: reviewAgentResponse{
				Approval: reviewApprovalPayload{Status: "APPROVED"},
			},
			allowNits: false,
			expected:  true,
		},
		{
			name: "approved_with_nits allows nits",
			resp: reviewAgentResponse{
				Approval: reviewApprovalPayload{Status: "approved_with_nits"},
			},
			allowNits: true,
			expected:  true,
		},
		{
			name: "approved_with_nits disallow nits",
			resp: reviewAgentResponse{
				Approval: reviewApprovalPayload{Status: "approved_with_nits"},
			},
			allowNits: false,
			expected:  false,
		},
		{
			name: "changes_requested",
			resp: reviewAgentResponse{
				Approval: reviewApprovalPayload{Status: "changes_requested"},
			},
			allowNits: true,
			expected:  false,
		},
		{
			name: "critical issue blocks",
			resp: reviewAgentResponse{
				Issues: []reviewIssuePayload{{Severity: "critical"}},
			},
			allowNits: true,
			expected:  false,
		},
		{
			name: "quality issue blocks",
			resp: reviewAgentResponse{
				Issues: []reviewIssuePayload{{Severity: "quality"}},
			},
			allowNits: true,
			expected:  false,
		},
		{
			name: "no issues approves",
			resp: reviewAgentResponse{
				Issues: []reviewIssuePayload{},
			},
			allowNits: false,
			expected:  true,
		},
		{
			name: "only nits with allowNits",
			resp: reviewAgentResponse{
				Issues: []reviewIssuePayload{{Severity: "nit"}},
			},
			allowNits: true,
			expected:  true,
		},
		{
			name: "only nits without allowNits",
			resp: reviewAgentResponse{
				Issues: []reviewIssuePayload{{Severity: "nit"}},
			},
			allowNits: false,
			expected:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := determineApproval(tc.resp, tc.allowNits)
			if got != tc.expected {
				t.Errorf("determineApproval() = %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestTruncateContent(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		maxChars int
		maxLines int
		expected string
	}{
		{
			name:     "empty string",
			value:    "",
			maxChars: 100,
			maxLines: 10,
			expected: "",
		},
		{
			name:     "short string",
			value:    "hello",
			maxChars: 100,
			maxLines: 10,
			expected: "hello",
		},
		{
			name:     "truncate by chars",
			value:    "hello world",
			maxChars: 5,
			maxLines: 10,
			expected: "hello\n... (content truncated)",
		},
		{
			name:     "truncate by lines",
			value:    "line1\nline2\nline3\nline4\nline5",
			maxChars: 1000,
			maxLines: 3,
			expected: "line1\nline2\nline3\n... (2 more lines truncated)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateContent(tc.value, tc.maxChars, tc.maxLines)
			if got != tc.expected {
				t.Errorf("truncateContent() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestTruncateForLog(t *testing.T) {
	tests := []struct {
		value    string
		maxLen   int
		expected string
	}{
		{"", 10, ""},
		{"short", 100, "short"},
		{"hello world this is long", 10, "hello worl..."},
	}

	for _, tc := range tests {
		got := truncateForLog(tc.value, tc.maxLen)
		if got != tc.expected {
			t.Errorf("truncateForLog(%q, %d) = %q, want %q", tc.value, tc.maxLen, got, tc.expected)
		}
	}
}

func TestDefaultApprovalStatus(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"approved", "approved"},
		{"APPROVED", "approved"},
		{"approved_with_nits", "approved_with_nits"},
		{"changes_requested", "changes_requested"},
		{"unknown", "changes_requested"},
		{"", "changes_requested"},
	}

	for _, tc := range tests {
		got := defaultApprovalStatus(tc.status)
		if got != tc.expected {
			t.Errorf("defaultApprovalStatus(%q) = %q, want %q", tc.status, got, tc.expected)
		}
	}
}

func TestReviewAgent_combineFilePaths(t *testing.T) {
	cfg := config.DefaultConfig()
	agent := &ReviewAgent{config: cfg}

	tests := []struct {
		name     string
		task     *Task
		result   *BuilderResult
		expected int
	}{
		{
			name:     "empty inputs",
			task:     &Task{Files: []string{}},
			result:   &BuilderResult{Files: []BuilderFile{}},
			expected: 0,
		},
		{
			name:     "only task files",
			task:     &Task{Files: []string{"a.go", "b.go"}},
			result:   &BuilderResult{Files: []BuilderFile{}},
			expected: 2,
		},
		{
			name:     "only builder files",
			task:     &Task{Files: []string{}},
			result:   &BuilderResult{Files: []BuilderFile{{Path: "c.go"}, {Path: "d.go"}}},
			expected: 2,
		},
		{
			name:     "combined deduplicated",
			task:     &Task{Files: []string{"a.go", "b.go"}},
			result:   &BuilderResult{Files: []BuilderFile{{Path: "b.go"}, {Path: "c.go"}}},
			expected: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := agent.combineFilePaths(tc.task, tc.result)
			if len(got) != tc.expected {
				t.Errorf("combineFilePaths() returned %d files, want %d", len(got), tc.expected)
			}
		})
	}
}
