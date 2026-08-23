---
layout: home

hero:
  name: Buckley
  text: AI dev assistant that remembers what you're doing
  tagline: Sessions survive crashes. Four trust levels. Loop detection. Multi-model experiments. Built by someone who uses it daily.
  image:
    src: /logo.jpg
    alt: Buckley - your AI development companion
  actions:
    - theme: brand
      text: Get Started
      link: /CLI
    - theme: alt
      text: View on GitHub
      link: https://github.com/odvcencio/buckley

features:
  - icon: 💾
    title: Sessions That Survive
    details: Crash? Power outage? Your work is still there. SQLite persistence means you pick up where you left off.
  - icon: 🎚️
    title: Tiered Autonomy
    details: Four trust levels from "ask everything" to "full auto". Smart command classification knows what's safe. Trusted paths per project.
  - icon: 📱
    title: Walk Away, Get Pinged
    details: Telegram and Slack notifications when Buckley needs you. Respond from your phone. Come back when it matters.
  - icon: 🔄
    title: Loop Detection
    details: AI gets stuck retrying the same thing? Buckley detects it and stops. Tokens aren't free.
  - icon: 🎯
    title: Multi-Model Experiments
    details: Run the same task across different models. See who's actually good, not who has the best marketing.
  - icon: 🔓
    title: No Vendor Lock-in
    details: OpenRouter, OpenAI, Anthropic, Google, or local Ollama. Your choice, always.
---

## Quick Start

```bash
# Install
go install github.com/odvcencio/buckley/cmd/buckley@latest

# Configure
export OPENROUTER_API_KEY="your-key-here"

# Run
buckley
```

## Why Buckley?

Most AI coding tools are black boxes. You type something, magic happens (or doesn't), and you hope for the best.

Buckley is different:

- **Transparent** - See exactly what the AI is thinking and doing
- **Recoverable** - Sessions persist to SQLite, survive crashes, resume cleanly
- **Controlled** - Human-in-the-loop approvals before any file changes
- **Smart** - Self-healing execution with loop detection so you don't burn tokens on infinite retries

Built because existing tools weren't good enough. Used daily. Still being improved.

## Documentation

| Section | What's Inside |
|---------|---------------|
| [CLI Reference](./CLI.md) | Commands, flags, and shortcuts |
| [Configuration](./CONFIGURATION.md) | Config files, env vars, model setup |
| [Error Handling](./ERRORS.md) | Error types and troubleshooting |

## Open Source

MIT licensed. Contributions welcome. Built in Go.

[View on GitHub →](https://github.com/odvcencio/buckley)
