package machine

import (
	"strings"
	"testing"
)

func TestRalphSpec_Validate(t *testing.T) {
	tests := []struct {
		name    string
		spec    RalphSpec
		wantErr bool
	}{
		{
			name:    "valid",
			spec:    RalphSpec{Goal: "fix auth bug", VerifyCommand: "go test ./..."},
			wantErr: false,
		},
		{
			name:    "missing goal",
			spec:    RalphSpec{VerifyCommand: "go test ./..."},
			wantErr: true,
		},
		{
			name:    "missing verify command",
			spec:    RalphSpec{Goal: "fix auth bug"},
			wantErr: true,
		},
		{
			name:    "empty strings",
			spec:    RalphSpec{Goal: "  ", VerifyCommand: " "},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRalphSpec_SystemPrompt(t *testing.T) {
	spec := RalphSpec{
		Goal:          "Fix the login flow",
		Files:         []string{"pkg/auth/login.go", "pkg/auth/login_test.go"},
		VerifyCommand: "go test ./pkg/auth/...",
	}

	prompt := spec.SystemPrompt(1, "")
	if !strings.Contains(prompt, "Fix the login flow") {
		t.Error("prompt should contain goal")
	}
	if !strings.Contains(prompt, "pkg/auth/login.go") {
		t.Error("prompt should contain file references")
	}
	if !strings.Contains(prompt, "go test ./pkg/auth/...") {
		t.Error("prompt should contain verify command")
	}
	if !strings.Contains(prompt, "iteration 1") {
		t.Error("prompt should contain iteration number")
	}

	// With last error
	promptErr := spec.SystemPrompt(2, "FAIL: TestLogin")
	if !strings.Contains(promptErr, "Previous Error") {
		t.Error("prompt should contain error section")
	}
	if !strings.Contains(promptErr, "FAIL: TestLogin") {
		t.Error("prompt should contain last error")
	}
	if !strings.Contains(promptErr, "iteration 2") {
		t.Error("prompt should show iteration 2")
	}
}

func TestParseRalphSpec(t *testing.T) {
	input := `
# Ralph spec for auth fix
goal: Fix the authentication timeout bug
files: pkg/auth/login.go, pkg/auth/session.go
verify: go test ./pkg/auth/... -v
max_iterations: 5
`
	spec, err := ParseRalphSpec(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if spec.Goal != "Fix the authentication timeout bug" {
		t.Errorf("Goal = %q", spec.Goal)
	}
	if len(spec.Files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(spec.Files))
	}
	if spec.Files[0] != "pkg/auth/login.go" {
		t.Errorf("Files[0] = %q", spec.Files[0])
	}
	if spec.Files[1] != "pkg/auth/session.go" {
		t.Errorf("Files[1] = %q", spec.Files[1])
	}
	if spec.VerifyCommand != "go test ./pkg/auth/... -v" {
		t.Errorf("VerifyCommand = %q", spec.VerifyCommand)
	}
	if spec.MaxIterations != 5 {
		t.Errorf("MaxIterations = %d", spec.MaxIterations)
	}
}

func TestParseRalphSpec_MissingGoal(t *testing.T) {
	input := `verify: go test ./...`
	_, err := ParseRalphSpec(input)
	if err == nil {
		t.Error("expected error for missing goal")
	}
}

func TestParseRalphSpec_MissingVerify(t *testing.T) {
	input := `goal: do something`
	_, err := ParseRalphSpec(input)
	if err == nil {
		t.Error("expected error for missing verify command")
	}
}
