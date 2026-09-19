package dockersandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"m31labs.dev/buckley/pkg/config"
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
	ExitCode        int
	Stdout          string
	Stderr          string
	Duration        time.Duration
	Killed          bool
	OutputTruncated bool
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

// WithLaunchAdmission seals the sandbox to one explicitly prepared container.
// A sealed container is never recreated after death and cannot execute until
// VerifyPrepared succeeds.
func WithLaunchAdmission() Option {
	return func(s *DockerSandbox) {
		s.launchAdmission = true
	}
}

// CommandRunner abstracts command execution for testability.
type CommandRunner func(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)

// DockerSandbox implements OS-level sandbox execution using Docker containers.
type DockerSandbox struct {
	cfg           config.DockerSandboxConfig
	workspacePath string
	runner        CommandRunner

	mu               sync.Mutex
	execMu           sync.Mutex
	containerName    string
	containerID      string
	containerOwner   string
	preparedID       string
	launchImageID    string
	launchSupervisor string
	admitted         bool
	launchAdmission  bool
	idleTimer        *time.Timer
	closed           bool
}

// ImageIdentity is the bounded subset of Docker image inspection used by
// launch admission.
type ImageIdentity struct {
	ID           string
	RepoDigests  []string
	OS           string
	Architecture string
	Labels       map[string]string
	Env          []string
	Entrypoint   []string
	Cmd          []string
}

// PreparedVerification binds a prepared launch container to the admitted
// image and the retained host workspace directory identity.
type PreparedVerification struct {
	ImageID           string
	WorkspaceIdentity string
	ProbePath         string
	// SupervisorPath is a digest-bound executable whose contract requires it
	// to reap the complete descendant group before returning.
	SupervisorPath  string
	SupervisorToken string
}

var dockerContainerIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var dockerContainerNamePattern = regexp.MustCompile(`^buckley-sandbox-[0-9a-f]{1,32}$`)

const launchOwnerLabel = "dev.m31labs.buckley.launch.owner"

var ErrCleanupRequired = errors.New("sandbox cleanup required")

// CleanupRequiredError safely identifies the one strict container whose
// removal must be retried. Container is either a validated daemon ID or the
// generated launch name; it never contains a workspace path or command body.
type CleanupRequiredError struct{ Container string }

func (e *CleanupRequiredError) Error() string {
	if e == nil || e.Container == "" {
		return ErrCleanupRequired.Error()
	}
	return ErrCleanupRequired.Error() + ": " + e.Container
}

func (*CleanupRequiredError) Unwrap() error { return ErrCleanupRequired }

