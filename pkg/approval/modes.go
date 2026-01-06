// Package approval provides tiered permission control for agent operations.
//
// Approval modes determine what actions an agent can perform autonomously:
//   - Ask: Explicit approval required for all writes and commands
//   - Safe: Read anything, write only to workspace, no shell/network
//   - Auto: Full workspace access, approval for external operations
//   - Yolo: Full autonomy, minimal prompts (dangerous)
package approval

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Mode represents an approval level for agent operations.
type Mode int

const (
	// ModeAsk requires explicit approval for all write operations and commands.
	// Read operations are allowed. This is the safest mode.
	ModeAsk Mode = iota

	// ModeSafe allows reading any file and writing within the workspace.
	// Shell commands and network access require approval.
	ModeSafe

	// ModeAuto allows full workspace operations including shell commands.
	// Operations outside workspace or with network access require approval.
	ModeAuto

	// ModeYolo allows all operations without approval prompts.
	// Use with extreme caution - agent has full system access.
	ModeYolo
)

// String returns the mode name.
func (m Mode) String() string {
	switch m {
	case ModeAsk:
		return "ask"
	case ModeSafe:
		return "safe"
	case ModeAuto:
		return "auto"
	case ModeYolo:
		return "yolo"
	default:
		return "unknown"
	}
}

// ParseMode converts a string to an approval mode.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ask", "explicit", "manual":
		return ModeAsk, nil
	case "safe", "readonly", "read-only":
		return ModeSafe, nil
	case "auto", "automatic", "workspace":
		return ModeAuto, nil
	case "yolo", "full", "dangerous":
		return ModeYolo, nil
	default:
		return ModeAsk, fmt.Errorf("unknown approval mode: %s (valid: ask, safe, auto, yolo)", s)
	}
}

// Operation represents a type of action the agent wants to perform.
type Operation int

const (
	OpRead Operation = iota
	OpWrite
	OpDelete
	OpShellRead    // Shell command that only reads (e.g., ls, cat)
	OpShellWrite   // Shell command that modifies state
	OpShellNetwork // Shell command with network access
	OpNetwork      // Direct network request
	OpGitRead      // Git read operations (status, log, diff)
	OpGitWrite     // Git write operations (commit, push, checkout)
)

// String returns the operation name.
func (o Operation) String() string {
	names := []string{
		"read", "write", "delete",
		"shell:read", "shell:write", "shell:network",
		"network", "git:read", "git:write",
	}
	if int(o) < len(names) {
		return names[o]
	}
	return "unknown"
}

// Request represents a permission check for an operation.
type Request struct {
	Operation   Operation
	Path        string   // File path for file operations
	Command     string   // Command for shell operations
	Tool        string   // Tool name requesting the operation
	Description string   // Human-readable description
	Args        []string // Additional arguments
}

// Context provides workspace context for permission decisions.
type Context struct {
	WorkspacePath string   // Root of the workspace/project
	TrustedPaths  []string // Additional paths with write access
	DeniedPaths   []string // Paths that are never writable
	AllowNetwork  bool     // Whether network is allowed in auto mode
}

// Decision represents the result of a permission check.
type Decision int

const (
	DecisionAllow Decision = iota
	DecisionDeny
	DecisionPrompt // Requires user approval
)

// String returns the decision name.
func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionDeny:
		return "deny"
	case DecisionPrompt:
		return "prompt"
	default:
		return "unknown"
	}
}

// Result contains the full permission check result.
type Result struct {
	Decision Decision
	Reason   string
	Request  Request
}

// Check evaluates whether an operation should be allowed, denied, or prompted.
func Check(mode Mode, req Request, ctx Context) Result {
	// Yolo mode allows everything
	if mode == ModeYolo {
		return Result{Decision: DecisionAllow, Reason: "yolo mode", Request: req}
	}

	// Check denied paths first (applies to all modes)
	if req.Path != "" && isPathDenied(req.Path, ctx) {
		return Result{Decision: DecisionDeny, Reason: "path is in denied list", Request: req}
	}

	switch req.Operation {
	case OpRead, OpGitRead:
		// Read operations always allowed
		return Result{Decision: DecisionAllow, Reason: "read operations allowed", Request: req}

	case OpWrite, OpDelete:
		return checkFileWrite(mode, req, ctx)

	case OpShellRead:
		return checkShellRead(mode, req, ctx)

	case OpShellWrite:
		return checkShellWrite(mode, req, ctx)

	case OpShellNetwork:
		return checkShellNetwork(mode, req, ctx)

	case OpNetwork:
		return checkNetwork(mode, req, ctx)

	case OpGitWrite:
		return checkGitWrite(mode, req, ctx)

	default:
		// Unknown operations require approval
		return Result{Decision: DecisionPrompt, Reason: "unknown operation type", Request: req}
	}
}

