# ACP Tool Access Policy

**Last Updated**: 2025-11-19
**Purpose**: Define capability-based access control for Buckley's tools

## Overview

The tool access policy maps **capabilities** (granted to agents via JWT tokens) to **tools** (actual Buckley builtin tools). This implements the principle of least privilege: agents only get access to tools they explicitly need.

## Policy Design Principles

1. **Use actual tool names**: Policy uses Buckley's real tool names (not generic placeholders)
2. **Risk-based grouping**: Capabilities grouped by risk level (shell > file > git > read-only)
3. **No redundancy**: Each tool appears in the capability that best describes its primary use
4. **Least privilege**: Agents get minimal permissions needed for their role
5. **Admin bypass**: Special `admin` capability grants access to everything

## Capabilities & Tools

### High Risk

#### `admin` - Full System Access
**Tools**: `*` (all tools)
**Use Case**: System administrators, trusted automation
**Risk Level**: ⚠️ **CRITICAL**

#### `shell_execution` - Shell Command Execution
**Tools**:
- `shell` - Run arbitrary shell commands

**Use Case**: Build automation, deployment scripts
**Risk Level**: ⚠️ **HIGH**
**Notes**: Most dangerous capability; can execute any command on the system

### Medium Risk

#### `file_access` - File System Operations
**Tools**:
- `file` - Read, write, delete, copy, move files
- `terminal_editor` - Interactive file editing

**Use Case**: File manipulation, code editing
**Risk Level**: ⚠️ **MEDIUM**
**Notes**: Can modify/delete files but cannot execute arbitrary code

#### `git_access` - Version Control Operations
**Tools**:
- `git` - All git operations (clone, commit, push, pull, branch, merge)
- `merge` - Code merging

**Use Case**: Version control, code collaboration
**Risk Level**: ⚠️ **MEDIUM**
**Notes**: Can push code to repositories, create commits

#### `code_modification` - Code Refactoring & Editing
**Tools**:
- `refactoring` - Automated code refactoring
- `terminal_editor` - Edit files
- `file` - File operations

**Use Case**: Code improvement, restructuring
**Risk Level**: ⚠️ **MEDIUM**
**Notes**: Overlaps with file_access but semantically distinct

### Low Risk

#### `code_analysis` - Code Reading & Analysis (Read-Only)
**Tools**:
- `search` - Text-based code search
- `semantic_search` - Semantic code search with embeddings
- `code_index` - Build/query code index
- `navigation` - Navigate codebase structure
- `quality` - Code quality analysis

**Use Case**: Code review, understanding codebase
**Risk Level**: ✅ **LOW**
**Notes**: Read-only operations, safe for untrusted agents

#### `testing` - Test Execution
**Tools**:
- `testing` - Run test suites

**Use Case**: Quality assurance, CI/CD
**Risk Level**: ✅ **LOW**
**Notes**: Can reveal vulnerabilities but doesn't modify code

#### `documentation` - Documentation Generation
**Tools**:
- `documentation` - Generate/manage documentation

**Use Case**: API docs, README generation
**Risk Level**: ✅ **LOW**

#### `web_access` - Web Browsing
**Tools**:
- `browser` - Web browsing capabilities

**Use Case**: Research, documentation lookup
**Risk Level**: ✅ **LOW** (if sandboxed)
**Notes**: Can make external HTTP requests

#### `task_management` - TODO Management
**Tools**:
- `todo` - Manage TODO items and checklists

**Use Case**: Task tracking, project planning
**Risk Level**: ✅ **LOW**

#### `agent_orchestration` - Sub-Agent Management
**Tools**:
- `delegate` - Spawn sub-agents
- `skill_activation` - Activate skills

**Use Case**: Multi-agent workflows, skill-based routing
**Risk Level**: ⚠️ **MEDIUM**
**Notes**: Sub-agents inherit parent's capabilities (or subset)

#### `data_operations` - Data File Operations
**Tools**:
- `excel` - Excel file operations

**Use Case**: Data analysis, spreadsheet manipulation
**Risk Level**: ✅ **LOW**

