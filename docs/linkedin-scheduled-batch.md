# LinkedIn Posts - ADR-Centered Batch

Based on Architecture Decision Records (ADRs) from Buckley.

---

## Post 1: SQLite with WAL (ADR-0001)

"Why aren't you using Postgres?" 🤔

Because I don't hate my users.

Here's my take 👇

AI dev tools should be:
```
go install <tool>
export API_KEY=xxx
<tool>
```

Done. Working. No Docker. No database setup.

So I chose SQLite with WAL mode.

WAL = Write-Ahead Logging. It means:
✅ Concurrent reads during writes
✅ Crash recovery built-in
✅ Zero external dependencies
✅ Ships as single binary

"But SQLite doesn't scale!"

Scale to what? This runs on YOUR laptop.
For YOUR sessions. On YOUR files.

You don't need distributed consensus for a dev tool.

The decision:
```go
db.Exec("PRAGMA journal_mode=WAL")
db.Exec("PRAGMA busy_timeout=5000")
```

Two lines. Crash-proof persistence.

Sometimes "boring" technology is the right technology.

🔗 https://github.com/odvcencio/buckley
📄 Full ADR: buckley.draco.quest/architecture/decisions/

What's a "doesn't scale" tech you've shipped anyway? 👇

---

## Post 2: Process-Based Plugins (ADR-0002)

I rejected WASM, shared libraries, and Go plugins.

Went with shell scripts instead. 🐚

Sounds crazy? Here's the ADR 👇

Requirements:
- Users can write custom tools
- Any language should work
- Plugin crash ≠ app crash
- Easy to debug

Options I rejected:

❌ **Shared libraries (.so/.dll)**
Platform-specific. Complex builds. Crash propagation.

❌ **WASM plugins**
Limited I/O. Debugging nightmare. Incomplete ecosystem.

❌ **Go plugins**
Version coupling. Platform limitations.

✅ **Process-based (stdin/stdout)**

```yaml
name: my_tool
executable: ./my_tool.sh
```

Plugin reads JSON from stdin.
Plugin writes JSON to stdout.
Plugin exits.

That's it.

Benefits:
→ Write plugins in Python, Go, Node, shell, whatever
→ Plugin crashes don't kill Buckley
→ Debug by running the script directly
→ No build complexity

Cost: ~10-50ms spawn overhead.

Worth it? Absolutely.

Simple > clever.

🔗 https://github.com/odvcencio/buckley

What plugin architecture would you choose? 👇

---

## Post 3: Plan-First Workflow (ADR-0004)

Most AI tools execute immediately.

I made mine plan first.

Here's why 👇

The problem with "just execute":

😤 No visibility into what's happening
😤 Can't pause and review
😤 Crash = start over
😤 100-step task? Good luck tracking it

My decision: Explicit plan-first workflow.

**Phase 1: Planning**
```
/plan "add user authentication"
```
→ Generates task breakdown
→ Stores in database
→ Creates trackable TODOs

**Phase 2: Execution**
```
/execute
```
→ State machine: Pending → InProgress → Done
→ Auto-checkpoint every 10 tasks
→ Self-healing retries
→ Pause anytime for review

**Phase 3: Review**
→ AI reviews changes
→ Cross-validation
→ PR generation

The key insight:

Plans are resumable. "Just doing stuff" isn't.

Crash at step 47 of 100?
`buckley` → picks up at step 47.

Not step 1. Step 47.

🔗 https://github.com/odvcencio/buckley

Do you plan before execute, or yolo? 👇

---

## Post 4: TOON Encoding (ADR-0007)

I invented a new encoding format to save tokens. 🧪

Sounds overkill? Here's the math 👇

Tool outputs go into LLM context.
Context = tokens.
Tokens = money.

JSON is verbose:
```json
{"files": ["main.go", "config.go"], "count": 2, "success": true}
```
87 characters. ~22 tokens.

