# Changelog

All notable changes to Buckley will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

## [1.0.0] - 2026-01-04

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

[Unreleased]: https://github.com/odvcencio/buckley/compare/v1.6.0...HEAD
[1.6.0]: https://github.com/odvcencio/buckley/compare/v1.5.0...v1.6.0
[1.5.0]: https://github.com/odvcencio/buckley/compare/v1.4.1...v1.5.0
[1.4.1]: https://github.com/odvcencio/buckley/compare/v1.4.0...v1.4.1
[1.4.0]: https://github.com/odvcencio/buckley/compare/v1.3.0...v1.4.0
[1.3.0]: https://github.com/odvcencio/buckley/releases/tag/v1.3.0
[1.2.0]: https://github.com/odvcencio/buckley/releases/tag/v1.2.0
[1.1.0]: https://github.com/odvcencio/buckley/releases/tag/v1.1.0
[1.0.0]: https://github.com/odvcencio/buckley/releases/tag/v1.0.0
