package graft

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const pushTimeout = 60 * time.Second

// VCS wraps graft VCS operations with safety guards.
type VCS struct {
	runner *Runner
}

// NewVCS creates a VCS client using the given runner.
func NewVCS(runner *Runner) *VCS {
	return &VCS{runner: runner}
}

// Add stages files for commit.
func (v *VCS) Add(ctx context.Context, files ...string) error {
	if len(files) == 0 {
		return nil
	}
	args := append([]string{"add"}, files...)
	_, err := v.runner.Run(ctx, args...)
	if err != nil {
		return fmt.Errorf("staging files: %w", err)
	}
	return nil
}

// Commit creates a commit with the given message.
// Graft automatically triggers entity extraction and coordination updates.
func (v *VCS) Commit(ctx context.Context, message string) error {
	_, err := v.runner.Run(ctx, "commit", "-m", message)
	if err != nil {
		return fmt.Errorf("committing: %w", err)
	}
	return nil
}

// Push pushes to the remote with safety checks.
// Uses a longer timeout (60s) since push involves network I/O.
func (v *VCS) Push(ctx context.Context) error {
	// Use a dedicated timeout for push, overriding the runner default.
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()

	_, err := v.runner.Run(ctx, "push")
	if err != nil {
		return fmt.Errorf("pushing: %w", err)
	}
	return nil
}

// CurrentBranch returns the current branch name.
func (v *VCS) CurrentBranch(ctx context.Context) (string, error) {
	out, err := v.runner.Run(ctx, "branch", "--show-current")
	if err != nil {
		return "", fmt.Errorf("getting current branch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
