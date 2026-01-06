# Seven Weeks of Rabbit Holes

**January 4, 2026**

November 14th. I'm reading some blog post about AI agents while procrastinating on actual work. MoonshotAI had just dropped Kimi-K2-Thinking and I made the mistake of trying it.

That model's context window is obscene. I watched it hold 50+ tool calls without losing the plot. Every existing harness I tried either crashed, forgot what we were doing, or decided to "help" by rewriting code I never asked it to touch.

So I started building a wrapper. Then the wrapper needed persistence. Then I wanted notifications. Then I wondered why I couldn't compare Claude against GPT against Kimi on the same task.

Seven weeks later I have 15,000 lines of Go and a terminal app that refuses to die when I yank the power cable.

## What Actually Pissed Me Off

**Lost work.** Mid-task crash, everything gone. The model knew what to do but the harness didn't save state. SQLite fixed this.

**Binary trust.** Either it asked permission for everything or ran wild. I wanted four levels: manual, cautious, standard, autonomous. Let me dial it per-project.

**Spinning.** The AI would fail, retry the same thing, fail again, burn tokens. Now Buckley tracks attempts and stops after three.

**One model.** Every tool married you to a provider. I route through OpenRouter. Pick whoever's cheapest or smartest today.

**No pager.** I'd walk away and miss the "waiting for human input" prompt. Telegram bot now yells at me.

## The Part I Actually Like

I use Buckley to build Buckley. Daily. It handles its own PRs, writes its own tests, catches its own bugs. When a feature breaks the tool I'm using to build the feature, I find out fast.

Not done. Over-engineered in spots. Documentation sparse. But it ships.

## Install

```bash
go install github.com/odvcencio/buckley/cmd/buckley@latest
export OPENROUTER_API_KEY="your-key"
buckley
```

No containers. No Postgres. One binary.

## The Philosophy Part

I don't vibe code. Everything gets reviewed. The AI writes first drafts; I own what merges.

Think of it like scaffolding, not autopilot. I climb places I couldn't reach before. I still decide where to go.

---

MIT. [Source](https://github.com/odvcencio/buckley). DMs open.
