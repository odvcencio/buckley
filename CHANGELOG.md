# Changelog

All notable changes to Buckley will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.6.0] - 2026-08-11

### Added
- Canopy-backed repository review table of contents with file/language and
  symbol inventories, parse health, call-graph edges, complexity hotspots,
  and caller/callee anchors for important flows.
- Line-paginated `read_file` results with total-line counts, continuation
  cursors, and explicit page bounds so long files can be inspected completely
  without losing evidence to output truncation.
- Explicit review controls for total timeout and optional tool-call limits;
  repository reviews default to a 20-minute completion window.

### Changed
- Repository-wide Buckbot reviews are completion-first and have no implicit
  per-review model-turn or inspection-call cap. The normal deadline and
  governor remain safety boundaries, while cost-sensitive callers can opt in
  to `--max-tool-calls`.
- Review prompts now require a tracked-file coverage ledger, evidence section,
  and an explicit `COMPLETE` or `PARTIAL` result, using the Canopy TOC as a
  navigation map rather than a sampling limit.
- OpenAI-compatible streaming retries are limited to interruptions before the
  first event, preventing duplicated tool calls or text after a provider has
  started responding while preserving partial assistant text for recovery.

### Fixed
- Long-file review caveats caused by a single truncated read; agents can now
  follow `next_start_line` until every relevant page has been inspected.
- ACP tool-result formatting now uses the canonical model-output encoder, and
  partial stream failures no longer replay unsafe reasoning or tool-call
  fragments.

## [0.5.0] - 2026-08-11

### Added
- Adaptive Harness v1: provider-neutral progress accounting now records exact
  tool yield, including a successful `0 matches` search result.
- Governed code-mode recovery. Buckley prominently recommends the audited,
  bubblewrap-isolated `exec_program` surface after an eligible tool failure or
  repeated successful zero-yield exploration.
- Durable subagent coordination with explicit child contracts, mailbox
  steering, dependency state, workspace claims, approval posture, evidence,
  and replayable lifecycle events.
- `buckley.artifact/v1` with validation, bounded repair, tool submission,
  durable evidence, legacy migration, and deterministic terminal, Markdown++,
  JSON, SARIF, FluffyUI, and ACP projections.
- Immutable empirical model-behavior profiles in SQLite and a policy-receipted
  protocol compiler. Profiles select bounded tool scope, fanout, continuation,
  verification, typed output, and code-mode posture without model-name rules.
- Restored `buckley buckbot` as the general-purpose review agent, with local,
  repository-wide advisory, and explicitly posted PR review routes.
- Local-first Hyphae project-space discovery now gives TUI, one-shot, and ACP
  agents bounded guidance for recalling durable project decisions and lessons.

### Changed
- Buckbot reviews are completion-first and have no automatic dollar cap by
  default. An explicit CLI or configuration budget remains available for
  cost-sensitive CI and unattended runs.
- Removed the legacy Qwen-specific two-turn review clamp; configured models
  now receive the same governed review-turn budget for the selected scope.
- The TUI activity inspector now exposes subagent execution contracts:
  persona, model, tier, effort, turn cap, isolation, output schema, approval
  posture, allowed-tool count, and workspace claims.
- Search and tool telemetry preserve measured yields across TUI, ACP,
  headless, and durable-goal surfaces. Successful empty results are no longer
  presented as failed searches.
- Adaptive protocol rollout remains opt-in. Shadow mode emits receipts only;
  dynamic mode applies only explicitly versioned, measured profiles.
- Replaced the bespoke React/Vite/Bun Mission Control browser bundle with a
  server-rendered GoSX application embedded in the Buckley binary. Native HTML
  actions, HTTP-only session cookies, bounded live refresh, and the existing
  PTY transport keep browser control aligned with the daemon without a second
  client runtime.

### Performance
- The progress hot path and subagent status lookup remain allocation-free.
- Protocol compilation now materializes only the active artifact contract,
  reducing the measured allocation volume by about 9x and allocation count by
  about 11x compared with the eager-schema baseline.

