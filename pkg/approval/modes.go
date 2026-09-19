// Package approval provides tiered permission control for agent operations.
//
// Approval modes determine what actions an agent can perform autonomously:
//   - Ask: Explicit approval required for all writes and commands
//   - Safe: Read anything, write only to workspace, no shell/network
//   - Auto: Full workspace access, approval for external operations
//   - Yolo: Full autonomy, minimal prompts (dangerous)
package approval

import (
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

// Result contains the full permission check result.
type Result struct {
	Decision Decision
	Reason   string
	Request  Request
}

// Check denied paths first (applies to ALL modes including yolo)

// Yolo mode allows everything else

// Read operations always allowed

// Unknown operations require approval

// Safe mode allows read-only shell commands

// Auto mode allows shell writes within workspace

// Safe mode only allows local git operations (no push)

// Auto mode allows most git operations except force push

// Check workspace

// Check trusted paths

// isReadOnlyCommand checks if a shell command is read-only.
func isReadOnlyCommand(cmd string) bool {
	cmdLower := strings.ToLower(strings.TrimSpace(cmd))

	// Check for output redirection which makes any command a write
	if strings.Contains(cmdLower, ">") || strings.Contains(cmdLower, ">>") {
		return false
	}

	// Check for command chaining operators which could execute arbitrary commands
	for _, sep := range []string{";", "&&", "||", "|", "`", "$(", "\n"} {
		if strings.Contains(cmdLower, sep) {
			return false
		}
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

// This is a heuristic - complex commands may need manual review

// Check if command explicitly references workspace path

// Commands that typically operate in current directory

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