// New creates a new DockerSandbox.
func New(cfg config.DockerSandboxConfig, opts ...Option) *DockerSandbox {
	s := &DockerSandbox{
		cfg: cfg,
	}
	s.runner = boundedCommandRunner(cfg.MaxOutputBytes, cfg.IsolatedClientEnv)
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func defaultRunner(ctx context.Context, name string, args ...string) (string, string, error) {
	return boundedCommandRunner(0, false)(ctx, name, args...)
}

func boundedCommandRunner(maxOutputBytes int64, isolated bool) CommandRunner {
	return func(ctx context.Context, name string, args ...string) (string, string, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		if isolated {
			cmd.Env = []string{
				"DOCKER_CONFIG=/nonexistent",
				"DOCKER_HOST=unix:///var/run/docker.sock",
				"HOME=/nonexistent",
				"LC_ALL=C",
				"PATH=/usr/bin:/bin",
			}
		}
		stdoutLimit, stderrLimit := splitOutputLimit(maxOutputBytes)
		stdout := newBoundedCapture(stdoutLimit)
		stderr := newBoundedCapture(stderrLimit)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
}

// Ready verifies that the Docker daemon is available.
func (s *DockerSandbox) Ready(ctx context.Context) error {
	_, _, err := s.runner(ctx, s.binary(), "info")
	if err != nil {
		return fmt.Errorf("docker not available: %w", err)
	}
	return nil
}

// ImageReady verifies the configured image is already present locally. It is
// used by strict launch admission so command execution can never trigger a
// network pull.
func (s *DockerSandbox) ImageReady(ctx context.Context) error {
	image := strings.TrimSpace(s.cfg.Image)
	if image == "" {
		return fmt.Errorf("sandbox image is required")
	}
	_, _, err := s.runner(ctx, s.binary(), "image", "inspect", image)
	if err != nil {
		return fmt.Errorf("sandbox image unavailable: %w", err)
	}
	return nil
}

// InspectImage returns only the immutable identity fields needed by strict
// launch admission. The configured output cap bounds the JSON payload.
func (s *DockerSandbox) InspectImage(ctx context.Context) (ImageIdentity, error) {
	image := strings.TrimSpace(s.cfg.Image)
	if image == "" {
		return ImageIdentity{}, fmt.Errorf("sandbox image is required")
	}
	stdout, _, err := s.runner(ctx, s.binary(), "image", "inspect", image)
	if err != nil {
		return ImageIdentity{}, fmt.Errorf("sandbox image unavailable: %w", err)
	}
	var raw []struct {
		ID           string   `json:"Id"`
		RepoDigests  []string `json:"RepoDigests"`
		OS           string   `json:"Os"`
		Architecture string   `json:"Architecture"`
		Config       struct {
			Labels     map[string]string `json:"Labels"`
			Env        []string          `json:"Env"`
			Entrypoint []string          `json:"Entrypoint"`
			Cmd        []string          `json:"Cmd"`
		} `json:"Config"`
	}
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil || len(raw) != 1 {
		return ImageIdentity{}, fmt.Errorf("sandbox image identity is invalid")
	}
	identity := ImageIdentity{
		ID:           raw[0].ID,
		RepoDigests:  append([]string(nil), raw[0].RepoDigests...),
		OS:           raw[0].OS,
		Architecture: raw[0].Architecture,
		Labels:       make(map[string]string, len(raw[0].Config.Labels)),
		Env:          append([]string(nil), raw[0].Config.Env...),
		Entrypoint:   append([]string(nil), raw[0].Config.Entrypoint...),
		Cmd:          append([]string(nil), raw[0].Config.Cmd...),
	}
	for key, value := range raw[0].Config.Labels {
		identity.Labels[key] = value
	}
	if s.launchAdmission && strings.HasPrefix(identity.ID, "sha256:") {
		s.mu.Lock()
		s.launchImageID = identity.ID
		s.mu.Unlock()
	}
	return identity, nil
}

// Prepare creates and starts the hardened container so bind-mount admission is
// proven before a strict launch registry can be published.
func (s *DockerSandbox) Prepare(ctx context.Context) error {
	if s.cfg.StrictCleanup {
		s.execMu.Lock()
		defer s.execMu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("sandbox is closed")
	}
	if s.launchAdmission {
		if s.preparedID != "" {
			return s.checkPreparedContainerLocked(ctx)
		}
		if s.containerID != "" {
			return fmt.Errorf("launch container cleanup is pending")
		}
		if err := s.createContainerLocked(ctx); err != nil {
			return fmt.Errorf("preparing sandbox: %w", err)
		}
		s.preparedID = s.containerID
		return nil
	}
	if err := s.ensureContainerLocked(ctx); err != nil {
		return fmt.Errorf("preparing sandbox: %w", err)
	}
	return nil
}

// VerifyPrepared admits the one prepared launch container. It verifies the
// container's immutable image ID and invokes the digest-bound fixed probe to
// compare the mounted workspace's device/inode identity with the retained host
// anchor.
func (s *DockerSandbox) VerifyPrepared(ctx context.Context, expected PreparedVerification) error {
	if !s.launchAdmission {
		return fmt.Errorf("launch admission is not enabled")
	}
	s.execMu.Lock()
	defer s.execMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.preparedID == "" || s.containerID != s.preparedID {
		return fmt.Errorf("prepared launch container is unavailable")
	}
	if err := s.checkPreparedContainerLocked(ctx); err != nil {
		return err
	}
	imageID, _, err := s.runner(ctx, s.binary(), "inspect", "-f", "{{.Image}}", "--", s.preparedID)
	if err != nil || strings.TrimSpace(imageID) != expected.ImageID {
		return fmt.Errorf("prepared launch image identity mismatch")
	}
	s.launchImageID = expected.ImageID
	probe, _, err := s.runner(ctx, s.binary(), "exec", "--", s.preparedID, expected.ProbePath, "/workspace")
	if err != nil || strings.TrimSpace(probe) != expected.WorkspaceIdentity {
		return fmt.Errorf("prepared launch workspace identity mismatch")
	}
	if !validLaunchExecutable(expected.SupervisorPath) || expected.SupervisorToken == "" || len(expected.SupervisorToken) > 128 {
		return fmt.Errorf("prepared launch supervisor contract is invalid")
	}
	supervisor, _, err := s.runner(ctx, s.binary(), "exec", "--", s.preparedID, expected.SupervisorPath, "--self-test")
	if err != nil || strings.TrimSpace(supervisor) != expected.SupervisorToken {
		return fmt.Errorf("prepared launch supervisor is unavailable")
	}
	s.launchSupervisor = expected.SupervisorPath
	s.admitted = true
	return nil
}

// Execute runs a command inside the sandboxed Docker container.
func (s *DockerSandbox) Execute(ctx context.Context, req Request) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	if s.cfg.StrictCleanup {
		s.execMu.Lock()
		defer s.execMu.Unlock()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("sandbox is closed")
	}

	var ensureErr error
	if s.launchAdmission {
		if !s.admitted {
			ensureErr = fmt.Errorf("launch container is not admitted")
		} else {
			ensureErr = s.checkPreparedContainerLocked(ctx)
		}
	} else {
		ensureErr = s.ensureContainerLocked(ctx)
	}
	if ensureErr != nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("ensuring container: %w", ensureErr)
	}
	containerID := s.containerID
	s.resetIdleTimerLocked()
	s.mu.Unlock()

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

	if s.launchAdmission {
		execArgs = append(execArgs, "--", containerID, s.launchSupervisor, "--", "/bin/sh", "-c", req.Command)
	} else {
		execArgs = append(execArgs, "--", containerID, "sh", "-c", req.Command)
	}

	stdout, stderr, err := s.runner(ctx, s.binary(), execArgs...)
	duration := time.Since(start)

	result := &Result{
		Duration: duration,
	}
	result.Stdout, result.Stderr, result.OutputTruncated = boundReturnedOutput(stdout, stderr, s.cfg.MaxOutputBytes)

	if err != nil {
		if ctx.Err() != nil {
			result.Killed = true
			result.ExitCode = 137
			if s.launchAdmission {
				s.mu.Lock()
				if s.containerID == containerID {
					s.admitted = false
				}
				s.mu.Unlock()
			}
			if cleanupErr := s.removeContainer(containerID); cleanupErr != nil && s.cfg.StrictCleanup {
				return result, fmt.Errorf("command timed out and sandbox cleanup failed")
			}
			return result, nil
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
			if s.launchAdmission && result.ExitCode == launchSupervisorFailureExitCode {
				s.mu.Lock()
				if s.containerID == containerID {
					s.admitted = false
				}
				s.mu.Unlock()
				if cleanupErr := s.removeContainer(containerID); cleanupErr != nil && s.cfg.StrictCleanup {
					return result, fmt.Errorf("launch supervisor failed and sandbox cleanup failed")
				}
				return result, fmt.Errorf("launch supervisor failed")
			}
			return result, nil
		}
		return result, fmt.Errorf("docker exec: %w", err)
	}

	return result, nil
}

