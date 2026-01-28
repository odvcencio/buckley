// Package sandbox provides sandboxed command execution for security.
// It restricts what commands can access based on the approval mode.
package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	dangerousPatterns = []struct {
		re     *regexp.Regexp
		reason string
	}{
		{regexp.MustCompile(`rm\s+-[rf]+\s+/`), "recursive delete from root"},
		{regexp.MustCompile(`rm\s+-[rf]+\s+~`), "recursive delete from home"},
		{regexp.MustCompile(`>\s*/dev/sd`), "writing to block devices"},
		{regexp.MustCompile(`dd\s+.*of=/dev/`), "dd to devices"},
		{regexp.MustCompile(`mkfs`), "formatting filesystems"},
		{regexp.MustCompile(`:\(\)\s*\{`), "fork bomb pattern"},
		{regexp.MustCompile(`chmod\s+777\s+/`), "dangerous permissions on root"},
		{regexp.MustCompile(`chown.*-R.*root`), "recursive ownership change to root"},
	}

	readOnlyPatterns = []*regexp.Regexp{
		regexp.MustCompile(`^cat\s`),
		regexp.MustCompile(`^head\s`),
		regexp.MustCompile(`^tail\s`),
		regexp.MustCompile(`^less\s`),
		regexp.MustCompile(`^more\s`),
		regexp.MustCompile(`^grep\s`),
		regexp.MustCompile(`^rg\s`),
		regexp.MustCompile(`^find\s.*-print`),
		regexp.MustCompile(`^ls\s`),
		regexp.MustCompile(`^pwd$`),
		regexp.MustCompile(`^echo\s`),
		regexp.MustCompile(`^wc\s`),
		regexp.MustCompile(`^file\s`),
		regexp.MustCompile(`^stat\s`),
		regexp.MustCompile(`^du\s`),
		regexp.MustCompile(`^df\s`),
		regexp.MustCompile(`^which\s`),
		regexp.MustCompile(`^whereis\s`),
		regexp.MustCompile(`^type\s`),
		regexp.MustCompile(`^git\s+status`),
		regexp.MustCompile(`^git\s+log`),
		regexp.MustCompile(`^git\s+diff`),
		regexp.MustCompile(`^git\s+show`),
		regexp.MustCompile(`^git\s+branch`),
		regexp.MustCompile(`^go\s+version`),
		regexp.MustCompile(`^go\s+list`),
		regexp.MustCompile(`^node\s+--version`),
		regexp.MustCompile(`^npm\s+list`),
		regexp.MustCompile(`^python\s+--version`),
		regexp.MustCompile(`^pip\s+list`),
	}
	networkPatterns = []*regexp.Regexp{
		regexp.MustCompile(`curl\s`),
		regexp.MustCompile(`wget\s`),
		regexp.MustCompile(`ssh\s`),
		regexp.MustCompile(`scp\s`),
		regexp.MustCompile(`rsync\s`),
		regexp.MustCompile(`ftp\s`),
		regexp.MustCompile(`sftp\s`),
		regexp.MustCompile(`nc\s`),
		regexp.MustCompile(`netcat\s`),
		regexp.MustCompile(`telnet\s`),
		regexp.MustCompile(`nmap\s`),
		regexp.MustCompile(`ping\s`),
		regexp.MustCompile(`traceroute\s`),
		regexp.MustCompile(`dig\s`),
		regexp.MustCompile(`nslookup\s`),
		regexp.MustCompile(`host\s`),
	}
)

// Mode represents the sandbox security level
type Mode int

const (
	// ModeDisabled allows all commands unrestricted
	ModeDisabled Mode = iota
	// ModeReadOnly allows only read-only commands
	ModeReadOnly
	// ModeWorkspace allows writes only within workspace
	ModeWorkspace
	// ModeStrict restricts to explicitly allowed commands
	ModeStrict
)

// Config configures the sandbox behavior
type Config struct {
	Mode            Mode
	WorkspacePath   string
	AllowedPaths    []string
	DeniedPaths     []string
	AllowedCommands []string
	DeniedCommands  []string
	AllowNetwork    bool
	Timeout         time.Duration
	MaxOutputSize   int64 // Max output bytes (0 = unlimited)
}

