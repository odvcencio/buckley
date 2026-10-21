// Package gitdiff captures native Git diffs without buffering their full bodies.
package gitdiff

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"m31labs.dev/buckley/internal/commandoutput"
	"m31labs.dev/buckley/internal/commandrun"
	"m31labs.dev/buckley/pkg/diffsignal"
)

// Capture drains git output into bounded file records. args contains Git's full
// argument list, including any global pathspec flags and the diff subcommand.
func Capture(ctx context.Context, workDir string, args ...string) (*diffsignal.Diff, error) {
	ctx, timeout := context.WithTimeout(ctx, time.Minute)
	defer timeout()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager"}, args...)...)
	cmd.Dir = workDir
	commandrun.ConfigureCancellation(cmd)
	var capture diffsignal.Capture
	cmd.Stdout = captureWriter{capture: &capture, cancel: cancel}
	stderr := commandoutput.NewCapture(2048)
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		_ = cmd.Cancel()
		return nil, fmt.Errorf("git diff: %s: %w", stderr.String(), errors.Join(context.Cause(ctx), err))
	}
	return capture.Finish()
}

type captureWriter struct {
	capture *diffsignal.Capture
	cancel  context.CancelCauseFunc
}

func (w captureWriter) Write(p []byte) (int, error) {
	n, err := w.capture.Write(p)
	if err != nil {
		w.cancel(err)
	}
	return n, err
}
