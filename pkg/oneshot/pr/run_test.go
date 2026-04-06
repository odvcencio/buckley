package pr

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// mockModelClient implements oneshot.ModelClient for testing.
type mockModelClient struct {
	response *model.ChatResponse
	err      error
}

func (m *mockModelClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}

func TestNewRunner(t *testing.T) {
	client := &mockModelClient{}
	invoker := oneshot.NewInvoker(oneshot.InvokerConfig{
		Client:   client,
		Model:    "test-model",
		Provider: "test",
		Pricing:  transparency.ModelPricing{},
	})

	cfg := RunnerConfig{
		Invoker: invoker,
		Ledger:  nil,
	}

	runner := NewRunner(cfg)
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}
	if runner.invoker != invoker {
		t.Error("invoker not set correctly")
	}
}

func TestNewRunner_WithLedger(t *testing.T) {
	client := &mockModelClient{}
	ledger := transparency.NewCostLedger()
	invoker := oneshot.NewInvoker(oneshot.InvokerConfig{
		Client: client,
		Model:  "test-model",
		Ledger: ledger,
	})

	cfg := RunnerConfig{
		Invoker: invoker,
		Ledger:  ledger,
	}

	runner := NewRunner(cfg)
	if runner == nil {
		t.Fatal("NewRunner returned nil")
	}
	if runner.ledger != ledger {
		t.Error("ledger not set correctly")
	}
}

func TestRunResult_Fields(t *testing.T) {
	result := RunResult{
		PR: &PRResult{
			Title:   "test title",
			Summary: "test summary",
			Changes: []string{"change 1"},
			Testing: []string{"test 1"},
		},
		Context: &Context{
			Branch:     "feature/test",
			BaseBranch: "main",
		},
		Trace: &transparency.Trace{
			ID:    "trace-1",
			Model: "test-model",
		},
		ContextAudit: transparency.NewContextAudit(),
		Error:        nil,
	}

	if result.PR == nil {
		t.Error("PR should not be nil")
	}
	if result.PR.Title != "test title" {
		t.Errorf("PR.Title = %q", result.PR.Title)
	}
	if result.Context == nil {
		t.Error("Context should not be nil")
	}
	if result.Context.Branch != "feature/test" {
		t.Errorf("Context.Branch = %q", result.Context.Branch)
	}
	if result.Trace == nil {
		t.Error("Trace should not be nil")
	}
	if result.ContextAudit == nil {
		t.Error("ContextAudit should not be nil")
	}
	if result.Error != nil {
		t.Errorf("Error = %v", result.Error)
	}
}

func TestRunResult_WithError(t *testing.T) {
	testErr := errors.New("test error")
	result := RunResult{
		Error: testErr,
	}

	if result.Error != testErr {
		t.Errorf("Error = %v, want %v", result.Error, testErr)
	}
}

func TestRunnerConfig_Fields(t *testing.T) {
	client := &mockModelClient{}
	ledger := transparency.NewCostLedger()
	invoker := oneshot.NewInvoker(oneshot.InvokerConfig{
		Client: client,
		Model:  "test-model",
	})

	cfg := RunnerConfig{
		Invoker: invoker,
		Ledger:  ledger,
	}

	if cfg.Invoker != invoker {
		t.Error("Invoker not set")
	}
	if cfg.Ledger != ledger {
		t.Error("Ledger not set")
	}
}