func checkFileWrite(mode Mode, req Request, ctx Context) Result {
	switch mode {
	case ModeAsk:
		return Result{Decision: DecisionPrompt, Reason: "ask mode requires approval for writes", Request: req}

	case ModeSafe, ModeAuto:
		if isPathInWorkspace(req.Path, ctx) {
			return Result{Decision: DecisionAllow, Reason: "path is within workspace", Request: req}
		}
		return Result{Decision: DecisionPrompt, Reason: "path is outside workspace", Request: req}

	default:
		return Result{Decision: DecisionPrompt, Reason: "unknown mode", Request: req}
	}
}

func checkShellRead(mode Mode, req Request, ctx Context) Result {
	switch mode {
	case ModeAsk:
		return Result{Decision: DecisionPrompt, Reason: "ask mode requires approval for shell", Request: req}

	case ModeSafe:
		// Safe mode allows read-only shell commands
		if isReadOnlyCommand(req.Command) {
			return Result{Decision: DecisionAllow, Reason: "read-only shell command in safe mode", Request: req}
		}
		return Result{Decision: DecisionPrompt, Reason: "command may have side effects", Request: req}

	case ModeAuto:
		return Result{Decision: DecisionAllow, Reason: "shell read allowed in auto mode", Request: req}

	default:
		return Result{Decision: DecisionPrompt, Reason: "unknown mode", Request: req}
	}
}

func checkShellWrite(mode Mode, req Request, ctx Context) Result {
	switch mode {
	case ModeAsk, ModeSafe:
		return Result{Decision: DecisionPrompt, Reason: "shell write requires approval", Request: req}

	case ModeAuto:
		// Auto mode allows shell writes within workspace
		if commandTargetsWorkspace(req.Command, ctx) {
			return Result{Decision: DecisionAllow, Reason: "shell command targets workspace", Request: req}
		}
		return Result{Decision: DecisionPrompt, Reason: "shell command may affect files outside workspace", Request: req}

	default:
		return Result{Decision: DecisionPrompt, Reason: "unknown mode", Request: req}
	}
}

func checkShellNetwork(mode Mode, req Request, ctx Context) Result {
	switch mode {
	case ModeAsk, ModeSafe:
		return Result{Decision: DecisionPrompt, Reason: "network access requires approval", Request: req}

	case ModeAuto:
		if ctx.AllowNetwork {
			return Result{Decision: DecisionAllow, Reason: "network allowed in auto mode", Request: req}
		}
		return Result{Decision: DecisionPrompt, Reason: "network access requires approval", Request: req}

	default:
		return Result{Decision: DecisionPrompt, Reason: "unknown mode", Request: req}
	}
}

func checkNetwork(mode Mode, req Request, ctx Context) Result {
	switch mode {
	case ModeAsk, ModeSafe:
		return Result{Decision: DecisionPrompt, Reason: "network access requires approval", Request: req}

	case ModeAuto:
		if ctx.AllowNetwork {
			return Result{Decision: DecisionAllow, Reason: "network allowed in auto mode", Request: req}
		}
		return Result{Decision: DecisionPrompt, Reason: "network access requires approval", Request: req}

	default:
		return Result{Decision: DecisionPrompt, Reason: "unknown mode", Request: req}
	}
}

func checkGitWrite(mode Mode, req Request, ctx Context) Result {
	switch mode {
	case ModeAsk:
		return Result{Decision: DecisionPrompt, Reason: "ask mode requires approval for git writes", Request: req}

	case ModeSafe:
		// Safe mode only allows local git operations (no push)
		if isLocalGitCommand(req.Command) {
			return Result{Decision: DecisionAllow, Reason: "local git operation in safe mode", Request: req}
		}
		return Result{Decision: DecisionPrompt, Reason: "remote git operations require approval", Request: req}

	case ModeAuto:
		// Auto mode allows most git operations except force push
		if isForcePush(req.Command) {
			return Result{Decision: DecisionPrompt, Reason: "force push requires approval", Request: req}
		}
		return Result{Decision: DecisionAllow, Reason: "git operation allowed in auto mode", Request: req}

	default:
		return Result{Decision: DecisionPrompt, Reason: "unknown mode", Request: req}
	}
}

