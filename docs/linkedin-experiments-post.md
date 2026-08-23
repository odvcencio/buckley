# LinkedIn Post - Multi-Model Experiments

---

"Claude is better than GPT for coding."

"No, GPT-5 destroys Claude on refactoring."

"Qwen is underrated."

Everyone has opinions. Nobody has data.

So I built a way to get data.

Buckley's experiment mode:

```bash
buckley experiment run refactor-auth \
  -m gpt-5.2-codex-xhigh \
  -m claude-sonnet-4-5 \
  -m qwen3-coder \
  -p "Refactor auth to use JWT"
```

What happens:
1. Creates isolated git worktree per model
2. Runs identical task in parallel
3. Evaluates against your success criteria
4. Compares cost, duration, and quality
5. Declares a winner

Output:

```
Model             | Score | Cost    | Duration
claude-sonnet-4-5 | 100%  | $0.0234 | 3m 42s
gpt-5.2-codex     | 100%  | $0.0156 | 2m 18s
qwen3-coder       |  60%  | $0.00   | 5m 12s

Winner: gpt-5.2-codex (100%, lowest cost)
```

Why this matters:

Marketing says one thing. Benchmarks say another. Your actual codebase? Different story.

Now you can test on YOUR code. YOUR tasks. YOUR success criteria.

Stop arguing about models. Start measuring.

GitHub: https://github.com/odvcencio/buckley
Docs: https://buckley.draco.quest/EXPERIMENTS

What's the weirdest model comparison you'd want to run? 👇

---

**Alt shorter version:**

Hot take: Model benchmarks are useless for real work.

GPT beats Claude on HumanEval.
Claude beats GPT on SWE-bench.
Neither tells you which one writes better code for YOUR codebase.

So I built something that does.

Buckley experiment mode:
→ Same task
→ Multiple models
→ Isolated git worktrees
→ Real success criteria
→ Cost + speed + quality comparison

One command. Actual data. No more vibes.

```bash
buckley experiment run add-tests \
  -m gpt-5.2-codex \
  -m claude-sonnet-4-5 \
  -p "Add tests for user service"
```

Run it. See who wins. On your code.

https://github.com/odvcencio/buckley
https://buckley.draco.quest/EXPERIMENTS

---

**Alt spicy version:**

I'm tired of "GPT vs Claude" debates.

You know what settles it?

Running both on the same task and measuring.

That's literally what Buckley does now.

```bash
buckley experiment run fix-bug \
  -m gpt-5.2-codex \
  -m claude-sonnet-4-5 \
  -m ollama/llama3:70b \
  -p "Fix the race condition in pkg/cache"
```

Each model gets its own git worktree. Runs in parallel. Evaluated against success criteria you define.

Winner declared by:
1. Did it actually work? (tests pass, files exist)
2. How much did it cost?
3. How long did it take?

Last week Claude won 3 experiments. GPT won 2. Qwen surprised me twice.

No benchmark will tell you that. Your codebase will.

https://github.com/odvcencio/buckley
https://buckley.draco.quest/EXPERIMENTS

What models are you comparing? Curious what others are seeing.