## [0.4.0] - 2026-08-01

Buckley returned to a pre-1.0 version line at 0.4.0. Sections are in
reverse chronological order, not version order. Releases 1.1.0 through
2.3.0 predate 0.4.0.

### Added
- ACP sessions bridge session-scoped MCP servers into the tool registry and advertise skills as invokable slash commands.
- ACP sessions also expose model selection through session config options and report token usage after each model round.

### Changed
- The module path drops the `/v2` semantic-import-version suffix used since 2.0.0: `m31labs.dev/buckley/v2` becomes `m31labs.dev/buckley`.
- The TUI tool loop streams assistant text live as it arrives from the model, replacing the buffered full-response render with incremental transcript updates.
- The TUI, oneshot review pipeline, and RLM sub-agent and coordinator loops migrate onto the shared `pkg/agentloop.Controller` turn engine. This engine replaces four separate hand-rolled tool loops with one governed implementation.
- Config loading replaces about 30 hand-written per-section merge functions and manual environment-variable overrides with a single reflection-driven walker keyed on struct tags.
- ACP sessions stream assistant text and reasoning per token instead of buffering a full turn.

### Fixed
- Tool-call preamble text, for example "Let me check that.", now survives into persisted conversation history. The wire-format request sent back to the model also keeps this preamble, instead of dropping it.
- ACP tool kind mappings match current tool names after renames (`patch_file` to `apply_patch`, `terminal_editor` to `edit_file_terminal`).

## [2.3.0] - 2026-08-02

### Added
- Canopy-backed code analysis tools (`code_callgraph`, `code_refs`, `code_impact`) and a post-edit Go diagnostics probe that reports compile errors in the tool result.
- Tiller-compatible persona registry with persona-aware subagent spawning, model tier pins, and typed escalation denial.
- Layered glob permission policy (posture, project, user, built-in) with arbiter evaluation, credential-read denial in every mode, and an `unattended` posture that parks ask decisions.
- MCP client support: stdio servers configured under `mcp.servers` become permission-governed `mcp_<server>_<tool>` tools.
- Plugin hook contract for process plugins: sanitized telemetry event subscriptions and a pre-tool veto surface with advisory and enforcing modes.
- Model variant presets with `Alt+M` cycling, recent-model cycling with `Alt+R`, `/undo` and `/redo` over shadow-git snapshots, a run-tree session navigator, `buckley session export`, and `buckley models refresh`.
- Shared turn engine (`pkg/agentloop.Controller`) driving the experiment, builder-agent, and headless loops with uniform projection, usage accounting, governor checks, and run-ledger events.
- `buckley attach` joins a running session over loopback gRPC: list sessions, stream events, and send input from a second terminal.
- ACP client permission flow: editors receive `session/request_permission` before any non-read-only tool runs.

### Changed
- ACP wire compliance: `currentModeId` field name, diff content always carries `path` and `newText` without trimming or truncation, custom extensions moved under underscore-prefixed methods, and tool kind and title maps match the real tool names.
- Headless turns record tool exchanges into conversation history and persist them as they land.

### Fixed
- Continuation window survives compaction: the represented prefix stays byte-identical, commits carry the full request fingerprints, and restore accepts caller-shaped model identities.

## [2.2.0] - 2026-08-01

### Added
- Tree-sitter syntax highlighting for supported fenced code blocks in terminal chat.
- Multiline chat input with `Shift+Enter` and copying of the latest code block with `Alt+C`.

### Changed
- Conversation projection uses a bounded search to retain the largest context that fits each provider request budget.
- BuckBot allocates focused verification calls to the changed package targets, including sharded reviews.

### Fixed
- Quiet BuckBot reviews no longer emit terminal progress spinners.
- BuckBot review guidance prevents a root-level Go test from standing in for nested changed packages.

## [2.1.0] - 2026-07-29

### Added
- Pluggable, token-budgeted pull-request context providers with native Hyphae knowledge enrichment.
- A Qwen 3.7 Plus review profile with adaptive thinking budgets and deterministic workflow-provenance analysis.

