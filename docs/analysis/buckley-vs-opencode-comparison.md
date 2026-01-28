# Buckley vs OpenCode Comparison (Code-Validated Where Noted)

This revision validates Buckley claims against the current Buckley codebase and
validates selected OpenCode claims against `github.com/anomalyco/opencode`.
OpenTUI renderer internals are validated against `github.com/sst/opentui` because
OpenCode uses OpenTUI for its TUI. Anything without code evidence is called out
as unverified.

## Scope and Method

- Verified Buckley behavior by reading source in `pkg/` and `cmd/`.
- Verified OpenCode behavior by reading source under `packages/opencode/src/`.
- Verified OpenTUI renderer internals by reading `github.com/sst/opentui`.
- Avoided relying on docs or README claims when code evidence exists.
- Unverified items are explicitly listed near the end.

## Buckley Capabilities Verified in Code

### RLM runtime with sub-agents and checkpoints

Evidence:
```go
// pkg/rlm/runtime.go
// Runtime is the RLM execution engine.
type Runtime struct {
	selector    *ModelSelector
	dispatcher  *Dispatcher
	scratchpad  *Scratchpad
	resultCodec *toon.Codec
}

// Checkpoint captures the state of an RLM execution for resumption.
type Checkpoint struct {
	ID      string
	Task    string
	Answer  Answer
	History []IterationHistory
}

func (r *Runtime) CreateCheckpoint(task string, answer *Answer) (*Checkpoint, error)
func (r *Runtime) ResumeFromCheckpoint(ctx context.Context, checkpoint *Checkpoint) (*Answer, error)
```
```go
// pkg/rlm/subagent.go
// SubAgent executes delegated tasks with tool access.
type SubAgent struct {
	model        string
	allowedTools map[string]struct{}
}
```
```go
// pkg/execution/rlm_strategy.go
// RLMStrategy uses the coordinator/sub-agent pattern for execution.
type RLMStrategy struct {
	runtime *rlm.Runtime
}
```

### Model routing and execution modes

Evidence (provider routing + hooks):
```go
// pkg/model/manager.go
// Manager manages provider routing and model metadata.
type Manager struct {
	providers    map[string]Provider
	routingHooks *RoutingHooks
}

func (m *Manager) RoutingHooks() *RoutingHooks
func (m *Manager) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error)
```
```go
// pkg/model/manager.go
func (m *Manager) providerIDFromRouting(modelID string) (string, bool) {
	for prefix, providerID := range m.config.Providers.ModelRouting {
		if strings.HasPrefix(modelID, prefix) {
			return providerID, true
		}
	}
	return "", false
}
```
Evidence (sub-agent model selection is single-model):
```go
// pkg/rlm/router.go
// Simplified: no tiers, just a single configured model or fallback to execution model.
type ModelSelector struct {
	model string
}

func (s *ModelSelector) Select() string
```

### TOON encoding for tool results (compact serialization)

Evidence:
```go
// pkg/encoding/toon/codec.go
// Marshal encodes v into TOON (or JSON when disabled).
func (c *Codec) Marshal(v any) ([]byte, error) {
	if !c.useToon || v == nil {
		return json.Marshal(v)
	}
	encoded, err := gotoon.Encode(v)
	if err != nil {
		return nil, fmt.Errorf("toon encode: %w", err)
	}
	return []byte(encoded), nil
}
```
```go
// pkg/tool/tool.go
var resultCodec = toon.New(true)

func ToJSON(r *builtin.Result) (string, error) {
	data, err := resultCodec.Marshal(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
```
```go
// pkg/execution/toolrunner_adapter.go
// TOON is great for wire efficiency but should never appear in user-facing content.
content := strings.TrimSpace(toon.SanitizeOutput(result.Content))
```

### Custom retained-mode TUI (dirty tracking + partial redraw)

