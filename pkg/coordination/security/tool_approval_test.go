package security

import (
	"context"
	"testing"
)

func TestToolPolicy_Simple(t *testing.T) {
	policy := NewToolPolicy()

	// Define policy: code_execution capability allows run_code and execute_shell tools
	policy.AddRule("code_execution", []string{"run_code", "execute_shell"})
	policy.AddRule("file_access", []string{"read_file", "write_file", "list_directory"})

	// Test allowed tools
	if !policy.IsToolAllowed("code_execution", "run_code") {
		t.Error("run_code should be allowed for code_execution capability")
	}

	if !policy.IsToolAllowed("file_access", "read_file") {
		t.Error("read_file should be allowed for file_access capability")
	}

	// Test disallowed tools
	if policy.IsToolAllowed("code_execution", "read_file") {
		t.Error("read_file should NOT be allowed for code_execution capability")
	}

	if policy.IsToolAllowed("network_access", "run_code") {
		t.Error("run_code should NOT be allowed for non-existent capability")
	}
}

func TestToolPolicy_MultipleCapabilities(t *testing.T) {
	policy := NewToolPolicy()

	policy.AddRule("code_execution", []string{"run_code"})
	policy.AddRule("file_access", []string{"read_file"})

	capabilities := []string{"code_execution", "file_access"}

	// Should be allowed if ANY capability grants access
	if !policy.IsToolAllowedForCapabilities(capabilities, "run_code") {
		t.Error("run_code should be allowed with code_execution capability")
	}

	if !policy.IsToolAllowedForCapabilities(capabilities, "read_file") {
		t.Error("read_file should be allowed with file_access capability")
	}

	// Should be denied if no capability grants access
	if policy.IsToolAllowedForCapabilities(capabilities, "network_request") {
		t.Error("network_request should NOT be allowed")
	}
}

func TestToolPolicy_Wildcards(t *testing.T) {
	policy := NewToolPolicy()

	// "*" capability allows all tools
	policy.AddRule("admin", []string{"*"})

	if !policy.IsToolAllowed("admin", "any_tool") {
		t.Error("admin capability should allow any tool")
	}

	if !policy.IsToolAllowed("admin", "another_tool") {
		t.Error("admin capability should allow any tool")
	}
}

func TestToolPolicy_RemoveRule(t *testing.T) {
	policy := NewToolPolicy()

	policy.AddRule("code_execution", []string{"run_code"})

	if !policy.IsToolAllowed("code_execution", "run_code") {
		t.Error("run_code should be allowed before removal")
	}

	policy.RemoveRule("code_execution")

	if policy.IsToolAllowed("code_execution", "run_code") {
		t.Error("run_code should NOT be allowed after removal")
	}
}

func TestToolPolicy_GetAllowedTools(t *testing.T) {
	policy := NewToolPolicy()

	policy.AddRule("code_execution", []string{"run_code", "execute_shell"})

	tools := policy.GetAllowedTools("code_execution")
	if len(tools) != 2 {
		t.Errorf("Expected 2 allowed tools, got %d", len(tools))
	}

	hasRunCode := false
	hasExecuteShell := false
	for _, tool := range tools {
		if tool == "run_code" {
			hasRunCode = true
		}
		if tool == "execute_shell" {
			hasExecuteShell = true
		}
	}

	if !hasRunCode || !hasExecuteShell {
		t.Error("Expected run_code and execute_shell in allowed tools")
	}
}

func TestToolApprover_CheckToolAccess(t *testing.T) {
	policy := NewToolPolicy()
	policy.AddRule("code_execution", []string{"run_code"})
	policy.AddRule("file_access", []string{"read_file"})

	approver := NewToolApprover(policy)

	// Create context with claims
	claims := &Claims{
		AgentID:      "agent-1",
		Capabilities: []string{"code_execution", "file_access"},
	}
	ctx := ContextWithClaims(context.Background(), claims)

	// Test allowed tool
	err := approver.CheckToolAccess(ctx, "run_code")
	if err != nil {
		t.Errorf("run_code should be allowed: %v", err)
	}

	// Test disallowed tool
	err = approver.CheckToolAccess(ctx, "network_request")
	if err == nil {
		t.Error("network_request should NOT be allowed")
	}
}

func TestToolApprover_WithoutClaims(t *testing.T) {
	policy := NewToolPolicy()
	approver := NewToolApprover(policy)

	ctx := context.Background()

	err := approver.CheckToolAccess(ctx, "any_tool")
	if err == nil {
		t.Error("Should fail when no claims in context")
	}
}

