# LinkedIn Post - RLM Runtime Announcement

---

MIT dropped a paper that changes how we think about AI context limits.

And I already implemented it.

Here's the problem:

Your LLM can't actually "see" 200k tokens.

Sure, the context window fits it. But accuracy tanks after ~50k. Ask about page 847 of a document? Good luck.

MIT's solution: Recursive Language Models (RLMs).

Instead of cramming everything into context, the model:
→ Loads data into a scratchpad it can query
→ Recursively calls *itself* on subproblems
→ Combines results at the coordinator level
→ Never exceeds its effective attention span

Results? 56.5% vs 44% on ultra-long docs. 2.7x improvement on code QA.

I read this and thought: "This is exactly how Buckley should work."

So I built it.

Buckley now has an RLM runtime:

- **Coordinator pattern** - breaks tasks into subtasks
- **Tiered model routing** - trivial lookups get cheap models, deep reasoning gets expensive ones
- **Confidence thresholds** - iterate until you're sure
- **Scratchpad** - intermediate results persist across calls
- **Budget awareness** - stops before you burn your API limit

It's not a 1:1 paper implementation. It's adapted for dev workflows.

But the core insight holds: don't fight context limits, work around them.

Paper: https://arxiv.org/abs/2512.24601
Buckley: https://github.com/odvcencio/buckley
Docs: https://buckley.draco.quest

Have you tried RLM-style approaches in your AI tooling? What worked? 👇

---

**Alt shorter version:**

MIT's RLM paper solved a problem I've been fighting for months:

LLMs can't actually use their full context window effectively.

The fix? Recursion.

Load data into a scratchpad. Let the model call itself on subproblems. Combine at the coordinator level.

I implemented this in Buckley:
- Coordinator breaks tasks into subtasks
- Tiered routing (cheap models for lookups, expensive for reasoning)
- Confidence-based iteration
- Persistent scratchpad

It's not the paper's exact approach—it's adapted for dev workflows.

But the result: AI that handles complexity without choking on context.

Paper: https://arxiv.org/abs/2512.24601
Code: https://github.com/odvcencio/buckley
Docs: https://buckley.draco.quest

---

**Alt technical version:**

Implemented MIT's Recursive Language Model (RLM) pattern in Buckley.

The insight: LLMs degrade on long contexts. Even with 200k windows, accuracy drops past ~50k tokens.

RLMs fix this by:
1. Storing input in queryable scratchpad (not raw context)
2. Coordinator decomposes tasks into subtasks
3. Subtasks route to appropriate model tiers
4. Results combine via recursive aggregation

My implementation adds:
- 5 model tiers (trivial → reasoning) with cost-aware routing
- Confidence thresholds that control iteration
- Budget limits (tokens + wall time)
- Telemetry hooks for observability

Use case: code review on a 50-file PR. Coordinator delegates file analysis to cheap models, synthesis to expensive ones. Scratchpad tracks findings across iterations.

Paper: https://arxiv.org/abs/2512.24601
Implementation: https://github.com/odvcencio/buckley/tree/main/pkg/rlm
Docs: https://buckley.draco.quest

Curious how others are handling the "context window is a lie" problem.