Evidence:
```go
// pkg/ui/runtime/buffer.go
// Buffer tracks which cells changed (dirty tracking).
type Buffer struct {
	cells      []Cell
	dirty      []bool
	dirtyCount int
	dirtyRect  Rect
}
```
```go
// pkg/ui/tui/app_widget.go
if buf.IsDirty() {
	dirtyCount := buf.DirtyCount()
	w, h := buf.Size()
	totalCells := w * h
	if dirtyCount > totalCells/2 {
		// Full redraw
	} else {
		// Partial redraw - only dirty cells
		buf.ForEachDirtyCell(func(x, y int, cell runtime.Cell) {
			a.backend.SetContent(x, y, cell.Rune, nil, cell.Style)
		})
	}
	buf.ClearDirty()
}
```
```go
// pkg/ui/backend/backend.go
// Backend abstraction for real terminals and simulation backends (testing).
type Backend interface {
	Init() error
	SetContent(x, y int, mainc rune, comb []rune, style Style)
	Show()
	PollEvent() terminal.Event
}
```

### Composable tool middleware (chainable execution pipeline)

Evidence:
```go
// pkg/tool/defaults.go
chain := []Middleware{
	PanicRecovery(),
	ToastNotifications(cfg.ToastManager),
	Validation(cfg.ValidationConfig, cfg.OnValidationError),
	ResultSizeLimit(cfg.MaxResultBytes, "\n...[truncated]"),
	Retry(cfg.RetryConfig),
	Timeout(cfg.DefaultTimeout, cfg.PerToolTimeouts),
	Progress(cfg.ProgressManager, longRunning),
	FileChangeTracking(cfg.FileWatcher),
}
```
```go
// pkg/tool/registry.go
middlewares := make([]Middleware, 0, len(r.middlewares)+3)
middlewares = append(middlewares, r.telemetryMiddleware(), Hooks(r.hooks), r.approvalMiddleware())
middlewares = append(middlewares, r.middlewares...)
r.executor = Chain(middlewares...)(base)
```

### Tiered approval modes and tool gating

Evidence (modes):
```go
// pkg/config/config.go
// Mode determines the default approval level: ask, safe, auto, yolo
Mode string `yaml:"mode"`
```
Evidence (mission control gating):
```go
// pkg/tool/middleware_approval.go
func (r *Registry) approvalMiddleware() Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if r == nil || ctx == nil || !r.shouldGateChanges() {
				return next(ctx)
			}
			switch strings.TrimSpace(ctx.ToolName) {
			case "write_file":
				return r.executeWithMissionWrite(ctx.Params, func(params map[string]any) (*builtin.Result, error) {
					ctx.Params = params
					return next(ctx)
				})
			case "apply_patch":
				return r.executeWithMissionPatch(ctx.Params, func(params map[string]any) (*builtin.Result, error) {
					ctx.Params = params
					return next(ctx)
				})
			default:
				return next(ctx)
			}
		}
	}
}
```
Evidence (capability-based tool approval for agents):
```go
// pkg/coordination/security/tool_approval.go
// ToolApprover enforces tool access policies for agents.
type ToolApprover struct {
	policy *ToolPolicy
}

func (ta *ToolApprover) CheckToolAccess(ctx context.Context, tool string) error
```

### Budget-aware tracking and alerts

Evidence:
```go
// pkg/cost/tracker.go
// BudgetStatus represents the current budget status.
type BudgetStatus struct {
	SessionBudget  float64
	DailyBudget    float64
	MonthlyBudget  float64
	SessionPercent float64
	DailyPercent   float64
	MonthlyPercent float64
}
```
```go
// pkg/cost/alerts.go
func (bn *BudgetNotifier) Check(status *BudgetStatus) {
	// ... emits info/warning/critical/exceeded alerts
}
```
```go
// pkg/rlm/runtime.go
// BudgetStatus tracks resource consumption.
type BudgetStatus struct {
	TokensUsed    int
	TokensPercent float64
	Warning       string
}
```

### Event-driven telemetry (pub/sub hub)

Evidence:
```go
// pkg/telemetry/telemetry.go
// Publish notifies all subscribers of an event. Non-blocking; drops if buffer full.
func (h *Hub) Publish(event Event) {
	// ...
}

// Subscribe returns a channel that will receive future events.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	// ...
}
```

### Plan workflow with persistence and checkpoints

