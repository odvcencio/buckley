package pr

import (
	"strings"
	"testing"
)

func TestPRResultValidate(t *testing.T) {
	tests := []struct {
		name    string
		pr      PRResult
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid minimal PR",
			pr: PRResult{
				Title:   "add(auth): user authentication",
				Summary: "This PR adds user authentication to the application.",
				Changes: []string{"Add login endpoint"},
				Testing: []string{"Run auth tests"},
			},
			wantErr: false,
		},
		{
			name: "valid full PR",
			pr: PRResult{
				Title:         "fix(api): rate limiting",
				Summary:       "Fixes rate limiting issues in the API.",
				Changes:       []string{"Add rate limiter", "Update middleware"},
				Testing:       []string{"Run load tests", "Verify rate limits"},
				Breaking:      true,
				Issues:        []string{"123", "456"},
				ReviewersHint: "Focus on the rate limiter configuration",
			},
			wantErr: false,
		},
		{
			name: "missing title",
			pr: PRResult{
				Summary: "Summary",
				Changes: []string{"Change"},
				Testing: []string{"Test"},
			},
			wantErr: true,
			errMsg:  "title is required",
		},
		{
			name: "title too long",
			pr: PRResult{
				Title:   strings.Repeat("a", 101),
				Summary: "Summary",
				Changes: []string{"Change"},
				Testing: []string{"Test"},
			},
			wantErr: true,
			errMsg:  "title too long",
		},
		{
			name: "missing summary",
			pr: PRResult{
				Title:   "Title",
				Changes: []string{"Change"},
				Testing: []string{"Test"},
			},
			wantErr: true,
			errMsg:  "summary is required",
		},
		{
			name: "missing changes",
			pr: PRResult{
				Title:   "Title",
				Summary: "Summary",
				Testing: []string{"Test"},
			},
			wantErr: true,
			errMsg:  "at least one change is required",
		},
		{
			name: "missing testing",
			pr: PRResult{
				Title:   "Title",
				Summary: "Summary",
				Changes: []string{"Change"},
			},
			wantErr: true,
			errMsg:  "at least one testing instruction is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.pr.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errMsg)
					return
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestPRResultFormatBody(t *testing.T) {
	pr := PRResult{
		Title:   "add(auth): user authentication",
		Summary: "This PR adds user authentication to the application.",
		Changes: []string{
			"Add login endpoint",
			"Add logout endpoint",
			"Add session management",
		},
		Testing: []string{
			"Run `go test ./pkg/auth/...`",
			"Test login flow manually",
		},
		Breaking:      true,
		Issues:        []string{"123", "456"},
		ReviewersHint: "Focus on the session timeout logic",
	}

	body := pr.FormatBody()

	// Check sections exist
	if !strings.Contains(body, "## Summary") {
		t.Error("expected Summary section")
	}
	if !strings.Contains(body, "## Changes") {
		t.Error("expected Changes section")
	}
	if !strings.Contains(body, "## Testing") {
		t.Error("expected Testing section")
	}
	if !strings.Contains(body, "## Breaking Changes") {
		t.Error("expected Breaking Changes section")
	}
	if !strings.Contains(body, "## Related Issues") {
		t.Error("expected Related Issues section")
	}
	if !strings.Contains(body, "## Review Focus") {
		t.Error("expected Review Focus section")
	}

	// Check content
	if !strings.Contains(body, pr.Summary) {
		t.Error("expected summary in body")
	}
	if !strings.Contains(body, "- Add login endpoint") {
		t.Error("expected change bullet")
	}
	if !strings.Contains(body, "Closes #123") {
		t.Error("expected issue reference")
	}
}

func TestPRResultFormatBodyMinimal(t *testing.T) {
	pr := PRResult{
		Title:   "fix: typo",
		Summary: "Fixes a typo in the readme.",
		Changes: []string{"Fix typo"},
		Testing: []string{"Review the change"},
	}

	body := pr.FormatBody()

	// Should NOT have optional sections
	if strings.Contains(body, "## Breaking Changes") {
		t.Error("should not have Breaking Changes section")
	}
	if strings.Contains(body, "## Related Issues") {
		t.Error("should not have Related Issues section")
	}
	if strings.Contains(body, "## Review Focus") {
		t.Error("should not have Review Focus section")
	}
}

func TestPRResultFormatBody_StructuredSections(t *testing.T) {
	pr := PRResult{
		Title:   "add(api): new endpoint",
		Summary: "Adds a new API endpoint for user management.",
		Changes: []string{
			"Add GET /users endpoint",
			"Add POST /users endpoint",
			"Add DELETE /users endpoint",
		},
		Testing: []string{
			"Run `go test ./pkg/api/...`",
			"Test with curl: `curl localhost:8080/users`",
		},
	}

	body := pr.FormatBody()

	// Verify each change is a bullet
	for _, change := range pr.Changes {
		if !strings.Contains(body, "- "+change) {
			t.Errorf("expected change bullet: %q", change)
		}
	}

	// Verify each testing instruction is a bullet
	for _, test := range pr.Testing {
		if !strings.Contains(body, "- "+test) {
			t.Errorf("expected testing bullet: %q", test)
		}
	}
}

func TestPRResultValidate_WhitespaceTitle(t *testing.T) {
	pr := PRResult{
		Title:   "   ",
		Summary: "Summary",
		Changes: []string{"Change"},
		Testing: []string{"Test"},
	}

	err := pr.Validate()
	if err == nil {
		t.Error("expected error for whitespace-only title")
	}
	if !strings.Contains(err.Error(), "title") {
		t.Errorf("expected title error, got %v", err)
	}
}

func TestPRResultValidate_WhitespaceSummary(t *testing.T) {
	pr := PRResult{
		Title:   "Title",
		Summary: "   ",
		Changes: []string{"Change"},
		Testing: []string{"Test"},
	}

	err := pr.Validate()
	if err == nil {
		t.Error("expected error for whitespace-only summary")
	}
	if !strings.Contains(err.Error(), "summary") {
		t.Errorf("expected summary error, got %v", err)
	}
}

func TestPRResultValidate_BoundaryTitleLength(t *testing.T) {
	// Exactly 100 chars should pass
	pr := PRResult{
		Title:   strings.Repeat("a", 100),
		Summary: "Summary",
		Changes: []string{"Change"},
		Testing: []string{"Test"},
	}

	err := pr.Validate()
	if err != nil {
		t.Errorf("expected 100-char title to pass, got error: %v", err)
	}

	// 101 chars should fail
	pr.Title = strings.Repeat("a", 101)
	err = pr.Validate()
	if err == nil {
		t.Error("expected 101-char title to fail")
	}
}

func TestPRResultFields(t *testing.T) {
	pr := PRResult{
		Title:         "title",
		Summary:       "summary",
		Changes:       []string{"c1", "c2"},
		Testing:       []string{"t1"},
		Breaking:      true,
		Issues:        []string{"123"},
		ReviewersHint: "hint",
	}

	if pr.Title != "title" {
		t.Errorf("Title = %q", pr.Title)
	}
	if pr.Summary != "summary" {
		t.Errorf("Summary = %q", pr.Summary)
	}
	if len(pr.Changes) != 2 {
		t.Errorf("Changes length = %d", len(pr.Changes))
	}
	if len(pr.Testing) != 1 {
		t.Errorf("Testing length = %d", len(pr.Testing))
	}
	if !pr.Breaking {
		t.Error("Breaking = false")
	}
	if len(pr.Issues) != 1 {
		t.Errorf("Issues length = %d", len(pr.Issues))
	}
	if pr.ReviewersHint != "hint" {
		t.Errorf("ReviewersHint = %q", pr.ReviewersHint)
	}
}

func TestGeneratePRTool_Schema(t *testing.T) {
	if GeneratePRTool.Name != "generate_pull_request" {
		t.Errorf("Name = %q, want 'generate_pull_request'", GeneratePRTool.Name)
	}
	if GeneratePRTool.Description == "" {
		t.Error("Description should not be empty")
	}
	if GeneratePRTool.Parameters.Type != "object" {
		t.Errorf("Parameters.Type = %q, want 'object'", GeneratePRTool.Parameters.Type)
	}

	// Check required fields
	props := GeneratePRTool.Parameters.Properties
	requiredFields := []string{"title", "summary", "changes", "testing"}
	for _, field := range requiredFields {
		if _, ok := props[field]; !ok {
			t.Errorf("expected required field %q in properties", field)
		}
	}

	// Check optional fields exist
	optionalFields := []string{"breaking", "issues", "reviewers_hint"}
	for _, field := range optionalFields {
		if _, ok := props[field]; !ok {
			t.Errorf("expected optional field %q in properties", field)
		}
	}
}
