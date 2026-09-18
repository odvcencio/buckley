package agent

import "time"

// Role defines the type of work an agent performs.
type Role string

const (
	RoleResearcher Role = "researcher" // Gathers context and information
	RoleCoder      Role = "coder"      // Writes and modifies code
	RoleReviewer   Role = "reviewer"   // Reviews and validates work
	RolePlanner    Role = "planner"    // Creates plans and strategies
	RoleExecutor   Role = "executor"   // Executes tasks from queue
)

// TaskResult represents the outcome of a task execution.
type TaskResult struct {
	TaskID     string        `json:"task_id"`
	AgentID    string        `json:"agent_id"`
	Success    bool          `json:"success"`
	Output     string        `json:"output"`
	Error      string        `json:"error,omitempty"`
	Artifacts  []Artifact    `json:"artifacts,omitempty"`
	Duration   time.Duration `json:"duration"`
	TokensUsed int           `json:"tokens_used"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
}

// Artifact represents a work product from task execution.
type Artifact struct {
	Type    string `json:"type"`    // file, pr, commit, etc.
	Path    string `json:"path"`    // File path or URL
	Content string `json:"content"` // Content or description
}

// ToolCall records a tool invocation during execution.
type ToolCall struct {
	Name      string        `json:"name"`
	Arguments string        `json:"arguments"`
	Result    string        `json:"result"`
	Duration  time.Duration `json:"duration"`
	Success   bool          `json:"success"`
}