const (
	outputTruncatedMarker           = "\n...[output truncated]"
	launchSupervisorFailureExitCode = 125
)

type boundedCapture struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func newBoundedCapture(limit int) boundedCapture { return boundedCapture{limit: limit} }

func (b *boundedCapture) Write(p []byte) (int, error) {
	written := len(p)
	if b == nil || b.limit <= 0 {
		if b == nil {
			return written, nil
		}
		_, _ = b.Buffer.Write(p)
		return written, nil
	}
	remaining := b.limit - b.Buffer.Len()
	if remaining > 0 {
		visible := p
		if len(visible) > remaining {
			visible = visible[:remaining]
		}
		_, _ = b.Buffer.Write(visible)
	}
	if len(p) > remaining {
		b.truncated = true
	}
	return written, nil
}

func (b *boundedCapture) String() string {
	if b == nil {
		return ""
	}
	return markedOutput(b.Buffer.String(), b.limit, b.truncated)
}

func splitOutputLimit(max int64) (int, int) {
	if max <= 0 {
		return 0, 0
	}
	maxInt := int64(^uint(0) >> 1)
	if max > maxInt {
		max = maxInt
	}
	stdout := int((max + 1) / 2)
	return stdout, int(max) - stdout
}

func boundReturnedOutput(stdout, stderr string, max int64) (string, string, bool) {
	stdoutLimit, stderrLimit := splitOutputLimit(max)
	if stdoutLimit == 0 && stderrLimit == 0 {
		return stdout, stderr, false
	}
	stdoutAlreadyTruncated := strings.HasSuffix(stdout, outputTruncatedMarker)
	stderrAlreadyTruncated := strings.HasSuffix(stderr, outputTruncatedMarker)
	stdout, stdoutTruncated := truncateOutput(stdout, stdoutLimit)
	stderr, stderrTruncated := truncateOutput(stderr, stderrLimit)
	stdoutTruncated = stdoutTruncated || stdoutAlreadyTruncated
	stderrTruncated = stderrTruncated || stderrAlreadyTruncated
	return stdout, stderr, stdoutTruncated || stderrTruncated
}

