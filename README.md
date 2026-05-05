# Buckley

Buckley is a tool-first AI agent harness for serious repository work.

It combines resumable sessions, Arbiter-governed model and tool selection, Claude-style repository instructions, and multiple operator surfaces: terminal, browser, one-shot, ACP, and LSP.

## Why Buckley

- One shared runtime powers TUI chat, `buckley -p`, headless sessions, ACP editor flows, and browser control.
- Arbiter governs planning/execution/review model routing, reasoning effort, tool pools, timeouts, approvals, and escalation.
- Runtime prompt assembly automatically pulls in `AGENTS.md`, `CLAUDE.md`, `.claude/instructions.md`, project context, and active skills.
- Tool use is first-class. Buckley ships with 38 built-in tool entry points plus skills, plugins, telemetry, approvals, and SQLite-backed persistence.
- Sessions survive crashes. Plans, approvals, telemetry, and artifacts stay resumable.

## Quick Start

```bash
go install github.com/odvcencio/buckley/cmd/buckley@latest
export OPENROUTER_API_KEY="your-key"
buckley
```

OpenAI, Anthropic, Google, and Ollama are also supported. OpenRouter is the default path when available.

## Interfaces

| Surface | Command | Use case |
| --- | --- | --- |
| TUI | `buckley` | Interactive coding with approvals, streaming, and history |
| One-shot | `buckley -p "inspect this repo and fix the failing tests"` | Fast task execution from the terminal |
| Browser UI | `buckley serve --browser` | Mission Control, approvals, and remote session control |
| ACP agent | `buckley acp` | Editor agent for ACP-compatible clients |
| LSP bridge | `buckley lsp` | LSP editor integration on stdio |

## Core Workflow

```bash
buckley plan "add auth" "support email/password login"
buckley execute <plan-id>
buckley review
```

The planner, builder, review, and runtime layers share the same governance stack, persistence, and tool registry.

## What 1.1.0 Adds

- Fully integrated Arbiter runtime across one-shot, TUI, ACP, and headless execution paths.
- Shared runtime prompt assembly for repo instructions, project context, working directory, and skills.
- Governed tool exposure with role/task-aware pool filtering before tool calls are shown to the model.
- Anthropic tool calling and tool-result round-tripping, so Claude-class models can participate in the same tool loop.
- Better model resolution for routed raw IDs such as unqualified Claude model names.

## Commands People Actually Use

```bash
buckley commit
buckley pr
buckley review
buckley hunt
buckley dream
buckley experiment run "compare-routing" -m moonshotai/kimi-k2-thinking -m anthropic/claude-sonnet-4-5 -p "Implement feature X"
```

`buckley commit` and `buckley pr` use transparent tool-first workflows rather than opaque text-only prompting.

## Configuration

Configuration is layered:

```text
~/.buckley/config.yaml
./.buckley/config.yaml
environment variables
```

Minimal setup:

```yaml
providers:
  openrouter:
    api_key: ${OPENROUTER_API_KEY}
```

Buckley supports separate planning, execution, and review models:

```yaml
models:
  planning: anthropic/claude-sonnet-4-5
  execution: moonshotai/kimi-k2-thinking
  review: openai/gpt-5.5
  reasoning: xhigh
```

## Skills And Instructions

Buckley ships with 8 bundled skills, including planning, code review, systematic debugging, refactoring, test-driven development, API design, git workflow, and creative writing.

Skills layer on top of repository instructions. If a repo already uses `AGENTS.md` or `CLAUDE.md`, Buckley consumes them automatically and applies the same guidance across terminal, headless, and editor sessions.

## Development

```bash
./scripts/test.sh
go build ./cmd/buckley
```

Primary repo docs live under [`docs/`](/home/draco/work/buckley/docs).

## License

MIT. See [LICENSE](LICENSE).