### Changed
- Buckbot curates supporting review context separately from protected diffs, immutable CI evidence, changed-file coverage, and feedback identifiers.
- Pull-request shard cost projections account for curated provider and repository context.

### Fixed
- Manual release dispatches validate the checked-out tag commit rather than the workflow event SHA.
- Review output limits preserve final-answer headroom in addition to provider reasoning tokens.
- Schema-only repair attempts preserve demonstrated findings and their severity-based verdict disposition.

## [2.0.0] - 2026-07-28

### Breaking
- The Go module and all import paths now use the semantic-import-versioned `m31labs.dev/buckley/v2` path.
- Removed legacy UI import-path mirrors and obsolete shell-mode helpers. External UI integrations must migrate to the canonical `pkg/ui/...` surfaces or remain on v1.6.1 while migrating.
- Consolidated terminal behavior around the current `pkg/ui/terminal`, `pkg/ui/tui`, and retained component APIs.

### Added
- Live OpenRouter catalog refresh and fuzzy model selection during an active chat.
- Steering, queued input, interrupt handling, durable provider threads, and asynchronous subagent progress.
- Canopy-first branch and project review context with compact prompts and repository-health reporting.
- FluffyUI terminal rendering and an accepted staged path for a GoSX browser/desktop client.
- Filesystem-discovered agent profiles, skills, named subagents, scoped tool tiers, and invocation previews.
- Project chat-check suites with artifacts, JUnit reports, health checks, and scenario inspection.
- Top-level `doctor`, `info`, and `skills` inspection surfaces with expanded agent initialization, discovery, and run workflows.
- Sharded pull-request review with bounded concurrency, projected cost, full high-signal coverage, and a governed posting gate.

### Changed
- Interactive tool execution continues until completion while remaining sequential and visible as a persistent event stream.
- Provider-supplied reasoning and tool results render as durable progress entries instead of transient status messages.
- Project reviews use bounded structural sampling to reduce elapsed time and token spend.
- Buckbot uses `qwen/qwen3.7-plus` with low, medium, or high reasoning selected from the governed change size.
- `codex/auto` scales Buckbot from Luna to Terra to Sol as review size increases. Luna uses xhigh reasoning for small diffs. Sol uses medium reasoning to avoid a long latency tail. Exact model overrides remain fixed.
- Pull request reviews stay local unless the caller uses `--post`.
- One-shot commit, PR, and review flows use focused command surfaces with governed Codex and API backends.
- The TUI, ACP, experiment, and task-workspace paths are split into smaller components while preserving behavior.
- Terminal input, search, file-picker, status-bar, and chat wrapping are rune-aware.

### Fixed
- OpenRouter requests now gate optional fields by each model's advertised parameters.
- Tool JSON schemas declare concrete nested item types accepted by Moonshot/Kimi providers.
- Unrestricted one-shot sessions no longer collapse into an empty tool allowlist.
- Task workspace discovery prefers the requested repository instead of an unrelated ancestor checkout.
- Reviews now enforce hard model-call deadlines, stop evidence collection by change size, and reserve time for final synthesis.
- Reviews now cap verification commands and prevent duplicate work before the command deadline.
- Reviews now stop Canopy context collection when the review deadline expires.
- GitHub reviews preserve valid line comments after one invalid comment rejects a batch.
- GitHub reviews show Canopy structural metrics and collapse detailed evidence for easier scanning.
- Pull-request context binds every operation to the resolved repository and immutable base/head revisions, validates exact changed-file manifests, and fails closed on incomplete CI evidence.
- Oversized GitHub diffs reconstruct through the paginated files API, with explicit unavailable-file evidence and immutable local fallback for throttled requests.
- Review repair preserves exact evidence, rejects speculative findings, bounds critic and verification work, and revalidates the target before posting.
- Retry backoff cap coverage is deterministic rather than dependent on scheduler timing.