func truncateOutput(value string, limit int) (string, bool) {
	if limit <= 0 {
		return "", value != ""
	}
	if len(value) <= limit {
		return value, false
	}
	return markedOutput(value[:limit], limit, true), true
}

func markedOutput(value string, limit int, truncated bool) string {
	if !truncated || limit <= 0 {
		return value
	}
	marker := outputTruncatedMarker
	if len(marker) >= limit {
		return marker[:limit]
	}
	if len(value) > limit-len(marker) {
		value = value[:limit-len(marker)]
	}
	return value + marker
}

// Close stops and removes the container.
func (s *DockerSandbox) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.CloseContext(ctx)
}

// CloseContext removes the exact retained container identity under a caller
// supplied cleanup deadline. Failed strict removal preserves that identity for
// a later retry.
func (s *DockerSandbox) CloseContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.cfg.StrictCleanup {
		s.execMu.Lock()
		defer s.execMu.Unlock()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true
	if s.idleTimer != nil {
		s.idleTimer.Stop()
		s.idleTimer = nil
	}

	if s.containerID == "" && s.containerName == "" {
		return nil
	}
	if s.launchAdmission {
		if err := s.removeLaunchContainerLocked(ctx); err != nil {
			return err
		}
		return nil
	}

	target := s.containerID
	if target == "" {
		target = s.containerName
	}
	_, _, err := s.runner(ctx, s.binary(), "rm", "-f", "--", target)
	if err != nil && s.cfg.StrictCleanup {
		if s.launchAdmission {
			return &CleanupRequiredError{Container: safeCleanupTarget(target)}
		}
		return fmt.Errorf("removing sandbox container: %w", err)
	}
	s.clearContainerIdentityLocked()
	return nil
}

func (s *DockerSandbox) clearContainerIdentityLocked() {
	s.containerID = ""
	s.containerName = ""
	s.containerOwner = ""
	s.preparedID = ""
	s.launchImageID = ""
	s.launchSupervisor = ""
	s.admitted = false
}