// isPathInWorkspace checks if a path is within the workspace or trusted paths.
func isPathInWorkspace(path string, ctx Context) bool {
	if path == "" {
		return false
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	// Check workspace
	if ctx.WorkspacePath != "" {
		wsAbs, err := filepath.Abs(ctx.WorkspacePath)
		if err == nil && strings.HasPrefix(absPath, wsAbs) {
			return true
		}
	}

	// Check trusted paths
	for _, trusted := range ctx.TrustedPaths {
		trustedAbs, err := filepath.Abs(trusted)
		if err == nil && strings.HasPrefix(absPath, trustedAbs) {
			return true
		}
	}

	return false
}

// isPathDenied checks if a path is explicitly denied.
func isPathDenied(path string, ctx Context) bool {
	if path == "" || len(ctx.DeniedPaths) == 0 {
		return false
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}

	for _, denied := range ctx.DeniedPaths {
		deniedAbs, err := filepath.Abs(denied)
		if err == nil && strings.HasPrefix(absPath, deniedAbs) {
			return true
		}
	}

	return false
}

// isReadOnlyCommand checks if a shell command is read-only.
func isReadOnlyCommand(cmd string) bool {
	cmdLower := strings.ToLower(strings.TrimSpace(cmd))

	// Check for output redirection which makes any command a write
	if strings.Contains(cmdLower, ">") || strings.Contains(cmdLower, ">>") {
		return false
	}

	readOnlyPrefixes := []string{
		"ls", "cat", "head", "tail", "grep", "rg", "find", "fd",
		"wc", "diff", "file", "stat", "which", "type",
		"pwd", "whoami", "date", "env", "printenv",
		"git status", "git log", "git diff", "git show", "git branch",
		"go version", "go list", "go env",
		"node --version", "npm list", "npm view",
		"python --version", "pip list", "pip show",
	}

	for _, prefix := range readOnlyPrefixes {
		if strings.HasPrefix(cmdLower, prefix) {
			return true
		}
	}

	// echo without redirection is read-only (just prints to stdout)
	if strings.HasPrefix(cmdLower, "echo") {
		return true
	}

	return false
}

// commandTargetsWorkspace attempts to determine if a command operates within workspace.
func commandTargetsWorkspace(cmd string, ctx Context) bool {
	// This is a heuristic - complex commands may need manual review
	if ctx.WorkspacePath == "" {
		return false
	}

	// Check if command explicitly references workspace path
	if strings.Contains(cmd, ctx.WorkspacePath) {
		return true
	}

	// Commands that typically operate in current directory
	localCommands := []string{
		"go build", "go test", "go run", "go fmt", "go vet",
		"npm install", "npm run", "npm test",
		"yarn", "pnpm",
		"make", "cargo build", "cargo test",
		"pytest", "python -m pytest",
		"bundle install", "rake",
	}

	cmdLower := strings.ToLower(strings.TrimSpace(cmd))
	for _, local := range localCommands {
		if strings.HasPrefix(cmdLower, local) {
			return true
		}
	}

	return false
}

// isLocalGitCommand checks if a git command is local-only.
func isLocalGitCommand(cmd string) bool {
	remoteCommands := []string{"push", "fetch", "pull", "clone", "remote"}
	cmdLower := strings.ToLower(cmd)

	for _, remote := range remoteCommands {
		if strings.Contains(cmdLower, "git "+remote) || strings.Contains(cmdLower, "git "+remote) {
			return false
		}
	}

	return true
}

// isForcePush checks if a command is a force push.
func isForcePush(cmd string) bool {
	cmdLower := strings.ToLower(cmd)
	return strings.Contains(cmdLower, "push") &&
		(strings.Contains(cmdLower, "-f") || strings.Contains(cmdLower, "--force"))
}

// NetworkCommands contains patterns that indicate network access.
var NetworkCommands = []string{
	"curl", "wget", "http", "ssh", "scp", "rsync",
	"git clone", "git fetch", "git pull", "git push",
	"npm publish", "npm install", // npm install can fetch
	"pip install",
	"docker pull", "docker push",
}

// ClassifyCommand determines the operation type for a shell command.
func ClassifyCommand(cmd string) Operation {
	cmdLower := strings.ToLower(strings.TrimSpace(cmd))

	// Check for network commands
	for _, netCmd := range NetworkCommands {
		if strings.Contains(cmdLower, netCmd) {
			return OpShellNetwork
		}
	}

	// Check for read-only commands
	if isReadOnlyCommand(cmd) {
		return OpShellRead
	}

	// Default to shell write (safer assumption)
	return OpShellWrite
}
