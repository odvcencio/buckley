# Agent Communication Protocol (ACP)

**Purpose**: Zed ACP integration for editor agents, with an optional LSP bridge for editors that only speak LSP.

---

## What ACP Does

ACP lets editors talk to Buckley directly for agent sessions, tool calls, and editor context without switching to a terminal. Buckley supports ACP over stdio via `buckley acp` and can expose an ACP gRPC server for editor bridges. Editors that only speak LSP can use `buckley lsp` as an adapter.

**Not in scope**: Multi-agent orchestration, agent swarms, P2P mesh. That's a separate system.

---

## Architecture

```
┌──────────────┐  ACP (stdio/gRPC)  ┌──────────────┐
│              │ ←───────────────→ │              │
│  Editor      │                   │  Buckley     │
│  (Zed ACP)   │                   │  ACP Server  │
└──────────────┘                   └──────────────┘
```

The optional LSP bridge adapts ACP to LSP and:
1. Speaks LSP over stdio to the editor
2. Translates requests into ACP calls
3. Streams responses back to the editor

---

## LSP Bridge Extensions

These `$/buckley/*` methods are specific to the LSP bridge, not the ACP protocol itself.

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
# ~/.buckley/config.yaml
acp:
  listen: "127.0.0.1:50051"
  event_store: sqlite
  allow_insecure_local: false
```

---

## Editor Setup

### Zed

Install the Buckley extension (when available) and configure it to launch `buckley acp` (see `zed-settings.json`).

### VS Code

Install the Buckley extension if ACP support is available, or run the LSP bridge with `buckley lsp`.

### Other Editors

ACP-capable editors should launch `buckley acp` over stdio. LSP-only editors can point at `buckley lsp`.

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