### Release
- Tag pushes and manual releases share strict semantic-tag, vanity-path, preflight, and post-install verification.

## [1.6.1] - 2026-06-17

### Fixed
- Removed a stray external harness name from an invalid agent-spec test fixture.

## [1.6.0] - 2026-06-17

### Added
- Buckley-native agent spec validation and inspection via `buckley agent check` and `buckley agent show`.
- Agent specs now describe personas, model roles, runtime drivers, tool tiers, rule packs, sandbox policy, terminals, and subagents without importing another harness's conventions.
- Arbiter fact contract catalog via `buckley rules facts` for inspecting Buckley policy domains and available rule inputs.

## [1.5.0] - 2026-06-17

### Added
- TUI chat now shows animated elapsed process indicators while waiting on model API calls and tool executions.

### Changed
- Default OpenRouter chat, planning, review, and interactive execution model is now `z-ai/glm-5.2`.
- Default curated premium reasoning candidates now include `z-ai/glm-5.2`, `moonshotai/kimi-k2.7-code`, and `qwen/qwen3.7-max`.
- Live-gated multi-turn headless harness coverage now defaults to `xiaomi/mimo-v2.5-pro`.

## [1.4.1] - 2026-06-17

### Fixed
- Release workflow now keeps generated Homebrew Cask and Scoop manifests in the GoReleaser `dist` output instead of trying to push them directly to protected `main`.

## [1.4.0] - 2026-06-17

### Added
- Live-gated `qwen/qwen3.6-flash` multi-turn headless harness test under `tests/live` for validating real OpenRouter chat continuity when credentials are available.
- `buckley review --scope worktree|branch|changes` for explicit review context selection across branch commits, local changes, and combined worktree state.

### Changed
- `buckley review` now reports the selected review scope in prompts and includes local changed files in worktree-scope file lists.
- GoReleaser metadata now describes Buckley as a governed AI agent harness rather than a generic terminal assistant.

### Fixed
- TUI and headless chat sessions now persist assistant tool-call messages and tool response correlation metadata so multi-turn tool conversations survive reconnects, resumes, and API-driven session reloads.
- SQLite session storage now round-trips `tool_calls`, `tool_call_id`, and tool `name` fields for model-visible chat history.

## [1.3.0] - 2026-05-25

### Changed
- Go module and import paths migrated to `m31labs.dev/buckley`.

## [1.2.0] - 2026-05-23

### Added
- OpenRouter request support for fallback model lists, provider routing preferences, response formats, service tiers, seeds, session IDs, metadata, traces, parallel tool calls, cache control, and expanded reasoning controls.
- OpenRouter `reasoning_details` preservation across streaming, conversation history, SQLite storage, ACP, headless, one-shot, RLM, and TUI tool-call loops.
- Storage migration for persisted assistant reasoning details.

### Changed
- Default OpenRouter chat, planning, review, and interactive execution model is now `qwen/qwen3.6-max-preview`.
- Default utility model, including commit generation, is now `qwen/qwen3.6-flash`.
- Arbiter model routing now selects the Qwen OpenRouter default and routes `qwen/` model IDs through OpenRouter.
- Non-OpenRouter provider normalization strips unsupported reasoning metadata while preserving reasoning-only assistant text as content.

### Fixed
- `buckley -p -m <model>` now honors the explicit model override through one-shot execution resolution.
- Multi-turn tool-call chats now carry assistant reasoning details forward instead of dropping them between turns.

## [1.1.0] - 2026-04-05

### Added
- Arbiter-governed runtime wiring across one-shot CLI, TUI, ACP, and headless execution paths.
- Shared runtime prompt assembly that folds together base system instructions, discovered repo guidance (`AGENTS.md`, `CLAUDE.md`, `.claude/instructions.md`), project context, working directory, and skill descriptions.
- Governed tool filtering so exposed tool pools now respect runtime policy, task type, role, and skill-level allowlists before model invocation.
- Anthropic tool calling support, including assistant `tool_use` emission and `tool_result` round-tripping.
- Runtime model utilities for phase-aware model resolution, reasoning effort selection, and coarse model-tier inference.
- Release coverage for governed tool exposure, runtime prompt assembly, Anthropic tool translation, and unqualified routed model IDs.