TOON (my format):
```
files[main.go,config.go]count:2,success:true
```
~45 characters. ~12 tokens.

**45% reduction.**

Over a session with 200 tool calls?
That's thousands of tokens saved.
That's real money.

The decision:
```go
if useToon {
    return toon.Encode(output)
}
return json.Marshal(output)
```

Fallback to JSON if needed.
TOON by default.

"But models might not understand it!"

They do. It's still text. Still readable.
Models are smart. They adapt.

Small optimizations compound.

🔗 https://github.com/odvcencio/buckley

What's the weirdest optimization you've shipped? 👇

---

## Post 5: Context Compaction (ADR-0005)

Context windows are a lie. 🤥

"200k tokens!" they say.

Yeah, but quality tanks after 50k.

My solution: automatic compaction.

Here's how 👇

The problem:
- Long conversation = huge context
- Huge context = degraded quality
- Degraded quality = wrong answers
- Wrong answers = wasted time

My decision:

At 90% context usage:
1. Identify oldest non-system messages
2. Summarize them with fast model
3. Replace messages with summary
4. Continue with fresh context

```go
if tokenCount >= maxTokens * 0.9 {
    summary := generateSummary(oldMessages)
    replaceWithSummary(conversation, summary)
}
```

Fallback if summarization fails:
```
[47 messages truncated - summary unavailable]
```

Graceful degradation > hard failure.

Result:
✅ Infinite conversations
✅ Quality stays high
✅ No manual intervention
✅ Works across sessions

Context limits are a problem.
Compaction is a solution.

🔗 https://github.com/odvcencio/buckley

How do you handle context limits? 👇

---

## Post 6: Custom TUI Runtime (ADR-0010)

4,000 lines of custom TUI code. 📺

"Why not just use bubbletea?"

I tried. Here's the ADR 👇

What I needed:
- 60fps streaming without flicker
- Testable rendering (golden frames)
- Partial updates (dirty regions only)
- Complex layouts (panels, modals, scrolling)

What bubbletea offers:
- `View() string` - rebuild entire screen every frame
- No dirty tracking
- No backend abstraction
- Layout? Figure it out yourself.

The breaking point:

Streaming LLM output.
Every character = full re-render.
Visible flicker. Slow. Ugly.

My decision: Build custom runtime.

```go
type Widget interface {
    Measure(constraints) Size
    Layout(bounds Rect)
    Render(buf *Buffer)
    HandleMessage(msg) Result
}
```

Retained-mode buffer with:
→ Cell-level dirty tracking
→ Partial redraws (only changed regions)
→ Simulation backend for tests
→ Golden frame comparisons

Result:
✅ ~1000 chars/sec, no flicker
✅ Testable with golden frames
✅ Clean separation of concerns

Inspired by Python's Textual. Built for Go.

Sometimes you gotta build the tool to build the tool.

🔗 https://github.com/odvcencio/buckley

Have you ever built your own framework? Worth it? 👇

---

## Post 7: Tiered Approval Modes (ADR-0006)

Binary trust is broken. ⚖️

"Approve everything" = slow.
"Approve nothing" = dangerous.

I built 4 levels instead 👇

**🔒 Conservative**
Approve everything.
New users. Unfamiliar repos. Training wheels.

**⚖️ Balanced**
Auto-approve: reads, searches
Manual: writes, shell commands

**✅ Standard**
Auto-approve: safe operations
Manual: destructive ones (deletes, force push)

**🚀 Autonomous**
Full auto.
For when you trust it. Or you're feeling bold.

But here's the real feature:

**Trusted paths.**

```yaml
trust:
  level: balanced
  trusted_paths:
    - "tests/"
    - "docs/"
    - "scripts/"
```

tests/ → autonomous
src/ → needs approval

Same session. Different rules. Per directory.

Why?

Your test files don't need the same protection as your production code.

Granular control > binary switches.

🔗 https://github.com/odvcencio/buckley

How do you manage AI trust in your workflow? 👇
