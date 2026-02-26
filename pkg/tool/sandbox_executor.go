package tool

import (
	"context"
	"time"
)

// SandboxExecutor defines the interface for OS-level sandbox execution.
// Implementations route shell commands to isolated environments (e.g. Docker containers).
type SandboxExecutor interface {
	Execute(ctx context.Context, req SandboxRequest) (*SandboxResult, error)
	Ready(ctx context.Context) error
	Close() error
}

// SandboxRequest describes a command to execute inside the sandbox.
type SandboxRequest struct {
	Command  string
	WorkDir  string
	Env      map[string]string
	Timeout  time.Duration
	ToolName string
}

// SandboxResult holds the output of a sandboxed command execution.
type SandboxResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
	Killed   bool
}