func safeCleanupTarget(value string) string {
	if dockerContainerIDPattern.MatchString(value) || dockerContainerNamePattern.MatchString(value) {
		return value
	}
	return ""
}

const launchOwnerInspectTemplate = `{{.Id}}|{{.Image}}|{{.Config.Image}}|{{index .Config.Labels "` + launchOwnerLabel + `"}}`

func (s *DockerSandbox) removeLaunchContainerLocked(ctx context.Context) error {
	if s.containerID == "" {
		if s.containerName == "" || s.containerOwner == "" {
			return &CleanupRequiredError{}
		}
		id, owned, err := s.inspectOwnedLaunchNameLocked(ctx)
		if err != nil {
			return &CleanupRequiredError{Container: safeCleanupTarget(s.containerName)}
		}
		if !owned {
			s.clearContainerIdentityLocked()
			return nil
		}
		s.containerID = id
	}

	target := s.containerID
	_, _, removeErr := s.runner(ctx, s.binary(), "rm", "-f", "--", target)
	if removeErr == nil {
		s.clearContainerIdentityLocked()
		return nil
	}
	absent, inspectErr := s.launchContainerAbsent(ctx, target, false)
	if inspectErr == nil && absent {
		s.clearContainerIdentityLocked()
		return nil
	}
	return &CleanupRequiredError{Container: safeCleanupTarget(target)}
}

func (s *DockerSandbox) inspectOwnedLaunchNameLocked(ctx context.Context) (string, bool, error) {
	name := s.containerName
	if !dockerContainerNamePattern.MatchString(name) || len(s.containerOwner) != 32 {
		return "", false, errors.New("launch container ownership is invalid")
	}
	stdout, _, err := s.runner(ctx, s.binary(), "inspect", "-f", launchOwnerInspectTemplate, "--", name)
	if err != nil {
		absent, absentErr := s.launchContainerAbsent(ctx, name, true)
		if absentErr == nil && absent {
			return "", false, nil
		}
		return "", false, errors.New("launch container ownership is unavailable")
	}
	parts := strings.Split(strings.TrimSpace(stdout), "|")
	if len(parts) != 4 || !dockerContainerIDPattern.MatchString(parts[0]) {
		return "", false, errors.New("launch container ownership is invalid")
	}
	if parts[2] != s.cfg.Image || parts[3] != s.containerOwner {
		return "", false, nil
	}
	if s.launchImageID != "" && parts[1] != s.launchImageID {
		return "", false, nil
	}
	return parts[0], true, nil
}

func (s *DockerSandbox) launchContainerAbsent(ctx context.Context, target string, byName bool) (bool, error) {
	filter := "id=" + target
	if byName {
		filter = "name=^/" + target + "$"
	}
	stdout, _, err := s.runner(ctx, s.binary(), "container", "ls", "-a", "--no-trunc", "--filter", filter, "--format", "{{.ID}}")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(stdout) == "", nil
}

func (s *DockerSandbox) ensureContainerLocked(ctx context.Context) error {
	if s.containerID != "" {
		// Check if container is still running
		stdout, _, err := s.runner(ctx, s.binary(), "inspect", "-f", "{{.State.Running}}", "--", s.containerID)
		if err == nil && strings.TrimSpace(stdout) == "true" {
			return nil
		}
		// Container is gone, clean up
		cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _, cleanupErr := s.runner(cleanCtx, s.binary(), "rm", "-f", "--", s.containerID)
		if cleanupErr != nil && s.cfg.StrictCleanup {
			return fmt.Errorf("removing unavailable sandbox container: %w", cleanupErr)
		}
		s.containerID = ""
	}

	return s.createContainerLocked(ctx)
}