Evidence (plan persistence):
```go
// pkg/orchestrator/plan_store.go
// FilePlanStore persists plans as JSON/Markdown files under docs/plans.
func (s *FilePlanStore) SavePlan(plan *Plan) error
func (s *FilePlanStore) LoadPlan(planID string) (*Plan, error)
```
```go
// pkg/orchestrator/planner.go
// SavePlan persists the given plan using the configured plan store.
func (p *Planner) SavePlan(plan *Plan) error
```
Evidence (TODO checkpoints):
```go
// pkg/tool/builtin/todo.go
// TodoCheckpointData represents a checkpoint snapshot
type TodoCheckpointData struct {
	SessionID          string
	CheckpointType     string
	TodoCount          int
	CompletedCount     int
	ConversationTokens int
}
```
Evidence (RLM checkpoints):
```go
// pkg/rlm/runtime.go
func (r *Runtime) CreateCheckpoint(task string, answer *Answer) (*Checkpoint, error)
```

### Context compaction thresholds (defaults)

Evidence:
```go
// pkg/conversation/compaction.go
const (
	defaultClassicAutoTrigger = 0.75
	defaultRLMAutoTrigger     = 0.85
	defaultCompactionRatio    = 0.45
)
```

### LSP integration (editor bridge)

Evidence:
```go
// cmd/buckley/lsp.go
// runLSPCommand starts the LSP bridge server on stdio
func runLSPCommand(args []string) error
```
```go
// pkg/acp/lsp/bridge.go
// Bridge implements LSP server that bridges to ACP coordinator
type Bridge struct {
	// ...
}
```

### MCP integration (client + tool adapter)

Evidence:
```go
// pkg/mcp/client.go
// Package mcp implements the Model Context Protocol client for connecting to external tool servers.
package mcp
```
```go
// pkg/mcp/tool_adapter.go
func RegisterMCPTools(manager *Manager, register func(name string, tool any))
```

### Remote server and CLI control

Evidence:
```go
// cmd/buckley/serve.go
// Start local HTTP/WebSocket server
func runServeCommand(args []string) error
```
```go
// cmd/buckley/remote.go
// Remote session operations (attach, sessions, tokens, login, console)
func runRemoteCommand(args []string) error
```

### Vision-capable model layer

Evidence:
```go
// pkg/model/types.go
// ContentPart represents a part of multimodal content (text or image)
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}
```
```go
// pkg/model/manager.go
// DescribeImage uses a vision model to describe an image
func (m *Manager) DescribeImage(ctx context.Context, imageURL string) (string, error)
```

## OpenCode Capabilities Verified in Code

### TUI stack (OpenTUI / Solid)

Evidence:
```ts
// packages/opencode/src/cli/cmd/tui/app.tsx
import { render, useKeyboard, useRenderer, useTerminalDimensions } from "@opentui/solid"
import { TextAttributes } from "@opentui/core"
```

### LSP integration (tool + server)

Evidence:
```ts
// packages/opencode/src/tool/lsp.ts
export const LspTool = Tool.define("lsp", {
  // ...
  execute: async (args, ctx) => {
    // ...
    const available = await LSP.hasClients(file)
    if (!available) {
      throw new Error("No LSP server available for this file type.")
    }
```
```ts
// packages/opencode/src/server/server.ts
.get("/lsp", describeRoute({ operationId: "lsp.status" }), async (c) => {
  return c.json(await LSP.status())
})
```

### MCP integration (SDK + CLI + server)

Evidence:
```ts
// packages/opencode/src/mcp/index.ts
import { Client } from "@modelcontextprotocol/sdk/client/index.js"
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js"
import { SSEClientTransport } from "@modelcontextprotocol/sdk/client/sse.js"
```
```ts
// packages/opencode/src/cli/cmd/mcp.ts
export const McpCommand = cmd({
  command: "mcp",
  describe: "manage MCP (Model Context Protocol) servers",
  // ...
})
```
```ts
// packages/opencode/src/server/server.ts
.get("/mcp", describeRoute({ operationId: "mcp.status" }), async (c) => {
  return c.json(await MCP.status())
})
.post("/mcp/:name/connect", describeRoute({ operationId: "mcp.connect" }), async (c) => {
  const { name } = c.req.valid("param")
  await MCP.connect(name)
  return c.json(true)
})
```

