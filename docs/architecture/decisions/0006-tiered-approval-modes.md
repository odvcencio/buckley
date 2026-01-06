# ADR 0006: Tiered Approval Modes

## Status

Accepted

## Context

Agents need varying levels of autonomy depending on:
- User trust level and risk tolerance
- Task sensitivity (editing production configs vs. running tests)
- Environment (local dev vs. shared server)

Requirements:
- Progressive autonomy levels from paranoid to fully autonomous
- Clear semantics for each level
- Path-based and tool-based allowlists/denylists
- Safe defaults that prevent accidental damage

Options considered:
1. **Binary (approve all / deny all)** - Too coarse, unusable in practice
2. **Per-operation prompts** - Exhausting for users, breaks flow
3. **Tiered modes with overrides** - Balance between safety and usability

## Decision

Implement four approval modes with increasing autonomy:

```go
const (
    ModeAsk  Mode = iota  // Explicit approval for all writes and commands
    ModeSafe              // Read anything, write to workspace only
    ModeAuto              // Full workspace access, approval for external ops
    ModeYolo              // Full autonomy (dangerous)
)
```

### Mode Semantics

| Mode | Reads | Workspace Writes | Shell | Network | External Writes |
|------|-------|------------------|-------|---------|-----------------|
| Ask  | ✓     | Prompt           | Prompt| Prompt  | Prompt          |
| Safe | ✓     | ✓                | Prompt| Prompt  | Prompt          |
| Auto | ✓     | ✓                | ✓     | Prompt  | Prompt          |
| Yolo | ✓     | ✓                | ✓     | ✓       | ✓               |

### Override Configuration

```yaml
approval:
  mode: safe
  trusted_paths:
    - ~/projects/trusted-repo
  denied_paths:
    - ~/.ssh
    - ~/.aws
    - /etc
  allowed_tools:
    - read_file
    - list_files
  auto_approve_patterns:
    - "go test"
    - "npm run build"
```

## Consequences

### Positive
- Clear mental model for users
- Safe default (ModeSafe) prevents accidental damage
- Granular overrides for power users
- Auto-approve patterns reduce friction for safe operations

### Negative
- Mode selection requires understanding tradeoffs
- Yolo mode is genuinely dangerous
- Pattern matching for auto-approve can be gamed

### Security Boundaries
- Denied paths are enforced even in Yolo mode
- Sensitive directories (~/.ssh, ~/.aws, /etc) denied by default
- Network operations always require explicit opt-in except in Yolo