func (s *DockerSandbox) createContainerLocked(ctx context.Context) error {
	name := fmt.Sprintf("buckley-sandbox-%d", time.Now().UnixNano())
	owner := ""
	if s.launchAdmission {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return fmt.Errorf("docker create ownership unavailable")
		}
		owner = hex.EncodeToString(token[:])
		name = "buckley-sandbox-" + owner
	}
	s.containerName = name
	s.containerOwner = owner

	args := buildCreateArgsWithOwner(s.cfg, s.workspacePath, name, launchOwnerLabel, owner)
	stdout, stderr, err := s.runner(ctx, s.binary(), args...)
	if err != nil {
		if s.launchAdmission {
			if ownedID, owned, ownershipErr := s.inspectOwnedLaunchNameLocked(ctx); ownershipErr == nil {
				if owned {
					s.containerID = ownedID
				} else {
					s.clearContainerIdentityLocked()
				}
			}
		}
		return fmt.Errorf("docker create: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	containerID := strings.TrimSpace(stdout)
	if containerID == "" || s.launchAdmission && !dockerContainerIDPattern.MatchString(containerID) {
		if s.launchAdmission {
			ownedID, owned, ownershipErr := s.inspectOwnedLaunchNameLocked(ctx)
			if ownershipErr == nil && !owned {
				s.clearContainerIdentityLocked()
				return fmt.Errorf("docker create returned invalid container ID")
			}
			if ownershipErr == nil {
				s.containerID = ownedID
			}
			if cleanupErr := s.removeLaunchContainerLocked(ctx); cleanupErr != nil {
				return fmt.Errorf("docker create returned invalid container ID and cleanup failed")
			}
		}
		return fmt.Errorf("docker create returned invalid container ID")
	}
	if s.launchAdmission {
		s.containerID = containerID
	}

	_, stderr, err = s.runner(ctx, s.binary(), "start", "--", containerID)
	if err != nil {
		var cleanupErr error
		if s.launchAdmission {
			cleanupErr = s.removeLaunchContainerLocked(ctx)
		} else {
			cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _, cleanupErr = s.runner(cleanCtx, s.binary(), "rm", "-f", "--", containerID)
		}
		if cleanupErr != nil && s.cfg.StrictCleanup {
			if !s.launchAdmission {
				s.containerID = containerID
			}
			return fmt.Errorf("docker start: %w (cleanup failed)", err)
		}
		if cleanupErr == nil {
			s.clearContainerIdentityLocked()
		}
		return fmt.Errorf("docker start: %w (stderr: %s)", err, strings.TrimSpace(stderr))
	}

	s.containerID = containerID
	return nil
}

func (s *DockerSandbox) checkPreparedContainerLocked(ctx context.Context) error {
	if s.preparedID == "" || s.containerID != s.preparedID {
		return fmt.Errorf("prepared launch container is unavailable")
	}
	stdout, _, err := s.runner(ctx, s.binary(), "inspect", "-f", "{{.State.Running}}", "--", s.preparedID)
	if err != nil || strings.TrimSpace(stdout) != "true" {
		return fmt.Errorf("prepared launch container is unavailable")
	}
	return nil
}

func (s *DockerSandbox) removeContainer(containerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if containerID == "" || s.containerID != containerID {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if s.launchAdmission {
		return s.removeLaunchContainerLocked(ctx)
	}
	_, _, err := s.runner(ctx, s.binary(), "rm", "-f", "--", containerID)
	if err != nil {
		return err
	}
	s.clearContainerIdentityLocked()
	return nil
}

func validLaunchExecutable(path string) bool {
	if path == "" || !strings.HasPrefix(path, "/") || len(path) > 256 || strings.Contains(path, "..") {
		return false
	}
	for _, r := range path {
		if r != '/' && r != '-' && r != '_' && r != '.' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
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
		_, _, err := s.runner(ctx, s.binary(), "rm", "-f", "--", s.containerID)
		if err != nil && s.cfg.StrictCleanup {
			return
		}
		s.containerID = ""
	})
}

func (s *DockerSandbox) binary() string {
	if s != nil && strings.TrimSpace(s.cfg.Binary) != "" {
		return s.cfg.Binary
	}
	return "docker"
}
