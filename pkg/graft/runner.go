package graft

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const maxOutputBytes = 16 * 1024 * 1024 // 16MB

var (
	ErrGraftTimeout = errors.New("graft: process timed out")
	ErrGraftKilled  = errors.New("graft: process killed")
)

func IsTimeout(err error) bool { return errors.Is(err, ErrGraftTimeout) }
func IsKilled(err error) bool  { return errors.Is(err, ErrGraftKilled) }

// Runner executes graft commands with timeouts and process isolation.
type Runner struct {
	binary  string
	workDir string
	timeout time.Duration
}

// RunnerOption configures a Runner.
type RunnerOption func(*Runner)

// WithBinary sets the graft binary path.
func WithBinary(path string) RunnerOption {
	return func(r *Runner) { r.binary = path }
}

// WithWorkDir sets the working directory for graft commands.
func WithWorkDir(dir string) RunnerOption {
	return func(r *Runner) { r.workDir = dir }
}

// WithTimeout sets the maximum execution time.
func WithTimeout(d time.Duration) RunnerOption {
	return func(r *Runner) { r.timeout = d }
}

// NewRunner creates a Runner with the given options.
// Defaults: binary auto-detected via LookPath("graft"), timeout=30s.
func NewRunner(opts ...RunnerOption) *Runner {
	binary := "graft"
	if p, err := exec.LookPath("graft"); err == nil {
		binary = p
	}
	r := &Runner{
		binary:  binary,
		timeout: 30 * time.Second,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Run executes a graft command with the configured guards.
// Output is capped at maxOutputBytes. On timeout, ErrGraftTimeout is returned.
func (r *Runner) Run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.binary, args...)

	if r.workDir != "" {
		cmd.Dir = r.workDir
	}

	cmd.SysProcAttr = sysProcAttr()

	var stdout limitWriter
	stdout.limit = maxOutputBytes
	var stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return stdout.Bytes(), r.classifyError(ctx, err, stderr.Bytes())
	}
	return stdout.Bytes(), nil
}

func (r *Runner) classifyError(ctx context.Context, err error, stderrOut []byte) error {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%w: %s", ErrGraftTimeout, string(stderrOut))
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && isKillSignal(exitErr) {
		return fmt.Errorf("%w: %s", ErrGraftKilled, string(stderrOut))
	}

	return fmt.Errorf("graft: %w: %s", err, string(stderrOut))
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// limitWriter is an io.Writer that caps total bytes written.
type limitWriter struct {
	buf   bytes.Buffer
	limit int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.buf.Len()
	if remaining <= 0 {
		return len(p), nil // discard but report success
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	return w.buf.Write(p)
}

func (w *limitWriter) Bytes() []byte {
	return w.buf.Bytes()
}

var _ io.Writer = (*limitWriter)(nil)