#### `read_only` - Safe Read-Only Operations
**Tools**:
- `search` - Search code
- `semantic_search` - Semantic search
- `code_index` - Query code index
- `navigation` - Navigate codebase
- `quality` - Quality analysis
- `browser` - Web browsing (read-only)

**Use Case**: Auditing, monitoring, non-privileged agents
**Risk Level**: ✅ **SAFE**
**Notes**: No modification capabilities whatsoever

## Common Agent Profiles

### Builder Agent (Full Development Access)
```go
capabilities := []string{
    "shell_execution",
    "file_access",
    "git_access",
    "code_modification",
    "testing",
    "documentation",
}
```

### Reviewer Agent (Read-Only Analysis)
```go
capabilities := []string{
    "code_analysis",
    "testing",
    "read_only",
}
```

### Research Agent (Web + Code Reading)
```go
capabilities := []string{
    "web_access",
    "code_analysis",
    "read_only",
}
```

### Deployment Agent (CI/CD Pipeline)
```go
capabilities := []string{
    "shell_execution",
    "git_access",
    "testing",
}
```

### Junior Agent (Minimal Permissions)
```go
capabilities := []string{
    "read_only",
    "task_management",
}
```

## Implementation Notes

### Tool Names Match Buckley's Codebase
All tool names in the policy correspond to actual files in `pkg/tool/builtin/`:
- ✅ `shell` → `pkg/tool/builtin/shell.go`
- ✅ `file` → `pkg/tool/builtin/file.go`
- ✅ `git` → `pkg/tool/builtin/git.go`
- ✅ `search` → `pkg/tool/builtin/search.go`
- ✅ `semantic_search` → `pkg/tool/builtin/semantic_search.go`
- etc.

### Removed Non-Existent Capabilities
Previous version included capabilities for tools that don't exist:
- ❌ `cloud_access` (create_instance, delete_instance) - Buckley has no cloud tools
- ❌ `database_access` (query_database, create_table) - Buckley has no database tools

### Eliminated Redundancies
Previous version had duplicated tools across capabilities:
- ❌ `read_file` in both `file_access` AND `read_only`
- ❌ `list_directory` in both `file_access` AND `read_only`

New version: each tool appears in ONE primary capability, but read-only capability references safe subset.

## Usage Examples

### Check if agent can use a tool:
```go
ctx := ContextWithClaims(ctx, &Claims{
    AgentID:      "agent-1",
    Capabilities: []string{"code_analysis", "testing"},
})

approver := NewToolApprover(DefaultToolPolicy())

// Allowed
err := approver.CheckToolAccess(ctx, "search") // ✅ nil

// Denied
err := approver.CheckToolAccess(ctx, "shell")  // ❌ ErrToolNotAllowed
```

### Get all tools an agent can use:
```go
tools := approver.GetAllowedToolsForAgent(ctx)
// Returns: ["search", "semantic_search", "code_index", "navigation", "quality", "testing"]
```

### Audit tool access:
```go
approver.CheckToolAccess(ctx, "shell") // Denied
entries := approver.GetAuditLog("agent-1", 10)
// entries[0].Allowed == false
// entries[0].ToolName == "shell"
// entries[0].Reason == "no capability grants access"
```

## Security Considerations

1. **Always validate**: Never trust client-provided tool names
2. **Audit everything**: All tool access attempts are logged
3. **Default deny**: If no capability grants access, deny
4. **Admin carefully**: Only grant `admin` to trusted systems
5. **Review regularly**: Audit logs should be reviewed for anomalies

## Future Enhancements

- [ ] Time-based restrictions (allow tools only during business hours)
- [ ] Rate limiting per tool (prevent abuse)
- [ ] Dynamic policies (adjust based on agent behavior)
- [ ] Tool parameter validation (not just tool names)
- [ ] Deny lists (explicit blocking even if capability grants access)

---

**See Also**:
- `pkg/acp/security/tool_approval.go` - Implementation
- `pkg/acp/security/tool_approval_test.go` - Test cases
- `docs/ACP_SECURITY_AUDIT.md` - Security audit results