### Changed
- `buckley -p` now runs through the same tool-using runtime loop as ACP sessions instead of a text-only one-shot path.
- ACP prompt construction now uses the shared runtime prompt builder and default rules engine bootstrap.
- TUI execution now resolves execution models, tool pools, and reasoning effort through Arbiter-backed runtime helpers.
- Headless approval gating now consults Arbiter risk/approval policy before falling back to legacy policy logic.
- README and release metadata now describe Buckley as a governed agent harness rather than a basic chat shell.

### Fixed
- ACP protobuf descriptors were regenerated, resolving the panic that previously broke ACP-related test packages.
- Config, docs hierarchy, and complexity-tool regressions that blocked `./scripts/test.sh`.
- Routed raw model IDs now resolve correctly for capability checks and tool/reasoning support lookups.

## 1.0.0 - 2026-01-04

No git tag exists for this release; the version jumped directly from
here to 1.1.0.

### Added
- Multi-agent coordination system with conflict-aware scheduling.
- Headless mode for CI/CD automation and non-interactive environments.
- MCP integration for enhanced model communication.
- Parallel agent execution for concurrent task processing.
- Ollama provider for local model runs.
- Mobile web UI for remote access and control.
- Experiment CLI for parallel comparisons and replay.
- Intelligent planning workflow with brainstorm, refine, and commit actions.
- Agent Communication Protocol (ACP) with Mission Control UI.
- Multi-suggestion editor workflow with HTTP proxy support.
- Personality system with YAML-based persona definitions and phase-based activation.
- Skill system with bundled workflow playbooks.
- Semantic search and RAG with embeddings-based code search via OpenRouter.
- TODO system with SQLite persistence and auto-checkpointing for large plans.
- Shell and index telemetry events for command history tracking.
- Network transport logging for debugging API interactions.
- Reasoning support for models that provide chain-of-thought.

### Changed
- SQLite now uses WAL mode with 5s busy timeout and connection pooling.
- Improved context injection with viewmodel assembler.
- Enhanced tool validation before execution.

### Fixed
- Flaky streaming test race condition in model package.
- Content parsing issues in conversation handling.
- Tool call validation preventing invalid executions.

### Security
- IPC/ACP servers disabled by default (opt-in for remote scenarios).
- Default binding to localhost unless explicitly overridden.
- Token/basic auth available for IPC.
- Telemetry is local-only by default.
- Plugin discovery limited to local paths only.

[Unreleased]: https://github.com/odvcencio/buckley/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/odvcencio/buckley/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/odvcencio/buckley/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/odvcencio/buckley/compare/v2.3.0...v0.4.0
[2.3.0]: https://github.com/odvcencio/buckley/compare/v2.2.0...v2.3.0
[2.2.0]: https://github.com/odvcencio/buckley/compare/v2.1.0...v2.2.0
[2.1.0]: https://github.com/odvcencio/buckley/compare/v2.0.0...v2.1.0
[2.0.0]: https://github.com/odvcencio/buckley/compare/v1.6.1...v2.0.0
[1.6.1]: https://github.com/odvcencio/buckley/compare/v1.6.0...v1.6.1
[1.6.0]: https://github.com/odvcencio/buckley/compare/v1.5.0...v1.6.0
[1.5.0]: https://github.com/odvcencio/buckley/compare/v1.4.1...v1.5.0
[1.4.1]: https://github.com/odvcencio/buckley/compare/v1.4.0...v1.4.1
[1.4.0]: https://github.com/odvcencio/buckley/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/odvcencio/buckley/releases/tag/v1.3.0
[1.2.0]: https://github.com/odvcencio/buckley/releases/tag/v1.2.0
[1.1.0]: https://github.com/odvcencio/buckley/releases/tag/v1.1.0
