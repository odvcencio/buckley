package builtin

import "context"

// WorkspaceVerificationTool runs foreground checks through the session's
// configured shell, including its sandbox, work directory, and time limits.
type WorkspaceVerificationTool struct {
	*ShellCommandTool
}

func NewWorkspaceVerificationTool(shell *ShellCommandTool) *WorkspaceVerificationTool {
	return &WorkspaceVerificationTool{ShellCommandTool: shell}
}

func (t *WorkspaceVerificationTool) Name() string { return "run_verification" }
func (t *WorkspaceVerificationTool) Description() string {
	return "Run a test or build command in the current workspace and record its exit status as verification. Supply command, for example go test ./..., go vet ./..., make test, make check, npm test, cargo test, or pytest. Use one command without shell operators."
}
func (t *WorkspaceVerificationTool) TrustedVerification() bool { return true }
func (t *WorkspaceVerificationTool) Parameters() ParameterSchema {
	schema := t.ShellCommandTool.Parameters()
	delete(schema.Properties, "interactive")
	return schema
}
func (t *WorkspaceVerificationTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *WorkspaceVerificationTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	if !IsVerificationCall(params) {
		return &Result{Error: "use one foreground test or build command, such as go test ./..., make check, npm test, cargo test, or pytest; no shell operators, no-op commands, or dry runs"}, nil
	}
	return t.ShellCommandTool.ExecuteWithContext(ctx, params)
}