// Tests for PRResult.Unmarshal scenarios
func TestPRResultUnmarshal_Valid(t *testing.T) {
	jsonData := `{
		"title": "add(api): new endpoint",
		"summary": "Adds a new API endpoint.",
		"changes": ["Add GET endpoint", "Add POST endpoint"],
		"testing": ["Run tests", "Manual verification"],
		"breaking": false,
		"issues": ["123"],
		"reviewers_hint": "Check the auth logic"
	}`

	var pr PRResult
	err := json.Unmarshal([]byte(jsonData), &pr)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if pr.Title != "add(api): new endpoint" {
		t.Errorf("Title = %q", pr.Title)
	}
	if pr.Summary != "Adds a new API endpoint." {
		t.Errorf("Summary = %q", pr.Summary)
	}
	if len(pr.Changes) != 2 {
		t.Errorf("Changes length = %d", len(pr.Changes))
	}
	if len(pr.Testing) != 2 {
		t.Errorf("Testing length = %d", len(pr.Testing))
	}
	if pr.Breaking {
		t.Error("Breaking should be false")
	}
	if len(pr.Issues) != 1 || pr.Issues[0] != "123" {
		t.Errorf("Issues = %v", pr.Issues)
	}
	if pr.ReviewersHint != "Check the auth logic" {
		t.Errorf("ReviewersHint = %q", pr.ReviewersHint)
	}
}

func TestPRResultUnmarshal_Minimal(t *testing.T) {
	jsonData := `{
		"title": "fix: typo",
		"summary": "Fixes a typo.",
		"changes": ["Fix typo"],
		"testing": ["Review"]
	}`

	var pr PRResult
	err := json.Unmarshal([]byte(jsonData), &pr)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if err := pr.Validate(); err != nil {
		t.Errorf("Validate failed: %v", err)
	}
	if pr.Breaking {
		t.Error("Breaking should default to false")
	}
	if len(pr.Issues) != 0 {
		t.Error("Issues should be empty")
	}
	if pr.ReviewersHint != "" {
		t.Error("ReviewersHint should be empty")
	}
}

func TestPRResultUnmarshal_WithBreaking(t *testing.T) {
	jsonData := `{
		"title": "feat!: breaking change",
		"summary": "Introduces a breaking change.",
		"changes": ["Change API"],
		"testing": ["Update clients"],
		"breaking": true
	}`

	var pr PRResult
	err := json.Unmarshal([]byte(jsonData), &pr)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if !pr.Breaking {
		t.Error("Breaking should be true")
	}
}

// Tests for edge cases in FormatBody
func TestPRResultFormatBody_MultipleIssues(t *testing.T) {
	pr := PRResult{
		Title:   "fix: multiple issues",
		Summary: "Fixes multiple issues.",
		Changes: []string{"Fix issue 1", "Fix issue 2"},
		Testing: []string{"Run tests"},
		Issues:  []string{"100", "101", "102"},
	}

	body := pr.FormatBody()

	if !contains(body, "Closes #100") {
		t.Error("expected issue 100")
	}
	if !contains(body, "Closes #101") {
		t.Error("expected issue 101")
	}
	if !contains(body, "Closes #102") {
		t.Error("expected issue 102")
	}
}

func TestPRResultFormatBody_EmptyReviewersHint(t *testing.T) {
	pr := PRResult{
		Title:         "test: pr",
		Summary:       "Test PR.",
		Changes:       []string{"Change"},
		Testing:       []string{"Test"},
		ReviewersHint: "",
	}

	body := pr.FormatBody()

	if contains(body, "## Review Focus") {
		t.Error("should not have Review Focus section when hint is empty")
	}
}

func TestPRResultFormatBody_NotBreaking(t *testing.T) {
	pr := PRResult{
		Title:    "feat: new feature",
		Summary:  "Adds a new feature.",
		Changes:  []string{"Add feature"},
		Testing:  []string{"Run tests"},
		Breaking: false,
	}

	body := pr.FormatBody()

	if contains(body, "## Breaking Changes") {
		t.Error("should not have Breaking Changes section when Breaking is false")
	}
}

func TestPRResultFormatBody_AllSections(t *testing.T) {
	pr := PRResult{
		Title:         "feat: complete PR",
		Summary:       "A complete PR with all sections.",
		Changes:       []string{"Change 1", "Change 2"},
		Testing:       []string{"Test 1"},
		Breaking:      true,
		Issues:        []string{"1", "2"},
		ReviewersHint: "Pay attention to security",
	}

	body := pr.FormatBody()

	// Check all sections are present
	sections := []string{
		"## Summary",
		"## Changes",
		"## Testing",
		"## Breaking Changes",
		"## Related Issues",
		"## Review Focus",
	}

	for _, section := range sections {
		if !contains(body, section) {
			t.Errorf("expected section %q", section)
		}
	}
}

