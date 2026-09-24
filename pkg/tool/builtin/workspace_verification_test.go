package builtin

import (
	"context"
	"testing"
)

func TestIsVerificationCommand_AcceptedAndRejected(t *testing.T) {
	for _, command := range []string{"go test ./...", "GOWORK=off go test ./pkg/... -count=1", "go vet ./...", "go build ./...", "make test", "make check", "npm test", "npm run build", "cargo test", "cargo check", "pytest -q", "python3 -m pytest", "go test -v ./..."} {
		if !IsVerificationCommand(command) {
			t.Errorf("rejected %q", command)
		}
	}
	for _, command := range []string{"make", "go test -n ./...", "go build -n ./...", "pytest --co", "pytest -V", "npm test -v", "true", "echo go test", "printf passed", "false", "go test ./... || true", "go test ./...; echo ok", "go test ./... &", "go test ./...\necho ok", "go test $(echo ./...)", "go test --help", "go test -exec true ./...", "make test -n", "make check -qn", "make check --dry-run", "make test SHELL=true", "npm test --ignore-scripts", "npm test --if-present", "pytest --collect-only", "cargo test --version", "cd src && go test ./..."} {
		if IsVerificationCommand(command) {
			t.Errorf("accepted %q", command)
		}
	}
}

func TestWorkspaceVerificationTool_RejectsNoOpAndInteractive(t *testing.T) {
	tool := NewWorkspaceVerificationTool(&ShellCommandTool{})
	for _, params := range []map[string]any{{"command": "true"}, {"command": "go test ./...", "interactive": true}} {
		result, err := tool.ExecuteWithContext(context.Background(), params)
		if err != nil || result.Success || result.Error == "" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}