### Client/server architecture and remote TUI control

Evidence:
```ts
// packages/opencode/src/cli/cmd/serve.ts
export const ServeCommand = cmd({
  command: "serve",
  describe: "starts a headless opencode server",
  handler: async (args) => {
    const server = Server.listen(opts)
    console.log(`opencode server listening on http://${server.hostname}:${server.port}`)
  },
})
```
```ts
// packages/opencode/src/server/tui.ts
export const TuiRoute = new Hono()
  .get("/next", describeRoute({ operationId: "tui.control.next" }), async (c) => {
    const req = await request.next()
    return c.json(req)
  })
  .post("/response", describeRoute({ operationId: "tui.control.response" }), async (c) => {
    response.push(body)
    return c.json(true)
  })
```

### Permission system (allow/deny/ask + per-tool prompts)

Evidence:
```ts
// packages/opencode/src/permission/next.ts
export const Action = z.enum(["allow", "deny", "ask"])
export const Request = z.object({
  permission: z.string(),
  patterns: z.string().array(),
  // ...
})
```
```ts
// packages/opencode/src/tool/lsp.ts
await ctx.ask({
  permission: "lsp",
  patterns: ["*"],
  always: ["*"],
  metadata: {},
})
```

### Prompt caching (ephemeral cache control)

Evidence:
```ts
// packages/opencode/src/provider/transform.ts
const providerOptions = {
  anthropic: { cacheControl: { type: "ephemeral" } },
  openrouter: { cacheControl: { type: "ephemeral" } },
  bedrock: { cachePoint: { type: "ephemeral" } },
  openaiCompatible: { cache_control: { type: "ephemeral" } },
}
// ...
msgs = applyCaching(msgs, model.providerID)
```

### Image paste support in TUI prompt

Evidence:
```ts
// packages/opencode/src/cli/cmd/tui/component/prompt/index.tsx
async function pasteImage(file: { filename?: string; content: string; mime: string }) {
  const part = {
    type: "file" as const,
    mime: file.mime,
    filename: file.filename,
    url: `data:${file.mime};base64,${file.content}`,
  }
  // ...
}
```
```ts
// packages/opencode/src/cli/cmd/tui/component/prompt/index.tsx
if (file.type.startsWith("image/")) {
  const content = await file.arrayBuffer().then((buffer) => Buffer.from(buffer).toString("base64"))
  if (content) {
    await pasteImage({ filename: file.name, mime: file.type, content })
    return
  }
}
```

### Multi-provider support

Evidence:
```ts
// packages/opencode/src/provider/provider.ts
const BUNDLED_PROVIDERS: Record<string, (options: any) => SDK> = {
  "@ai-sdk/anthropic": createAnthropic,
  "@ai-sdk/openai": createOpenAI,
  "@openrouter/ai-sdk-provider": createOpenRouter,
  "@ai-sdk/google": createGoogleGenerativeAI,
  // ...
}
```

### Agent profiles with subagent mode

Evidence:
```ts
// packages/opencode/src/agent/agent.ts
export const Info = z.object({
  mode: z.enum(["subagent", "primary", "all"]),
  // ...
})
// ...
general: { name: "general", mode: "subagent", native: true },
```

### OpenTUI renderer internals (diff-based flush + hit grid)

Evidence (diff between current and next buffers):
```zig
// packages/core/src/zig/renderer.zig
const currentCell = self.currentRenderBuffer.get(x, y);
const nextCell = self.nextRenderBuffer.get(x, y);