// DefaultConfig returns a safe default configuration
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	cwd, _ := os.Getwd()

	return Config{
		Mode:          ModeWorkspace,
		WorkspacePath: cwd,
		AllowedPaths:  []string{cwd},
		DeniedPaths: []string{
			filepath.Join(home, ".ssh"),
			filepath.Join(home, ".gnupg"),
			filepath.Join(home, ".aws"),
			"/etc",
			"/var",
			"/usr",
			"/bin",
			"/sbin",
		},
		DeniedCommands: []string{
			"rm -rf /",
			"rm -rf ~",
			"sudo rm",
			"chmod 777",
			"curl | sh",
			"curl | bash",
			"wget | sh",
			"wget | bash",
		},
		AllowNetwork:  false,
		Timeout:       5 * time.Minute,
		MaxOutputSize: 10 * 1024 * 1024, // 10MB
	}
}

// Sandbox provides sandboxed command execution
type Sandbox struct {
	config Config
}

// New creates a new sandbox with the given configuration
func New(config Config) *Sandbox {
	return &Sandbox{config: config}
}

// NewWithDefaults creates a sandbox with default configuration
func NewWithDefaults() *Sandbox {
	return New(DefaultConfig())
}

// Result contains the result of a sandboxed command execution
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
	Killed   bool
	Error    error
}

// Validate checks if a command is allowed to run
func (s *Sandbox) Validate(command string) error {
	if s.config.Mode == ModeDisabled {
		return nil
	}

	// Check denied commands
	for _, denied := range s.config.DeniedCommands {
		if strings.Contains(command, denied) {
			return fmt.Errorf("command contains denied pattern: %s", denied)
		}
	}

	// Check for dangerous patterns
	if err := s.checkDangerousPatterns(command); err != nil {
		return err
	}

	// Mode-specific checks
	switch s.config.Mode {
	case ModeReadOnly:
		if !s.isReadOnlyCommand(command) {
			return fmt.Errorf("command may modify files (read-only mode)")
		}
	case ModeWorkspace:
		if err := s.checkWorkspaceBounds(command); err != nil {
			return err
		}
	case ModeStrict:
		if !s.isAllowedCommand(command) {
			return fmt.Errorf("command not in allowed list (strict mode)")
		}
	}

	// Check network access
	if !s.config.AllowNetwork && s.usesNetwork(command) {
		return fmt.Errorf("network access not allowed")
	}

	return nil
}

// Execute runs a command in the sandbox
func (s *Sandbox) Execute(ctx context.Context, command string) *Result {
	start := time.Now()
	result := &Result{}

	// Validate first
	if err := s.Validate(command); err != nil {
		result.Error = err
		result.ExitCode = 1
		return result
	}

	// Create command with timeout
	if s.config.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.config.Timeout)
		defer cancel()
	}

	cmd := shellCommandContext(ctx, command)

	// Set up process group for cleanup (platform-specific)
	setSysProcAttr(cmd)

	// Restrict environment if in strict mode
	if s.config.Mode == ModeStrict {
		cmd.Env = s.restrictedEnv()
	}

	// Set working directory
	if s.config.WorkspacePath != "" {
		cmd.Dir = s.config.WorkspacePath
	}

	// Capture output
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result.Duration = time.Since(start)
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()

	if ctx.Err() == context.DeadlineExceeded {
		result.Killed = true
		result.Error = fmt.Errorf("command timed out after %v", s.config.Timeout)
		result.ExitCode = 124 // Standard timeout exit code
		return result
	}

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitError.ExitCode()
		} else {
			result.Error = err
			result.ExitCode = 1
		}
	}

	// Truncate output if too large
	if s.config.MaxOutputSize > 0 {
		if int64(len(result.Stdout)) > s.config.MaxOutputSize {
			result.Stdout = result.Stdout[:s.config.MaxOutputSize] + "\n... (output truncated)"
		}
		if int64(len(result.Stderr)) > s.config.MaxOutputSize {
			result.Stderr = result.Stderr[:s.config.MaxOutputSize] + "\n... (output truncated)"
		}
	}

	return result
}

