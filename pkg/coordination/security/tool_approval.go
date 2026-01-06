package security

import (
	"context"
	"fmt"
	"sync"
	"time"
)

var (
	ErrToolNotAllowed = fmt.Errorf("tool not allowed for agent capabilities")
)

// ToolPolicy defines which tools are allowed for each capability
type ToolPolicy struct {
	mu    sync.RWMutex
	rules map[string][]string // capability -> allowed tools
}

// NewToolPolicy creates a new empty tool policy
func NewToolPolicy() *ToolPolicy {
	return &ToolPolicy{
		rules: make(map[string][]string),
	}
}

// AddRule adds a policy rule allowing certain tools for a capability
func (tp *ToolPolicy) AddRule(capability string, tools []string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	tp.rules[capability] = append(tp.rules[capability], tools...)
}

// RemoveRule removes all tools for a capability
func (tp *ToolPolicy) RemoveRule(capability string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	delete(tp.rules, capability)
}

// IsToolAllowed checks if a single capability allows a tool
func (tp *ToolPolicy) IsToolAllowed(capability string, tool string) bool {
	tp.mu.RLock()
	defer tp.mu.RUnlock()

	tools, exists := tp.rules[capability]
	if !exists {
		return false
	}

	// Check for wildcard (*) which allows all tools
	for _, t := range tools {
		if t == "*" || t == tool {
			return true
		}
	}

	return false
}

// IsToolAllowedForCapabilities checks if any of the given capabilities allow a tool
func (tp *ToolPolicy) IsToolAllowedForCapabilities(capabilities []string, tool string) bool {
	for _, cap := range capabilities {
		if tp.IsToolAllowed(cap, tool) {
			return true
		}
	}
	return false
}

// GetAllowedTools returns all tools allowed for a capability
func (tp *ToolPolicy) GetAllowedTools(capability string) []string {
	tp.mu.RLock()
	defer tp.mu.RUnlock()

	tools, exists := tp.rules[capability]
	if !exists {
		return []string{}
	}

	// Make a copy to avoid mutation
	result := make([]string, len(tools))
	copy(result, tools)
	return result
}

// ToolApprover enforces tool access policies for agents
type ToolApprover struct {
	policy   *ToolPolicy
	mu       sync.RWMutex
	auditLog []AuditEntry
}

// AuditEntry records a tool access attempt
type AuditEntry struct {
	Timestamp time.Time
	AgentID   string
	ToolName  string
	Allowed   bool
	Reason    string
}

// NewToolApprover creates a new tool approver with the given policy
func NewToolApprover(policy *ToolPolicy) *ToolApprover {
	return &ToolApprover{
		policy:   policy,
		auditLog: make([]AuditEntry, 0),
	}
}

// CheckToolAccess verifies that an agent can use a specific tool
func (ta *ToolApprover) CheckToolAccess(ctx context.Context, tool string) error {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		ta.logAccess("unknown", tool, false, "no authentication claims")
		return ErrInsufficientAuth
	}

	// Admin capability bypasses all restrictions
	for _, cap := range claims.Capabilities {
		if cap == "admin" {
			ta.logAccess(claims.AgentID, tool, true, "admin bypass")
			return nil
		}
	}

	// Check if any capability allows this tool
	if ta.policy.IsToolAllowedForCapabilities(claims.Capabilities, tool) {
		ta.logAccess(claims.AgentID, tool, true, "allowed by capability")
		return nil
	}

	ta.logAccess(claims.AgentID, tool, false, "no capability grants access")
	return fmt.Errorf("%w: %s (agent: %s, capabilities: %v)",
		ErrToolNotAllowed, tool, claims.AgentID, claims.Capabilities)
}