if (!force) {
    const charEqual = currentCell.?.char == nextCell.?.char;
    const attrEqual = currentCell.?.attributes == nextCell.?.attributes;

    if (charEqual and attrEqual and
        buf.rgbaEqual(currentCell.?.fg, nextCell.?.fg, colorEpsilon) and
        buf.rgbaEqual(currentCell.?.bg, nextCell.?.bg, colorEpsilon))
    {
        // Skip unchanged cells
        continue;
    }
}
```
Evidence (hit grid for mouse hit testing with clipping):
```zig
// packages/core/src/zig/renderer.zig
// The hit grid is a screen-sized array where each cell stores the renderable ID
// at that position. Mouse events query checkHit(x, y) to find which element to
// dispatch to.
// ...
// Scissor clipping: The hitScissorStack mirrors overflow:hidden regions.
currentHitGrid: []u32,
nextHitGrid: []u32,
hitScissorStack: std.ArrayListUnmanaged(buf.ClipRect),
```
Evidence (renderables mark clean and add to hit grid):
```ts
// packages/core/src/Renderable.ts
public render(buffer: OptimizedBuffer, deltaTime: number): void {
  // ...
  this.renderSelf(renderBuffer, deltaTime)
  this.markClean()
  this._ctx.addToHitGrid(this.x, this.y, this.width, this.height, this.num)
  // ...
}
```
Evidence (buffered renderables can skip repaint):
```ts
// packages/core/src/renderables/LineNumberRenderable.ts
if (this.buffered && !this.isDirty && !scrollChanged) {
  return
}
```

## Buckley Gaps and Limitations Visible in Code

These are gaps or constraints observed directly in the Buckley codebase.

- RLM tiering and escalation are not implemented: RLM config and dispatcher
  are explicitly simplified to a single sub-agent model (no tiers).
  Evidence:
  ```go
  // pkg/rlm/config.go
  // Simplified from the original 5-tier system - all sub-agents use the same model.
  type SubAgentConfig struct {
  	Model string
  }

  // pkg/rlm/batch.go
  // Simplified: no weight tiers, all sub-agents use the same model.
  type SubTask struct {
  	ID string
  }
  ```
- Prompt caching is absent: no `cache_control` or prompt-cache handling found in
  Buckley model request paths. OpenCode applies ephemeral cache control in
  `packages/opencode/src/provider/transform.ts`.
- MCP wiring is incomplete in the CLI/runtime: `pkg/mcp` provides a client and
  adapter, but no call sites were found in `cmd/` or other `pkg/` packages to
  register MCP tools into a registry. OpenCode exposes MCP via CLI and server
  endpoints (see `packages/opencode/src/cli/cmd/mcp.ts` and `server/server.ts`).
- TUI drag/drop or attachment capture is not implemented: paste currently inserts
  raw text into the input; file picker injects file content as a system message.
- TOON savings are not benchmarked in-repo: TOON exists, but no token reduction
  measurements or benchmarks were found.

## OpenCode Gaps or Missing Evidence (From Code Search)

- No coordinator/runtime checkpointing system found; only prompt text mentions
  "checkpoint" (see `packages/opencode/src/session/prompt/*.txt`).
- No TOON or tool-output compaction references found under
  `packages/opencode/src/`.
- Image drag/drop handling was not located; only a tip string mentions it.
- Community metrics (stars/users) are external to code and unverified here.

## Kilo Code Prompt Caching (External)

Kilo Code "tool caching" is described in external PRs and vendor docs. OpenCode
already sets ephemeral cache control on system and tail messages for certain
providers (see `packages/opencode/src/provider/transform.ts`). Buckley currently
has no prompt caching implementation; if we pursue this, it should be implemented
and benchmarked in Buckley code.

## TUI Takeaways for Buckley (From OpenTUI/OpenCode)

These are architecture patterns we can borrow without adopting OpenCode UI/UX.

- Hit grid for mouse dispatch: OpenTUI keeps a per-cell renderable ID grid and
  uses it for hit testing (see `packages/core/src/zig/renderer.zig`). This could
  simplify precise mouse routing in Buckley, especially for sidebar controls and
  scrollable regions.
- Hit grid scissor clipping: OpenTUI tracks a hit scissor stack to avoid
  dispatching clicks to off-screen or clipped content. That would help Buckley
  avoid accidental clicks on hidden chat rows in the scroll view.
- Render diffing at flush time: OpenTUI compares current vs next buffers and
  skips unchanged cells before emitting ANSI output. Buckley already does
  dirty-cell tracking, but diffing could provide a second safety net for
  correctness and debugging.
- Buffered renderables with dirty checks: OpenTUI can skip rendering buffered
  widgets if they are not dirty and have not scrolled. Buckley widgets could
  adopt similar per-widget caching for expensive views.

## Concrete Buckley Implementation Tasks (Based on Evidence)

### Prompt caching (input token savings)

Goal: add optional prompt caching for providers that support it, similar to
OpenCode `ProviderTransform.applyCaching`.

1) Add config for caching
- Add a new config struct in `pkg/config/config.go` (for example `PromptCacheConfig`)
  with fields like `Enabled bool`, `Providers []string`, `Scope string`.
- Plumb config through init paths (for example `cmd/buckley/main.go` and
  `pkg/ui/tui/controller.go`) so it is available in `model.Manager`.

2) Add provider options on messages
- Extend `pkg/model/types.go` to allow provider-specific options on messages
  (for example `ProviderOptions map[string]any`).
- Update `pkg/model/provider_anthropic.go` message structs to carry provider
  options so they can be serialized when present.

3) Apply caching transform
- Add a transform in `pkg/model/manager.go` similar to `applyProviderTransforms`
  that sets cache control options on the first system message and the last
  N messages when enabled.
- For OpenRouter, ensure the JSON payload includes provider options for
  cache control; for Anthropic, set the provider-specific field in the request.

Done when:
- A unit test verifies that the serialized request includes cache control
  options on the system message and tail messages when caching is enabled.
- A disabled config yields no cache control fields in the JSON payload.

### MCP wiring (tool registration and config)

Goal: make `pkg/mcp` usable from CLI/TUI by registering MCP tools.

1) Add MCP config
- Add MCP server definitions to `pkg/config/config.go` (struct for name, command,
  args, env). Avoid the JSON-only `ParseMCPConfig` path.

2) Initialize manager and register tools
- In `cmd/buckley/main.go` and `pkg/ui/tui/controller.go` (buildRegistry), create
  an `mcp.Manager`, add servers from config, connect, and call
  `mcp.RegisterMCPTools(manager, registry.Register)`.

3) Surface status
- Add a small UI or command entry to show MCP server status using
  `mcp.Manager.ListServers()` and `ListConnectedServers()`.

Done when:
- `registry.List()` contains `mcp__<server>__<tool>` entries after startup.
- A basic test validates MCP tools are registered when config is present.

### TUI attachments and drag-drop style input

Goal: allow file selection or paste to attach images and files to the next
message without adopting OpenCode UI.

1) Track pending attachments in controller
- Add pending attachment state to `pkg/ui/tui/controller.go` (for example
  `pendingImages []orchestrator.ImageData` and `pendingFiles []string`).
- Add UI hints in `pkg/ui/tui/app_widget.go` (status line or sidebar summary).

2) Update file picker and paste handling
- Modify `handleFileSelect` to enqueue attachments instead of injecting file
  contents as a system message.
- Update `PasteMsg` handling in `pkg/ui/tui/app_widget.go` to detect file paths
  and queue attachments (images get data URLs using `orchestrator.InputProcessor`).

3) Submit with multimodal content
- Add a new `Conversation` helper (for example `AddUserMessageParts`) in
  `pkg/conversation/conversation.go` to accept `[]model.ContentPart`.
- In `handleSubmit` or `streamResponse`, build a `RawInput` and call
  `orchestrator.InputProcessor.Process` to produce `[]model.ContentPart` and
  pass them into the conversation and model request.

Done when:
- Submitting a message with a selected image produces a `[]model.ContentPart`
  payload containing `image_url` content.
- File picker selection does not auto-insert file content into chat history.

### Optional: hit grid mouse routing (TUI ergonomics)

Goal: improve mouse click routing for sidebar and scrollable widgets.

1) Add hit grid to runtime
- Extend `pkg/ui/runtime` to store a grid of widget IDs per cell during render.
- Populate it in `pkg/ui/runtime/screen.go` as widgets render.

2) Use hit grid for mouse dispatch
- Update `pkg/ui/tui/app_widget.go` mouse handling to route events to the widget
  under the cursor, using the hit grid as a first pass.

Done when:
- Clicking on sidebar items consistently routes to the intended widget even
  when overlapping overlays are present.

