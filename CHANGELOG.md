# Changelog

All notable changes to Buckley will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.0] - 2026-02-05

First public release. AI development assistant that remembers what you're doing.

### Added

#### Core
- **Three execution modes**: Interactive TUI, headless API, one-shot CLI commands
- **Plan-first workflow**: `/plan` breaks work into tasks, `/execute` runs them with self-healing retries, AI reviews before merge
- **Session persistence**: Conversations survive crashes. Resume where you left off via SQLite with WAL mode
- **Context compaction**: Auto-summarizes at 75% context usage so conversations can run indefinitely

#### Execution Strategies
- **Classic mode**: Single-agent tool loop — model calls tools, tools return results, loop repeats. Streaming-compatible
- **RLM (Recursive Language Model) mode**: Coordinator delegates to sub-agents via meta-tools (`delegate`, `delegate_batch`, `inspect`, `set_answer`). Weight-based model routing. Shared scratchpad for cross-task visibility. Supports 200+ sequential tool calls
- **Execution factory**: Pluggable strategy selection via config (`classic`, `rlm`, `auto`)

#### Machine Runtime
- **Pure state machine**: `Transition(event) -> (State, []Action)` with 18 states across 3 modalities (Classic, RLM, Ralph). No I/O, fully unit-testable
- **File lock manager**: Reader-writer locking for parallel agents. Prevents simultaneous edits to the same file. Deadlock prevention via sorted lock acquisition
- **Observable wrapper**: Publishes state transitions to telemetry hub for dashboards and debugging

#### Tools
- **46+ built-in tools**: File operations, search, shell, git, quality checks
- **Middleware pipeline**: Approval, timeout, retry, telemetry, progress, toast, validation, file watch — configurable per-project
- **MCP integration**: Model Context Protocol client for external tool servers via JSON-RPC
- **TOON encoding**: Compact serialization format saving ~30% tokens on tool outputs vs JSON

#### One-Shot Commands
- `buckley commit` — AI-generated conventional commit from staged changes with cost transparency, secret detection, and interactive confirmation
- `buckley pr` — AI-generated pull request with structured title/summary/changes. Auto-detects base branch. Creates via `gh`
- `buckley review` — Code review of current changes
- `buckley hunt` — Codebase improvement scanner
- `buckley dream` — Architectural analysis
- RLM retry mode for one-shot commands: retries with in-context guidance when models fail to call tools or return invalid JSON

#### Ralph — Autonomous Task Runner
- Multi-backend orchestration engine for long-running autonomous tasks
- Pluggable backends (internal Buckley or external CLIs) with sequential, parallel, or round-robin execution
- Cost-aware throttling: pauses backends that exceed budget
- Dynamic model selection via "when" expressions
- Hot-reloadable control config (`ralph-control.yaml`) with pause/resume/backend switching
- Session memory with automatic summarization every N iterations
- Auto-commit and PR creation on completion
- `buckley ralph list`, `ralph control --status/--pause/--resume`

#### Terminal UI
- **fluffyui-native** retained-mode rendering with dirty tracking and signal-based reactivity
- **Chat view**: Scrollable messages with code blocks, syntax highlighting, text selection, search
- **Reasoning accordion**: Collapsible panel showing model thinking/chain-of-thought
- **Input area**: Multi-line editor with 5 modes — Normal, Shell (!), Env ($), Search (/), Picker (@). History navigation
- **Sidebar**: Resizable, two tabs (Status and Files). Shows tasks, plan progress, running tools, context usage, RLM status, active agents, file locks
- **Interactive palette**: Fuzzy-searchable command/model/file picker
- **Settings dialog**: UI preferences (theme, contrast, motion, metadata)
- **Approval dialog**: Tool execution approval with diff preview, approve/deny/always-allow
- **Status bar**: Mode indicator, context usage (color-coded), token count + cost
- Mouse support, drag-drop file attachment, keyboard shortcuts (Ctrl+K sidebar, Ctrl+/ search, Ctrl+O files, Ctrl+, settings)

