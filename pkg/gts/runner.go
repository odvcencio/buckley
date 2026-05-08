package gts

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
	ErrGTSTimeout = errors.New("gts: process timed out")
	ErrGTSOOM     = errors.New("gts: process killed (likely OOM)")
)

func IsTimeout(err error) bool { return errors.Is(err, ErrGTSTimeout) }
func IsOOM(err error) bool     { return errors.Is(err, ErrGTSOOM) }

// Runner executes gts-suite commands with memory limits and timeouts.
type Runner struct {
	binary     string
	workDir    string
	timeout    time.Duration
	memLimitMB int
}

// RunnerOption configures a Runner.
type RunnerOption func(*Runner)

// WithBinary sets the gts binary path.
func WithBinary(path string) RunnerOption {
	return func(r *Runner) { r.binary = path }
}

// WithWorkDir sets the working directory for gts commands.
func WithWorkDir(dir string) RunnerOption {
	return func(r *Runner) { r.workDir = dir }
}

// WithTimeout sets the maximum execution time.
func WithTimeout(d time.Duration) RunnerOption {
	return func(r *Runner) { r.timeout = d }
}

// WithMemLimit sets the virtual memory limit in megabytes.
func WithMemLimit(mb int) RunnerOption {
	return func(r *Runner) { r.memLimitMB = mb }
}

// NewRunner creates a Runner with the given options.
// Defaults: binary="gts", timeout=30s, memLimitMB=512.
func NewRunner(opts ...RunnerOption) *Runner {
	r := &Runner{
		binary:     "gts",
		timeout:    30 * time.Second,
		memLimitMB: 512,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Run executes a gts command with the configured guards.
// Output is capped at maxOutputBytes. On timeout, ErrGTSTimeout is returned.
// On kill signal (OOM), ErrGTSOOM is returned.
func (r *Runner) Run(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	shellCmd := r.buildShellCommand(args)
	cmd := exec.CommandContext(ctx, "sh", "-c", shellCmd)

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

func (r *Runner) buildShellCommand(args []string) string {
	var sb strings.Builder

	if r.memLimitMB > 0 {
		limitKB := r.memLimitMB * 1024
		fmt.Fprintf(&sb, "ulimit -v %d && ", limitKB)
	}

	sb.WriteString("exec ")
	sb.WriteString(shellQuote(r.binary))
	for _, arg := range args {
		sb.WriteByte(' ')
		sb.WriteString(shellQuote(arg))
	}
	return sb.String()
}

func (r *Runner) classifyError(ctx context.Context, err error, stderrOut []byte) error {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%w: %s", ErrGTSTimeout, string(stderrOut))
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && isOOMKill(exitErr) {
		return fmt.Errorf("%w: %s", ErrGTSOOM, string(stderrOut))
	}

	return fmt.Errorf("gts: %w: %s", err, string(stderrOut))
}

// shellQuote wraps s in single quotes, escaping any embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// limitWriter is an io.Writer that caps total bytes written.
// Go stdlib has no io.LimitWriter, so we provide one.
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