func (s *Sandbox) checkDangerousPatterns(command string) error {
	for _, d := range dangerousPatterns {
		if d.re.MatchString(command) {
			return fmt.Errorf("dangerous command pattern detected: %s", d.reason)
		}
	}

	return nil
}

func (s *Sandbox) isReadOnlyCommand(command string) bool {
	// Check for redirects (writes) first - this takes precedence
	if strings.Contains(command, ">") || strings.Contains(command, ">>") {
		return false
	}

	// Commands that don't modify files
	for _, pattern := range readOnlyPatterns {
		if pattern.MatchString(command) {
			return true
		}
	}

	// Check for write commands
	writeCommands := []string{"rm", "mv", "cp", "mkdir", "rmdir", "touch", "chmod", "chown", "git commit", "git push"}
	for _, wc := range writeCommands {
		if strings.Contains(command, wc) {
			return false
		}
	}

	return true
}

func (s *Sandbox) checkWorkspaceBounds(command string) error {
	// Extract paths from command
	paths := extractPaths(command)

	for _, path := range paths {
		absPath, err := filepath.Abs(path)
		if err != nil {
			continue
		}

		// Check if path is within workspace
		if !strings.HasPrefix(absPath, s.config.WorkspacePath) {
			// Check if it's an allowed path
			allowed := false
			for _, ap := range s.config.AllowedPaths {
				if strings.HasPrefix(absPath, ap) {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("path outside workspace: %s", path)
			}
		}

		// Check denied paths
		for _, dp := range s.config.DeniedPaths {
			if strings.HasPrefix(absPath, dp) {
				return fmt.Errorf("access to denied path: %s", path)
			}
		}
	}

	return nil
}

func (s *Sandbox) isAllowedCommand(command string) bool {
	baseCmd := strings.Fields(command)[0]

	for _, allowed := range s.config.AllowedCommands {
		if baseCmd == allowed {
			return true
		}
		// Also match by prefix
		if strings.HasPrefix(command, allowed) {
			return true
		}
	}

	return false
}

func (s *Sandbox) usesNetwork(command string) bool {
	for _, pattern := range networkPatterns {
		if pattern.MatchString(command) {
			return true
		}
	}

	return false
}

func (s *Sandbox) restrictedEnv() []string {
	// Only pass through safe environment variables
	safeVars := []string{
		"PATH",
		"HOME",
		"USER",
		"SHELL",
		"TERM",
		"LANG",
		"LC_ALL",
		"TZ",
	}

	var env []string
	for _, key := range safeVars {
		if val := os.Getenv(key); val != "" {
			env = append(env, fmt.Sprintf("%s=%s", key, val))
		}
	}

	return env
}

// extractPaths attempts to extract file paths from a command
func extractPaths(command string) []string {
	var paths []string

	// Split by common separators
	parts := strings.Fields(command)

	for _, part := range parts {
		// Skip flags
		if strings.HasPrefix(part, "-") {
			continue
		}

		// Check if it looks like a path
		if strings.HasPrefix(part, "/") ||
			strings.HasPrefix(part, "./") ||
			strings.HasPrefix(part, "../") ||
			strings.HasPrefix(part, "~/") ||
			strings.Contains(part, "/") {
			// Expand ~
			if strings.HasPrefix(part, "~/") {
				if home, err := os.UserHomeDir(); err == nil {
					part = filepath.Join(home, part[2:])
				}
			}
			paths = append(paths, part)
		}
	}

	return paths
}

// ModeFromString parses a mode string
func ModeFromString(s string) Mode {
	switch strings.ToLower(s) {
	case "disabled", "none", "off":
		return ModeDisabled
	case "readonly", "read-only", "ro":
		return ModeReadOnly
	case "workspace", "ws":
		return ModeWorkspace
	case "strict":
		return ModeStrict
	default:
		return ModeWorkspace
	}
}

// String returns the string representation of a mode
func (m Mode) String() string {
	switch m {
	case ModeDisabled:
		return "disabled"
	case ModeReadOnly:
		return "read-only"
	case ModeWorkspace:
		return "workspace"
	case ModeStrict:
		return "strict"
	default:
		return "unknown"
	}
}