#### Multi-Model Support
- **6 providers**: OpenRouter (100+ models), OpenAI, Anthropic, Google, Ollama, LiteLLM
- **Per-phase model routing**: Different models for planning, execution, review, and utility tasks (commit, PR, compaction)
- **Fallback chains**: Automatic failover when a model is unavailable
- **Prompt caching**: Provider-level caching with OpenRouter/LiteLLM message trimming and OpenAI native cache keys
- **Circuit breaker**: Per-provider health tracking with automatic fallback

#### Safety & Control
- **Four approval modes**: Ask (approve everything), Safe (read anything, approve writes), Auto (full workspace, approve external), Yolo (full autonomy)
- **Four sandbox tiers**: Disabled, ReadOnly, Workspace (writes limited to project), Strict (explicit allowlist only). Detects fork bombs, recursive deletes, dangerous patterns
- **Cost management**: Session/daily/monthly budgets with tiered alerts at 50%/75%/90%/100%. Pauses execution when exceeded
- **Trusted/denied paths**: Per-project path restrictions. Sensitive dirs (~/.ssh, ~/.aws, /etc) denied by default

#### Infrastructure
- **Conversation engine**: Export/import (Markdown, JSON, HTML), semantic search over message embeddings, smart context trimming that preserves tool call/response pairs
- **ACP (Agent Control Protocol)**: gRPC server for remote agent interaction — ChatCompletion, CallTool, SpawnAgent, SteerAgent, ListAgents. SQLite or NATS event stores. TLS/mTLS
- **IPC server**: HTTP/WebSocket for web UI (Mission Control). Vue-based frontend with approval modals, message bubbles, terminal pane. Token/basic auth, CORS, web push
- **Telemetry hub**: Pub/sub event bus. Diagnostics collector with ring-buffered aggregation — API calls, tokens, latency, model/tool usage, circuit breaker states, error history
- **Reasoning logger**: Daily-rotating log files for model thinking content
- **Browser runtime**: Servo-based headless browser engine (Rust) with 8 tools (`browser_start`, `browser_navigate`, `browser_observe`, `browser_act`, `browser_screenshot`, `browser_stream`, `browser_clipboard`, `browser_close`), virtual clipboard, network allowlists, process isolation
- **Coordination**: Multi-agent primitives — P2P, pub/sub, service discovery, capabilities

#### Configuration
- Hierarchical: `~/.buckley/config.yaml` (user) → `.buckley/config.yaml` (project) → environment variables
- 25+ config sections covering models, providers, approval, sandbox, orchestrator, cost, memory, compaction, notifications, UI, git events, workflow, artifacts, personality
- `buckley config check` validates configuration and shows diagnostics
- Shell completions for bash, zsh, fish

#### Testing
- 103 test packages, 6,713 individual test cases
- Integration test suites: browser runtime, chat loop chaos, TUI rendering, TUI tool loop, OpenRouter rendering
- Self-healing E2E framework (`scripts/agent-tests/`) using AI-powered semantic matching — adapts to UI changes without brittle selectors

#### Documentation
- 11 Architecture Decision Records (ADRs) covering SQLite/WAL, process plugins, multi-model routing, plan-first workflow, context compaction, tiered approval, TOON encoding, event telemetry, RLM runtime, custom TUI, Servo browser
- Full CLI reference, configuration reference, tools reference, skills guide, orchestration guide, ACP protocol docs

#### Build & Release
- GoReleaser: Linux/macOS/Windows (amd64 + arm64), CGO disabled
- Docker images: `ghcr.io/odvcencio/buckley` (runtime), `buckley-worker` (with build tools)
- Package formats: tar.gz, zip, deb, rpm
- Homebrew and Scoop casks
- CI pipeline via GitHub Actions

### Security
- IPC/ACP servers disabled by default (opt-in)
- Default binding to localhost (127.0.0.1)
- Token and basic auth available for IPC
- Telemetry is local-only by default
- Plugin discovery limited to local paths
- Command sandboxing with dangerous pattern detection
- Secret detection in staged files during commit generation

[Unreleased]: https://github.com/odvcencio/buckley/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/odvcencio/buckley/releases/tag/v0.1.0
