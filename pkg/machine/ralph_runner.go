package machine

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// CommitExecutor commits staged Git changes.
type CommitExecutor interface {
	Commit(ctx context.Context) (hash string, message string, err error)
}

// ShellExecutor runs shell commands for verification.
type ShellExecutor interface {
	Run(ctx context.Context, command string) (output string, err error)
}

// shellExec is a default ShellExecutor that runs commands via os/exec.
type shellExec struct {
	workDir string
}

// NewShellExecutor creates a ShellExecutor that runs commands in the given directory.
func NewShellExecutor(workDir string) ShellExecutor {
	return &shellExec{workDir: workDir}
}

func (s *shellExec) Run(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if s.workDir != "" {
		cmd.Dir = s.workDir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// executeCommit handles the CommitChanges action.
func (r *Runtime) executeCommit(ctx context.Context, _ CommitChanges) (Event, error) {
	if r.cfg.CommitExecutor == nil {
		return CommitCompleted{Hash: "none", Message: "no commit executor configured"}, nil
	}
	hash, message, err := r.cfg.CommitExecutor.Commit(ctx)
	if err != nil {
		return CommitCompleted{Hash: "", Message: fmt.Sprintf("commit failed: %v", err)}, nil
	}
	return CommitCompleted{Hash: hash, Message: message}, nil
}

// executeVerification handles the RunVerification action.
func (r *Runtime) executeVerification(ctx context.Context, act RunVerification) (Event, error) {
	if r.cfg.ShellExecutor == nil {
		return VerificationResult{Passed: false, Output: "no shell executor configured", Command: act.Command}, nil
	}
	output, err := r.cfg.ShellExecutor.Run(ctx, act.Command)
	passed := err == nil
	result := VerificationResult{
		Passed:  passed,
		Output:  strings.TrimSpace(output),
		Command: act.Command,
	}
	return result, nil
}

// executeResetContext handles the ResetContext action.
func (r *Runtime) executeResetContext(_ ResetContext) (Event, error) {
	// The ModelCaller adapter maintains conversation state. On ResetContext,
	// the runtime signals the machine to clear its conversation and re-inject
	// the spec + last error. The actual clearing happens in the model adapter
	// when it receives the ContextResetDone event.
	return ContextResetDone{Iteration: 0, LastError: ""}, nil
}