func TestToolApprover_AdminBypass(t *testing.T) {
	policy := NewToolPolicy()
	policy.AddRule("code_execution", []string{"run_code"})
	// No rule for network_request

	approver := NewToolApprover(policy)

	// Create context with admin capability
	claims := &Claims{
		AgentID:      "admin-agent",
		Capabilities: []string{"admin"},
	}
	ctx := ContextWithClaims(context.Background(), claims)

	// Admin capability should allow ANY tool
	err := approver.CheckToolAccess(ctx, "run_code")
	if err != nil {
		t.Errorf("Admin should be allowed to use run_code: %v", err)
	}

	err = approver.CheckToolAccess(ctx, "network_request")
	if err != nil {
		t.Errorf("Admin should be allowed to use network_request: %v", err)
	}

	err = approver.CheckToolAccess(ctx, "any_tool")
	if err != nil {
		t.Errorf("Admin should be allowed to use any tool: %v", err)
	}
}

func TestToolApprover_GetAllowedToolsForAgent(t *testing.T) {
	policy := NewToolPolicy()
	policy.AddRule("code_execution", []string{"run_code", "execute_shell"})
	policy.AddRule("file_access", []string{"read_file", "write_file"})

	approver := NewToolApprover(policy)

	claims := &Claims{
		AgentID:      "agent-1",
		Capabilities: []string{"code_execution", "file_access"},
	}
	ctx := ContextWithClaims(context.Background(), claims)

	tools := approver.GetAllowedToolsForAgent(ctx)

	// Should have 4 tools total
	if len(tools) != 4 {
		t.Errorf("Expected 4 allowed tools, got %d", len(tools))
	}

	expectedTools := map[string]bool{
		"run_code":      true,
		"execute_shell": true,
		"read_file":     true,
		"write_file":    true,
	}

	for _, tool := range tools {
		if !expectedTools[tool] {
			t.Errorf("Unexpected tool in allowed list: %s", tool)
		}
	}
}

func TestToolApprover_AuditLog(t *testing.T) {
	policy := NewToolPolicy()
	policy.AddRule("code_execution", []string{"run_code"})

	approver := NewToolApprover(policy)

	claims := &Claims{
		AgentID:      "agent-1",
		Capabilities: []string{"code_execution"},
	}
	ctx := ContextWithClaims(context.Background(), claims)

	// Check tool access multiple times
	approver.CheckToolAccess(ctx, "run_code")
	approver.CheckToolAccess(ctx, "run_code")
	approver.CheckToolAccess(ctx, "network_request") // denied

	// Get audit log
	entries := approver.GetAuditLog("agent-1", 10)

	if len(entries) != 3 {
		t.Errorf("Expected 3 audit entries, got %d", len(entries))
	}

	// Check last entry (most recent)
	lastEntry := entries[len(entries)-1]
	if lastEntry.AgentID != "agent-1" {
		t.Errorf("Expected agent-1, got %s", lastEntry.AgentID)
	}

	if lastEntry.ToolName != "network_request" {
		t.Errorf("Expected network_request, got %s", lastEntry.ToolName)
	}

	if lastEntry.Allowed {
		t.Error("network_request should be denied")
	}
}

func TestDefaultToolPolicy(t *testing.T) {
	policy := DefaultToolPolicy()

	// Check that Buckley's actual capabilities are defined
	shellTools := policy.GetAllowedTools("shell_execution")
	if len(shellTools) == 0 {
		t.Error("shell_execution should have allowed tools in default policy")
	}
	if !policy.IsToolAllowed("shell_execution", "shell") {
		t.Error("shell_execution should allow 'shell' tool")
	}

	fileTools := policy.GetAllowedTools("file_access")
	if len(fileTools) == 0 {
		t.Error("file_access should have allowed tools in default policy")
	}
	if !policy.IsToolAllowed("file_access", "file") {
		t.Error("file_access should allow 'file' tool")
	}

	gitTools := policy.GetAllowedTools("git_access")
	if len(gitTools) == 0 {
		t.Error("git_access should have allowed tools in default policy")
	}
	if !policy.IsToolAllowed("git_access", "git") {
		t.Error("git_access should allow 'git' tool")
	}

	// Check code analysis capabilities (read-only)
	analysisTools := policy.GetAllowedTools("code_analysis")
	if len(analysisTools) == 0 {
		t.Error("code_analysis should have allowed tools in default policy")
	}
	expectedAnalysisTools := []string{"search", "semantic_search", "code_index", "navigation", "quality"}
	for _, tool := range expectedAnalysisTools {
		if !policy.IsToolAllowed("code_analysis", tool) {
			t.Errorf("code_analysis should allow '%s' tool", tool)
		}
	}

	// Check that admin allows everything
	if !policy.IsToolAllowed("admin", "any_tool") {
		t.Error("admin should allow any tool in default policy")
	}
	if !policy.IsToolAllowed("admin", "shell") {
		t.Error("admin should allow 'shell' tool")
	}

	// Check that read_only doesn't allow dangerous operations
	if policy.IsToolAllowed("read_only", "shell") {
		t.Error("read_only should NOT allow 'shell' tool")
	}
	if policy.IsToolAllowed("read_only", "file") {
		t.Error("read_only should NOT allow 'file' tool (can write)")
	}
	if !policy.IsToolAllowed("read_only", "search") {
		t.Error("read_only should allow 'search' tool (safe)")
	}
}
