# Changelog

All notable changes to Buckley will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0] - 2026-01-04

### Added
- **Multi-agent coordination system** with conflict-aware scheduling:
  - Scope validation for pre-flight conflict detection
  - Automatic task partitioning into execution waves
  - Runtime file locking with TTL and heartbeats
  - Auto-merge orchestration for completed worktrees
- Headless mode for CI/CD automation and non-interactive environments
- MCP (Model Context Protocol) integration for enhanced model communication
- Parallel agent execution for concurrent task processing
- Ollama provider for local model runs
- Mobile web UI for remote access and control
- Experiment CLI for parallel comparisons and replay
- Intelligent planning workflow with brainstorm, refine, and commit actions
- Agent Communication Protocol (ACP) with Mission Control UI
- Multi-suggestion editor workflow with HTTP proxy support
- Personality system with YAML-based persona definitions and phase-based activation
- Skill system with 7 bundled skills (TDD, debugging, refactoring, planning, code-review, API design, git workflow)
- Semantic search / RAG with embeddings-based code search via OpenRouter
- TODO system with SQLite persistence and auto-checkpointing for 100+ step plans
- Shell and index telemetry events for command history tracking
- Network transport logging for debugging API interactions
- Reasoning support for models that provide chain-of-thought

### Changed
- SQLite now uses WAL mode with 5s busy timeout and connection pooling
- Improved context injection with viewmodel assembler
- Enhanced tool validation before execution

### Fixed
- Flaky streaming test race condition in model package
- Content parsing issues in conversation handling
- Tool call validation preventing invalid executions

### Security
- IPC/ACP servers disabled by default (opt-in for remote scenarios)
- Default binding to localhost (127.0.0.1) unless explicitly overridden
- Token/basic auth available for IPC (optional)
- Telemetry is local-only by default
- Plugin discovery limited to local paths only

[Unreleased]: https://github.com/odvcencio/buckley/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/odvcencio/buckley/releases/tag/v1.0.0
