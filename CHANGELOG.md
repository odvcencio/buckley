# Changelog

All notable changes to Buckley will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/odvcencio/buckley/compare/v1.1.0...HEAD
[1.1.0]: https://github.com/odvcencio/buckley/releases/tag/v1.1.0
[1.0.0]: https://github.com/odvcencio/buckley/releases/tag/v1.0.0
