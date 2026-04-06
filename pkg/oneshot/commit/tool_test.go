package commit

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/tools"
)

func TestGenerateCommitToolRegistered(t *testing.T) {
	def, ok := tools.Get("generate_commit")
	if !ok {
		t.Fatal("expected generate_commit to be registered")
	}

	if def.Name != "generate_commit" {
		t.Errorf("expected name 'generate_commit', got %q", def.Name)
	}
	if def.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestGenerateCommitToolSchema(t *testing.T) {
	if GenerateCommitTool.Parameters.Type != "object" {
		t.Errorf("expected object type, got %q", GenerateCommitTool.Parameters.Type)
	}

	props := GenerateCommitTool.Parameters.Properties
	if props == nil {
		t.Fatal("expected properties to be set")
	}

	// Check required fields exist
	requiredFields := []string{"action", "subject", "body"}
	for _, field := range requiredFields {
		if _, ok := props[field]; !ok {
			t.Errorf("expected required field %q", field)
		}
	}

	// Check action has enum
	action := props["action"]
	if len(action.Enum) == 0 {
		t.Error("expected action to have enum values")
	}

	// Check body is array
	body := props["body"]
	if body.Type != "array" {
		t.Errorf("expected body type 'array', got %q", body.Type)
	}
}

func TestCommitResultHeader(t *testing.T) {
	tests := []struct {
		name   string
		result CommitResult
		want   string
	}{
		{
			name: "with scope",
			result: CommitResult{
				Action:  "add",
				Scope:   "api",
				Subject: "user authentication",
			},
			want: "add(api): user authentication",
		},
		{
			name: "without scope",
			result: CommitResult{
				Action:  "fix",
				Subject: "null pointer in handler",
			},
			want: "fix: null pointer in handler",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.result.Header()
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCommitResultFormat(t *testing.T) {
	result := CommitResult{
		Action:  "refactor",
		Scope:   "commit",
		Subject: "use tool-based generation",
		Body: []string{
			"Remove regex parsing",
			"Add structured tool calls",
		},
		Breaking: false,
	}

	formatted := result.Format()

	if formatted == "" {
		t.Fatal("expected non-empty formatted message")
	}

	// Check header is present
	if !contains(formatted, "refactor(commit): use tool-based generation") {
		t.Error("expected header in formatted message")
	}

	// Check bullets are present
	if !contains(formatted, "- Remove regex parsing") {
		t.Error("expected first bullet in formatted message")
	}
	if !contains(formatted, "- Add structured tool calls") {
		t.Error("expected second bullet in formatted message")
	}
}

func TestCommitResultFormatWithBreaking(t *testing.T) {
	result := CommitResult{
		Action:   "remove",
		Subject:  "deprecated API endpoints",
		Body:     []string{"Remove v1 endpoints"},
		Breaking: true,
	}

	formatted := result.Format()

	if !contains(formatted, "BREAKING CHANGE") {
		t.Error("expected BREAKING CHANGE in formatted message")
	}
}

func TestCommitResultFormatWithIssues(t *testing.T) {
	result := CommitResult{
		Action:  "fix",
		Subject: "memory leak",
		Body:    []string{"Fix leak in connection pool"},
		Issues:  []string{"123", "456"},
	}

	formatted := result.Format()

	if !contains(formatted, "Closes #123") {
		t.Error("expected 'Closes #123' in formatted message")
	}
	if !contains(formatted, "Closes #456") {
		t.Error("expected 'Closes #456' in formatted message")
	}
}

func TestCommitResultValidate(t *testing.T) {
	tests := []struct {
		name    string
		result  CommitResult
		wantErr bool
	}{
		{
			name: "valid",
			result: CommitResult{
				Action:  "add",
				Subject: "feature",
				Body:    []string{"Add new feature"},
			},
			wantErr: false,
		},
		{
			name: "missing action",
			result: CommitResult{
				Subject: "feature",
				Body:    []string{"Add new feature"},
			},
			wantErr: true,
		},
		{
			name: "missing subject",
			result: CommitResult{
				Action: "add",
				Body:   []string{"Add new feature"},
			},
			wantErr: true,
		},
		{
			name: "missing body",
			result: CommitResult{
				Action:  "add",
				Subject: "feature",
			},
			wantErr: true,
		},
		{
			name: "empty body",
			result: CommitResult{
				Action:  "add",
				Subject: "feature",
				Body:    []string{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.result.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidationError_Error(t *testing.T) {
	tests := []struct {
		name    string
		err     *ValidationError
		wantMsg string
	}{
		{
			name:    "action error",
			err:     &ValidationError{Field: "action", Message: "action is required"},
			wantMsg: "action: action is required",
		},
		{
			name:    "subject error",
			err:     &ValidationError{Field: "subject", Message: "subject is required"},
			wantMsg: "subject: subject is required",
		},
		{
			name:    "body error",
			err:     &ValidationError{Field: "body", Message: "body requires at least one bullet"},
			wantMsg: "body: body requires at least one bullet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
		})
	}
}

func TestCommitActions(t *testing.T) {
	// Verify CommitActions contains expected verbs
	expectedActions := []string{
		"add", "fix", "update", "refactor", "remove",
		"improve", "rename", "move", "revert", "merge",
		"bump", "release", "format", "optimize", "simplify",
		"extract", "inline", "document", "test", "build", "ci",
	}

	for _, expected := range expectedActions {
		found := false
		for _, action := range CommitActions {
			if action == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected CommitActions to contain %q", expected)
		}
	}
}

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
