package builtin

import "context"

// WorkspaceVerificationTool runs foreground checks through the session's
// configured shell, including its sandbox, work directory, and time limits.
// The check runs in the live workspace, so it sees every edit made so far.
type WorkspaceVerificationTool struct {
	*ShellCommandTool
}

func NewWorkspaceVerificationTool(shell *ShellCommandTool) *WorkspaceVerificationTool {
	return &WorkspaceVerificationTool{ShellCommandTool: shell}
}

func (t *WorkspaceVerificationTool) Name() string { return "run_verification" }
func (t *WorkspaceVerificationTool) Description() string {
	return "Run one build, test, vet, or lint command in the current workspace, with your latest edits in place, and record its exit status as verification. Call it after your last change. " +
		VerificationCommandHelp() +
		" Other commands are not verification: use run_shell for them. If the workspace has no test or build command, do not call this tool; say in your final answer that the change is unverified and why."
}
func (t *WorkspaceVerificationTool) TrustedVerification() bool { return true }
func (t *WorkspaceVerificationTool) Parameters() ParameterSchema {
	schema := t.ShellCommandTool.Parameters()
	delete(schema.Properties, "interactive")
	command := schema.Properties["command"]
	command.Description = "One accepted check in plain words, for example `go test ./...`, `go vet ./...`, `make check`, `npm test`, `cargo test`, or `pytest`. See the tool description for the full list."
	schema.Properties["command"] = command
	return schema
}
func (t *WorkspaceVerificationTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *WorkspaceVerificationTool) ExecuteWithContext(ctx context.Context, params map[string]any) (*Result, error) {
	command, _ := params["command"].(string)
	if parseBoolParam(params["interactive"], false) {
		return rejectedVerification("an interactive terminal is not a foreground check"), nil
	}
	if reason := VerificationCommandRejection(command); reason != "" {
		return rejectedVerification(reason), nil
	}
	return t.ShellCommandTool.ExecuteWithContext(ctx, params)
}

// rejectedVerification reports a call refused before launch. It is marked as
// not run, so the completion contract records an unavailable check instead of
// a failed one, and the message says how to fix the call.
func rejectedVerification(reason string) *Result {
	return &Result{
		Success: false,
		Error:   "run_verification did not run and nothing was executed: " + reason + ". This is not a test failure. " + VerificationCommandHelp(),
		Data:    verificationNotRun(VerificationStatusRejected, reason),
	}
}