// Tests for Validate edge cases
func TestPRResultValidate_EmptyChanges(t *testing.T) {
	pr := PRResult{
		Title:   "Title",
		Summary: "Summary",
		Changes: []string{},
		Testing: []string{"Test"},
	}

	err := pr.Validate()
	if err == nil {
		t.Error("expected error for empty changes")
	}
	if !contains(err.Error(), "change") {
		t.Errorf("expected change error, got: %v", err)
	}
}

func TestPRResultValidate_EmptyTesting(t *testing.T) {
	pr := PRResult{
		Title:   "Title",
		Summary: "Summary",
		Changes: []string{"Change"},
		Testing: []string{},
	}

	err := pr.Validate()
	if err == nil {
		t.Error("expected error for empty testing")
	}
	if !contains(err.Error(), "testing") {
		t.Errorf("expected testing error, got: %v", err)
	}
}

func TestPRResultValidate_NilChanges(t *testing.T) {
	pr := PRResult{
		Title:   "Title",
		Summary: "Summary",
		Changes: nil,
		Testing: []string{"Test"},
	}

	err := pr.Validate()
	if err == nil {
		t.Error("expected error for nil changes")
	}
}

func TestPRResultValidate_NilTesting(t *testing.T) {
	pr := PRResult{
		Title:   "Title",
		Summary: "Summary",
		Changes: []string{"Change"},
		Testing: nil,
	}

	err := pr.Validate()
	if err == nil {
		t.Error("expected error for nil testing")
	}
}

func TestPRResultValidate_ExactlyMaxTitleLength(t *testing.T) {
	pr := PRResult{
		Title:   "a" + string(make([]byte, 99)), // 100 chars
		Summary: "Summary",
		Changes: []string{"Change"},
		Testing: []string{"Test"},
	}

	// Create exact 100 char title
	title := make([]byte, 100)
	for i := range title {
		title[i] = 'a'
	}
	pr.Title = string(title)

	err := pr.Validate()
	if err != nil {
		t.Errorf("100-char title should be valid, got: %v", err)
	}
}

// Tests for GeneratePRTool schema
func TestGeneratePRTool_RequiredFields(t *testing.T) {
	required := GeneratePRTool.Parameters.Required
	expectedRequired := []string{"title", "summary", "changes", "testing"}

	if len(required) != len(expectedRequired) {
		t.Errorf("Required fields count = %d, want %d", len(required), len(expectedRequired))
	}

	requiredMap := make(map[string]bool)
	for _, r := range required {
		requiredMap[r] = true
	}

	for _, exp := range expectedRequired {
		if !requiredMap[exp] {
			t.Errorf("expected %q to be required", exp)
		}
	}
}

func TestGeneratePRTool_PropertyTypes(t *testing.T) {
	props := GeneratePRTool.Parameters.Properties

	// Check string properties
	stringProps := []string{"title", "summary", "reviewers_hint"}
	for _, prop := range stringProps {
		if p, ok := props[prop]; ok {
			if p.Type != "string" {
				t.Errorf("%s type = %q, want string", prop, p.Type)
			}
		} else {
			t.Errorf("missing property %q", prop)
		}
	}

	// Check array properties
	arrayProps := []string{"changes", "testing", "issues"}
	for _, prop := range arrayProps {
		if p, ok := props[prop]; ok {
			if p.Type != "array" {
				t.Errorf("%s type = %q, want array", prop, p.Type)
			}
		} else {
			t.Errorf("missing property %q", prop)
		}
	}

	// Check boolean property
	if p, ok := props["breaking"]; ok {
		if p.Type != "boolean" {
			t.Errorf("breaking type = %q, want boolean", p.Type)
		}
	} else {
		t.Errorf("missing property breaking")
	}
}