// GetAllowedToolsForAgent returns all tools an agent can use
func (ta *ToolApprover) GetAllowedToolsForAgent(ctx context.Context) []string {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return []string{}
	}

	// Admin gets all tools (represented by wildcard)
	for _, cap := range claims.Capabilities {
		if cap == "admin" {
			return []string{"*"}
		}
	}

	// Collect unique tools from all capabilities
	toolSet := make(map[string]bool)
	for _, cap := range claims.Capabilities {
		tools := ta.policy.GetAllowedTools(cap)
		for _, tool := range tools {
			toolSet[tool] = true
		}
	}

	// Convert set to slice
	result := make([]string, 0, len(toolSet))
	for tool := range toolSet {
		result = append(result, tool)
	}

	return result
}

// logAccess records a tool access attempt in the audit log
func (ta *ToolApprover) logAccess(agentID, tool string, allowed bool, reason string) {
	ta.mu.Lock()
	defer ta.mu.Unlock()

	entry := AuditEntry{
		Timestamp: time.Now(),
		AgentID:   agentID,
		ToolName:  tool,
		Allowed:   allowed,
		Reason:    reason,
	}

	ta.auditLog = append(ta.auditLog, entry)

	// Keep only last 10000 entries to prevent unbounded growth
	if len(ta.auditLog) > 10000 {
		ta.auditLog = ta.auditLog[len(ta.auditLog)-10000:]
	}
}

// GetAuditLog returns recent audit entries for an agent
func (ta *ToolApprover) GetAuditLog(agentID string, limit int) []AuditEntry {
	ta.mu.RLock()
	defer ta.mu.RUnlock()

	result := make([]AuditEntry, 0, limit)

	// Iterate from most recent to oldest
	for i := len(ta.auditLog) - 1; i >= 0 && len(result) < limit; i-- {
		if ta.auditLog[i].AgentID == agentID {
			result = append(result, ta.auditLog[i])
		}
	}

	// Reverse to return oldest-to-newest
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return result
}

// DefaultToolPolicy returns a sensible default policy for Buckley's actual tools
func DefaultToolPolicy() *ToolPolicy {
	policy := NewToolPolicy()

	// Admin can do everything
	policy.AddRule("admin", []string{"*"})

	// Shell execution (highest risk)
	policy.AddRule("shell_execution", []string{
		"shell", // Run shell commands
	})

	// File system access
	policy.AddRule("file_access", []string{
		"file",            // File operations (read, write, delete, etc.)
		"terminal_editor", // Edit files interactively
	})

	// Git operations
	policy.AddRule("git_access", []string{
		"git",   // Git commands (clone, commit, push, pull, branch, merge, etc.)
		"merge", // Code merging
	})

	// Code analysis and navigation (read-only)
	policy.AddRule("code_analysis", []string{
		"search",          // Text-based code search
		"semantic_search", // Semantic code search with embeddings
		"code_index",      // Build/query code index
		"navigation",      // Navigate codebase
		"quality",         // Code quality analysis
	})

	// Code modification
	policy.AddRule("code_modification", []string{
		"refactoring",     // Refactor code
		"terminal_editor", // Edit files
		"file",            // File operations
	})

	// Testing
	policy.AddRule("testing", []string{
		"testing", // Run tests
	})

	// Documentation
	policy.AddRule("documentation", []string{
		"documentation", // Generate/manage documentation
	})

	// Web browsing
	policy.AddRule("web_access", []string{
		"browser", // Web browsing capabilities
	})

	// Task management
	policy.AddRule("task_management", []string{
		"todo", // Manage TODO items
	})

	// Agent orchestration
	policy.AddRule("agent_orchestration", []string{
		"delegate",         // Spawn sub-agents
		"skill_activation", // Activate skills
	})

	// Data operations
	policy.AddRule("data_operations", []string{
		"excel", // Excel file operations
	})

	// Read-only safe operations (no modification capabilities)
	policy.AddRule("read_only", []string{
		"search",          // Search code
		"semantic_search", // Semantic search
		"code_index",      // Query code index (read-only)
		"navigation",      // Navigate codebase
		"quality",         // Quality analysis
		"browser",         // Web browsing (read-only if sandboxed)
	})

	return policy
}
