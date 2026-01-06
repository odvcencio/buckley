# Agent Communication Protocol (ACP)

**Purpose**: LSP-based integration for external editors (Zed, VS Code, etc.)

---

## What ACP Does

ACP lets editors talk to Buckley via the Language Server Protocol. You get AI assistance directly in your editor without switching to a terminal.

**Not in scope**: Multi-agent orchestration, agent swarms, P2P mesh. That's a separate system.

---

## Architecture

```
┌──────────────┐     stdio/LSP     ┌──────────────┐
│              │ ←───────────────→ │              │
│  Zed Editor  │                   │  Buckley     │
│              │                   │  LSP Bridge  │
└──────────────┘                   └──────────────┘
```

The LSP bridge is a plugin that:
1. Speaks LSP over stdio to the editor
2. Translates requests to Buckley's internal API
3. Streams responses back to the editor

---

## LSP Extensions

Standard LSP plus custom methods in the `$/buckley/*` namespace:

### Phase 1: Text Q&A

```typescript
// Request: buckley/ask
interface AskRequest {
  question: string;
  context?: {
    activeFile?: string;
    selection?: Range;
    openFiles?: string[];
  };
}

// Response
interface AskResponse {
  answer: string;
  references?: CodeReference[];
}
```

Ask questions about code. Buckley reads context from open files.

### Phase 2: Task Streaming

```typescript
// Request: buckley/executeTask
interface ExecuteTaskRequest {
  task: string;
  context?: SessionContext;
}

// Notification: $/buckley/taskProgress
interface TaskProgressNotification {
  taskId: string;
  status: "pending" | "in_progress" | "completed" | "failed";
  progress?: number;
  message?: string;
  toolExecutions?: ToolExecution[];
}
```

Execute multi-step tasks with real-time progress in the editor.

### Phase 3: Tool Approvals

```typescript
// Request: buckley/approveTool
interface ToolApprovalRequest {
  tool: string;
  parameters: Record<string, any>;
  risk: "low" | "medium" | "high" | "destructive";
}

// Response
interface ToolApprovalResponse {
  approved: boolean;
  remember?: boolean;  // Trust this tool going forward
}
```

Editor prompts you before Buckley runs risky operations.

---

## Configuration

```yaml
# .buckley/config.yaml
acp:
  enabled: true
  lsp:
    transport: stdio  # or tcp
    port: 4489        # if tcp
```

---

## Editor Setup

### Zed

Install the Buckley extension (when available). It auto-detects local Buckley installations.

### VS Code

Install the Buckley extension from the marketplace. Configure the path to your Buckley binary if not in PATH.

### Other Editors

Any LSP-capable editor works. Point it at `buckley lsp` as the language server.

---

## Implementation Status

| Phase | Feature | Status |
|-------|---------|--------|
| 1 | Text Q&A | 🚧 In progress |
| 2 | Task streaming | 📋 Planned |
| 3 | Tool approvals | 📋 Planned |
| 4 | Deep integration | 📋 Future |

---

## Related

- [Multi-Agent Orchestration](./ORCHESTRATION.md) - Agent swarms and coordination
- [CLI Reference](./CLI.md) - Terminal interface
