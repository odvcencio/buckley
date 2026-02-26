package dockersandbox

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
)

// Request describes a command to execute inside the sandbox.
type Request struct {
	Command  string
	WorkDir  string
	Env      map[string]string
	Timeout  time.Duration
	ToolName string
}

// Result holds the output of a sandboxed command execution.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
	Killed   bool
}

// Option configures a DockerSandbox.
type Option func(*DockerSandbox)

// WithWorkspacePath sets the host workspace path for bind mounting.
func WithWorkspacePath(path string) Option {
	return func(s *DockerSandbox) {
		s.workspacePath = path
	}
}

// WithCommandRunner overrides the command execution function (for testing).
func WithCommandRunner(runner CommandRunner) Option {
	return func(s *DockerSandbox) {
		s.runner = runner
	}
}

// CommandRunner abstracts command execution for testability.
type CommandRunner func(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)

// DockerSandbox implements OS-level sandbox execution using Docker containers.
type DockerSandbox struct {
	cfg           config.DockerSandboxConfig
	workspacePath string
	runner        CommandRunner

	mu            sync.Mutex
	containerName string
	containerID   string
	idleTimer     *time.Timer
	closed        bool
}

// New creates a new DockerSandbox.
func New(cfg config.DockerSandboxConfig, opts ...Option) *DockerSandbox {
	s := &DockerSandbox{
		cfg:    cfg,
		runner: defaultRunner,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func defaultRunner(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// Ready verifies that the Docker daemon is available.
func (s *DockerSandbox) Ready(ctx context.Context) error {
	_, _, err := s.runner(ctx, "docker", "info")
	if err != nil {
		return fmt.Errorf("docker not available: %w", err)
	}
	return nil
}

// Execute runs a command inside the sandboxed Docker container.
func (s *DockerSandbox) Execute(ctx context.Context, req Request) (*Result, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("sandbox is closed")
	}

	if err := s.ensureContainerLocked(ctx); err != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("ensuring container: %w", err)
	}
	containerID := s.containerID
	s.resetIdleTimerLocked()
	s.mu.Unlock()

	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	start := time.Now()

	execArgs := []string{"exec"}
	workDir := req.WorkDir
	if workDir == "" {
		mount := s.cfg.WorkspaceMount
		if mount == "" {
			mount = "/workspace"
		}
		workDir = mount
	}
	execArgs = append(execArgs, "-w", workDir)

	for k, v := range req.Env {
		if isDangerousEnvVar(k) {
			continue
		}
		execArgs = append(execArgs, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	execArgs = append(execArgs, containerID, "sh", "-c", req.Command)

	stdout, stderr, err := s.runner(ctx, "docker", execArgs...)
	duration := time.Since(start)

	result := &Result{
		Stdout:   stdout,
		Stderr:   stderr,
		Duration: duration,
	}

	if err != nil {
		if ctx.Err() != nil {
			result.Killed = true
			result.ExitCode = 137
			return result, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("docker exec: %w", err)
	}

	return result, nil
}

// Close stops and removes the container.
func (s *DockerSandbox) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}

	if s.containerID == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, _, _ = s.runner(ctx, "docker", "rm", "-f", s.containerID)
	s.containerID = ""
	s.containerName = ""
	return nil
}

func (s *DockerSandbox) ensureContainerLocked(ctx context.Context) error {
	if s.containerID != "" {
		// Check if container is still running
		stdout, _, err := s.runner(ctx, "docker", "inspect", "-f", "{{.State.Running}}", s.containerID)
		if err == nil && strings.TrimSpace(stdout) == "true" {
			return nil
		}
		// Container is gone, clean up
		cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, _ = s.runner(cleanCtx, "docker", "rm", "-f", s.containerID)
		s.containerID = ""
	}

	name := fmt.Sprintf("buckley-sandbox-%d", time.Now().UnixNano())
	s.containerName = name

	args := buildCreateArgs(s.cfg, s.workspacePath, name)
	stdout, stderr, err := s.runner(ctx, "docker", args...)
	if err != nil {
		return fmt.Errorf("docker create: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	containerID := strings.TrimSpace(stdout)
	if containerID == "" {
		return fmt.Errorf("docker create returned empty container ID")
	}

	_, stderr, err = s.runner(ctx, "docker", "start", containerID)
	if err != nil {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, _ = s.runner(cleanCtx, "docker", "rm", "-f", containerID)
		return fmt.Errorf("docker start: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	s.containerID = containerID
	return nil
}

// isDangerousEnvVar returns true for environment variables that could be used
// to escape sandbox restrictions.
func isDangerousEnvVar(key string) bool {
	upper := strings.ToUpper(key)
	for _, dangerous := range []string{
		"LD_PRELOAD", "LD_LIBRARY_PATH", "LD_AUDIT",
		"PATH", "HOME", "SHELL",
		"BASH_ENV", "ENV", "CDPATH",
		"PYTHONSTARTUP", "PERL5OPT", "RUBYOPT",
		"NODE_OPTIONS", "JAVA_TOOL_OPTIONS",
	} {
		if upper == dangerous {
			return true
		}
	}
	return false
}

func (s *DockerSandbox) resetIdleTimerLocked() {
	if !s.cfg.KeepAlive {
		return
	}
	timeout := s.cfg.KeepAliveTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	if s.idleTimer != nil {
		s.idleTimer.Stop()
	}
	s.idleTimer = time.AfterFunc(timeout, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.containerID == "" || s.closed {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _, _ = s.runner(ctx, "docker", "rm", "-f", s.containerID)
		s.containerID = ""
	})
}