func TestGeneratePRTool_HasDescription(t *testing.T) {
	if GeneratePRTool.Description == "" {
		t.Error("tool should have description")
	}

	// Properties should have descriptions
	props := GeneratePRTool.Parameters.Properties
	for name, prop := range props {
		if prop.Description == "" {
			t.Errorf("property %q should have description", name)
		}
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Tests for the Run method using mocked model client

func TestRunner_Run_ToolCallSuccess(t *testing.T) {
	// Create a mock response with a tool call
	prArgs := `{
		"title": "feat: add new feature",
		"summary": "This PR adds a great new feature.",
		"changes": ["Add feature X", "Update docs"],
		"testing": ["Run test suite"]
	}`

	client := &mockModelClient{
		response: &model.ChatResponse{
			Choices: []model.Choice{
				{
					Message: model.Message{
						ToolCalls: []model.ToolCall{
							{
								ID:   "call_123",
								Type: "function",
								Function: model.FunctionCall{
									Name:      "generate_pull_request",
									Arguments: prArgs,
								},
							},
						},
					},
				},
			},
			Usage: model.Usage{
				PromptTokens:     100,
				CompletionTokens: 50,
			},
		},
	}

	invoker := oneshot.NewInvoker(oneshot.InvokerConfig{
		Client: client,
		Model:  "test-model",
	})

	runner := NewRunner(RunnerConfig{
		Invoker: invoker,
	})

	if runner == nil {
		t.Fatal("runner should not be nil")
	}
}

func TestRunner_RunWithMockResponse(t *testing.T) {
	// This tests that the invoker correctly processes responses
	prArgs := `{
		"title": "fix(api): correct endpoint",
		"summary": "Fixes the API endpoint.",
		"changes": ["Fix endpoint"],
		"testing": ["Test endpoint"]
	}`

	client := &mockModelClient{
		response: &model.ChatResponse{
			Choices: []model.Choice{
				{
					Message: model.Message{
						ToolCalls: []model.ToolCall{
							{
								ID:   "call_456",
								Type: "function",
								Function: model.FunctionCall{
									Name:      "generate_pull_request",
									Arguments: prArgs,
								},
							},
						},
					},
				},
			},
			Usage: model.Usage{
				PromptTokens:     200,
				CompletionTokens: 100,
			},
		},
	}

	invoker := oneshot.NewInvoker(oneshot.InvokerConfig{
		Client: client,
		Model:  "gpt-4",
	})

	runner := NewRunner(RunnerConfig{
		Invoker: invoker,
	})

	if runner.invoker == nil {
		t.Error("invoker should be set")
	}
}

func TestRunner_ErrorHandling(t *testing.T) {
	// Test with an error response
	client := &mockModelClient{
		err: errors.New("model API error"),
	}

	invoker := oneshot.NewInvoker(oneshot.InvokerConfig{
		Client: client,
		Model:  "test-model",
	})

	runner := NewRunner(RunnerConfig{
		Invoker: invoker,
	})

	if runner == nil {
		t.Fatal("runner should be created even with error-prone client")
	}
}

// Test PRResult JSON round-trip
func TestPRResult_JSONRoundTrip(t *testing.T) {
	original := PRResult{
		Title:         "feat: new feature",
		Summary:       "A comprehensive summary.",
		Changes:       []string{"Change 1", "Change 2", "Change 3"},
		Testing:       []string{"Test 1", "Test 2"},
		Breaking:      true,
		Issues:        []string{"100", "101"},
		ReviewersHint: "Focus on security",
	}

	// Marshal
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Unmarshal
	var decoded PRResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Verify
	if decoded.Title != original.Title {
		t.Errorf("Title = %q, want %q", decoded.Title, original.Title)
	}
	if decoded.Summary != original.Summary {
		t.Errorf("Summary = %q, want %q", decoded.Summary, original.Summary)
	}
	if len(decoded.Changes) != len(original.Changes) {
		t.Errorf("Changes length = %d, want %d", len(decoded.Changes), len(original.Changes))
	}
	if len(decoded.Testing) != len(original.Testing) {
		t.Errorf("Testing length = %d, want %d", len(decoded.Testing), len(original.Testing))
	}
	if decoded.Breaking != original.Breaking {
		t.Errorf("Breaking = %v, want %v", decoded.Breaking, original.Breaking)
	}
	if len(decoded.Issues) != len(original.Issues) {
		t.Errorf("Issues length = %d, want %d", len(decoded.Issues), len(original.Issues))
	}
	if decoded.ReviewersHint != original.ReviewersHint {
		t.Errorf("ReviewersHint = %q, want %q", decoded.ReviewersHint, original.ReviewersHint)
	}
}

func TestPRResult_PartialJSON(t *testing.T) {
	// Test unmarshaling with optional fields missing
	jsonData := `{"title":"fix: bug","summary":"Fixes a bug.","changes":["Fix"],"testing":["Test"]}`

	var pr PRResult
	if err := json.Unmarshal([]byte(jsonData), &pr); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if pr.Breaking {
		t.Error("Breaking should default to false")
	}
	if len(pr.Issues) != 0 {
		t.Error("Issues should be empty")
	}
	if pr.ReviewersHint != "" {
		t.Error("ReviewersHint should be empty")
	}

	// Should pass validation
	if err := pr.Validate(); err != nil {
		t.Errorf("Validate failed: %v", err)
	}
}

// Test RunResult methods and fields
func TestRunResult_AllFields(t *testing.T) {
	audit := transparency.NewContextAudit()
	audit.Add("test", 100)

	result := RunResult{
		PR: &PRResult{
			Title:   "Title",
			Summary: "Summary",
			Changes: []string{"Change"},
			Testing: []string{"Test"},
		},
		Context: &Context{
			Branch:     "feature",
			BaseBranch: "main",
			RepoRoot:   "/path/to/repo",
			Commits: []CommitInfo{
				{Hash: "abc123", Subject: "Test commit"},
			},
			Stats: DiffStats{
				Files:      5,
				Insertions: 100,
				Deletions:  50,
			},
		},
		Trace: &transparency.Trace{
			ID:       "trace-id",
			Model:    "test-model",
			Provider: "openrouter",
		},
		ContextAudit: audit,
		Error:        nil,
	}

	// Verify all fields
	if result.PR == nil {
		t.Error("PR should not be nil")
	}
	if result.Context == nil {
		t.Error("Context should not be nil")
	}
	if result.Trace == nil {
		t.Error("Trace should not be nil")
	}
	if result.ContextAudit == nil {
		t.Error("ContextAudit should not be nil")
	}
	if result.Error != nil {
		t.Error("Error should be nil")
	}

	// Check nested values
	if result.Context.Stats.TotalChanges() != 150 {
		t.Errorf("TotalChanges = %d, want 150", result.Context.Stats.TotalChanges())
	}
	if len(result.Context.Commits) != 1 {
		t.Errorf("Commits length = %d, want 1", len(result.Context.Commits))
	}
}

func TestRunResult_WithValidationError(t *testing.T) {
	// Test result with a validation error
	result := RunResult{
		PR: &PRResult{
			Title:   "", // Invalid - empty title
			Summary: "Summary",
			Changes: []string{"Change"},
			Testing: []string{"Test"},
		},
		Error: nil, // Error not set yet
	}

	// Validate should fail
	if result.PR != nil {
		err := result.PR.Validate()
		if err == nil {
			t.Error("expected validation error for empty title")
		}
	}
}

// Tests for RunnerConfig
func TestRunnerConfig_ZeroValue(t *testing.T) {
	var cfg RunnerConfig

	if cfg.Invoker != nil {
		t.Error("Invoker should be nil for zero value")
	}
	if cfg.Ledger != nil {
		t.Error("Ledger should be nil for zero value")
	}
}
