# LinkedIn Post - Custom TUI Framework

---

I deleted 3,000 lines of bubbletea code last month. 🗑️

Best decision I've made all year.

Here's what happened 👇

I was building a terminal UI. Streaming output. Modals. Vim keybinds. The works.

Bubbletea is the "standard" Go TUI library. Everyone uses it.

So I used it too.

6 weeks in, I hit a wall:

😤 Full rerender on every keystroke
😤 No layout system (manual x,y math everywhere)
😤 Compositing overlays = spaghetti
😤 Debugging the Elm loop at 2am = pain

Then I saw what Textual was doing in Python. 👀

CSS-like layouts. Widget trees. Proper compositing. Diff-based rendering.

I thought: "Why doesn't Go have this?"

So I built it.

⚡ Constraint-based layout (Textual-inspired)
⚡ Measure → Layout → Render pipeline
⚡ Double-buffered screen with cell-level diffing
⚡ Only sends CHANGED characters to terminal
⚡ Compositor with overlay layers
⚡ Focus management baked in

2,000 lines. 3 days. Worth it.

The result? 🎯

✨ 60fps streaming - smooth
✨ Zero flicker - finally
✨ Sub-ms renders - fast
✨ Code I can actually debug

Textual showed what terminal UIs could be. I just wanted that in Go.

🔗 GitHub: https://github.com/odvcencio/buckley
📚 Docs: https://buckley.draco.quest

Ever seen a library in another language and thought "why don't we have this?" 👇

---

**Alt version (confession style):**

Confession: I kept using bubbletea even though I knew it wasn't working. 😅

The signs were there:

🚩 Coordinate math everywhere
🚩 Elm update loops getting unreadable
🚩 "Just one more hack" (x47)
🚩 Flickering on fast output

Then I watched Will McGugan's Textual demos.

CSS layouts. In a terminal. Actual widget composition. Diffing that works.

I thought: "This is what TUIs should feel like."

Problem: Textual is Python. I'm writing Go.

Solution: Build my own.

⚡ Textual-style constraints
⚡ Double-buffered screens
⚡ Cell-level diffing
⚡ Layer compositor

3 days of work. Months of pain saved.

Lesson: Steal ideas from better ecosystems.

🔗 https://github.com/odvcencio/buckley

What library from another language do you wish existed in yours? 👇

---

**Alt version (numbers hook):**

3,000 lines deleted. 🗑️
2,000 lines written.
3 days of work.
$0 regrets.

I replaced bubbletea with a Textual-inspired TUI runtime.

Why mass kill Go's most popular TUI library?

Because Python's Textual showed me what terminals could actually do:

✅ CSS-like layouts
✅ Widget composition
✅ Diff-based rendering
✅ Proper focus management

Bubbletea has none of that.

So I built it for Go:

📐 Constraint-based layout
🎬 Measure → Layout → Render pipeline
⚡ Cell-level diff rendering
🎭 Compositor with layers
🎯 Built-in focus management

Textual is proof terminals don't have to feel like 1985.

Now my Go TUI doesn't either.

🔗 https://github.com/odvcencio/buckley

What's a library you've "ported" from another language's ecosystem? 👇
