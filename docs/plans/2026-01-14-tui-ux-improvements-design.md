# TUI UX Improvements Design

Date: 2026-01-14

## Overview

Comprehensive UX overhaul for Buckley's TUI focused on visual polish, information density, and adaptive layouts. The TUI should feel like a living system that breathes with activity - calm when idle, energized when working.

## Design Goals

- **Rich + Dense**: Glowing accents, animated indicators, but information-forward with clear hierarchy
- **Context-Aware**: UI adapts to what's happening, surfaces relevant info automatically
- **Web Escape Hatch**: Deep links to web UI for detailed views when needed
- **Sacred Elements**: Keep the pulsing cursor

## Current State (Already Implemented)

The following elements already exist in the current TUI and are not deferred:

- Code-block chrome (language header with `[copy]`/`[open]`) and line numbers (hover for short blocks)
- Message metadata footer (always/hover modes)
- Toasts with slide/fade animation
- Web UI deep links (configurable base URL + open/copy)
- Progress ring in the sidebar (with glyph fallback in the presence strip)

---

## Visual Language

### Core Principle: "Alive and Aware"

| Element | Idle State | Active State |
|---------|------------|--------------|
| Borders | Dim, single-line | Glowing accent, optional double-line |
| Backgrounds | Deep black (`#0c0c0c`) | Subtle gradient wash toward activity |
| Text | Muted secondary | Bright, high contrast |
| Indicators | Static or hidden | Animated (pulse, spin, sparkle) |

### Expanded Accent Palette

| Color | Hex | Usage |
|-------|-----|-------|
| Amber/Gold | `#FFB74D` | Primary accent, user actions, success |
| Electric Blue | `#4FC3F7` | Active processes, streaming, "working" |
| Soft Purple | existing | Tool output, system activity |
| Coral | `#FF8A65` | Warnings, errors, attention needed |
| Teal | `#4DB6AC` | Informational, metrics, neutral highlights |

### Glow Effects

Strategic use of "glow" via adjacent dim cells of the same hue:

```
░░▓██▓░░   ← Glow around focused element
```

Reserved for: focused widget borders, active progress indicators, urgent notifications.

### Animation Budget

- 60fps tick already exists
- Max 3 simultaneous animations visible
- Types: pulse (cursor), spin (progress), fade (transitions), sparkline (metrics)

---

## Smart Sidebar - The Context Panel

### Concept: Morphing Priority Stack

The sidebar becomes a single intelligent panel that reorders and resizes its sections based on what's happening. Not tabs - a fluid stack where relevant content floats to top and expands, irrelevant content compresses or hides.

### Priority Signals

| Activity | Sidebar Response |
|----------|------------------|
| Tools executing | Tools section expands to top, shows live output preview |
| Planning phase | Plan tasks expand, progress ring prominent |
| Streaming response | Sidebar dims slightly, minimizes to give chat room |
| High context usage | Token/context section pulses, moves up |
| RLM active | RLM status expands with refinement progress |
| Idle | Balanced view, everything visible but compact |

### Section Components

**1. Plan Progress** (when active)
- Circular progress ring with percentage
- Task list with checkmarks, current task highlighted with glow
- Collapsed: just the ring + "3/7 tasks"

**2. Active Tools** (when running)
- Tool name + spinner animation
- Mini elapsed timer
- One-line output preview (last line or truncated)
- Click → opens web UI tool detail

**3. Context Gauge**
- Horizontal bar or arc showing context usage
- Color gradient: green → amber → coral as it fills
- Sparkline showing burn rate over last N turns
- Click → opens web UI session stats

**4. Scratch Pad / Notes** (lowest priority)
- Collapses to single line when other content needs space
- Expandable on hover/focus

### Collapse Behavior

When terminal is narrow (<100 cols), sidebar auto-hides but leaves a thin "presence strip" (2-3 chars) showing:
- Colored dot for activity status
- Mini progress arc if plan active
- Pulse if attention needed

`Ctrl+B` or clicking strip expands it.

---

## Chat Area - Message Cards & Visual Hierarchy

### Concept: Distinct Message Identities

Each message type gets a unique visual treatment for instant parsing of who said what.

### Message Anatomy

```
┌─ role indicator ─────────────────────────────────────────┐
│ ░░ gradient accent strip                                 │
│                                                          │
│  Message content here with proper padding                │
│                                                          │
│  ┌────────────────────────────────────────────────────┐  │
│  │ ```python                              [copy] ↗    │  │
│  │ def hello():                                       │  │
│  │     print("world")                                 │  │
│  └────────────────────────────────────────────────────┘  │
│                                                          │
│                          tokens: 142 · 0.3s · 2:34pm  ░░ │
└──────────────────────────────────────────────────────────┘
```

### Role Visual Treatments

| Role | Left Strip | Background | Icon/Prefix |
|------|-----------|------------|-------------|
| User | Fresh green | Subtle green wash | `λ` or `>` |
| Assistant | Amber/gold | Near-black | None (clean) |
| Tool Output | Purple | Slight purple tint | `⚙` or tool name |
| System | Dim gray | Transparent | `•` |
| Thinking | Soft blue, animated | Slight blue | `◐` spinning |

### Code Block Enhancements

Already implemented: code headers with `[copy]`/`[open]` and line numbers (hover for short blocks).
Use this section to refine visuals and behavior.

- Header bar with language label + copy button + "open in web" link
- Subtle syntax highlighting (enhance contrast)
- Line numbers on hover or for blocks >10 lines
- Diff blocks get green/red gutter treatment
- Long blocks: collapsed with "Show N more lines" expander

### Inline Metadata

Already implemented with `message_metadata` modes; refine styling and spacing.

Subtle footer on each message (appears on hover or configurable always-on):
- Token count for that message
- Generation time
- Timestamp
- Model used (if multi-model)

### Streaming State

While assistant is streaming:
- Soft glow on message border
- Typing indicator: `▍` cursor at end (existing pulse)
- "Thinking" messages show animated `◐◑◒◓` spinner

### Message Spacing

Tighter spacing between same-role sequential messages, more breathing room on role transitions. Creates visual "paragraphs" in the conversation.

---

## Adaptive Layouts

### Concept: Content-Aware Responsive Design

Layout adapts to three factors:
1. Terminal dimensions (width × height)
2. Content state (planning, streaming, idle, tool-heavy)
3. User focus (where attention should be)

### Width Breakpoints

| Width | Name | Layout |
|-------|------|--------|
| <80 | Compact | No sidebar, status bar minimal, header collapses to icon |
| 80-119 | Standard | Sidebar as presence strip (expandable), full header |
| 120-159 | Comfortable | Sidebar visible (~25 cols), balanced proportions |
| 160+ | Spacious | Wider sidebar (~35 cols), message max-width for readability |

### Height Adaptations

| Height | Adaptation |
|--------|------------|
| <20 | Header hidden, status bar merges into input area |
| 20-30 | Compact header (single line), full status |
| 30+ | Full chrome, more chat history visible |

### Content-Driven Shifts

| State | Layout Response |
|-------|-----------------|
| Long code block in chat | Sidebar narrows temporarily to give code room |
| Multiple tools running | Sidebar expands, chat accepts narrower width |
| Deep in planning | Plan section dominates sidebar, other sections minimize |
| Approval dialog open | Background dims, dialog gets glow border |
| Streaming long response | Input area shrinks to 2 lines, maximize chat |

### Smooth Transitions

Layout transitions are optional; existing animations already cover spinners and toasts.
If added, keep transitions subtle and deterministic.

Layout changes animate over ~150ms:
- Width changes: content reflows with easing
- Section expand/collapse: slide animation
- Opacity shifts: fade transitions

No jarring snaps. The UI "breathes" into new shapes.

### User Overrides

- `Ctrl+B` - Force toggle sidebar regardless of auto behavior
- `Ctrl+Shift+F` - "Focus mode" - hide all chrome, just chat + input
- Sidebar width draggable (if mouse support permits)
- Overrides persist until state change or manual reset

---

## Progress Indicators & Animated Feedback

### Progress Ring (Plan Tasks)

Already implemented in the sidebar when space allows (glyph fallback in presence strip).
Use this section to refine the visuals and behavior.

```
    ╭───╮
   ╱  ●  ╲      ← Filled arc shows progress (amber glow on active edge)
  │   3   │     ← Count or percentage in center
   ╲  /7 ╱
    ╰───╯
```

- Arc fills clockwise as tasks complete
- Active edge has subtle glow/pulse
- Center shows `done/total` or `%`
- On completion: brief sparkle animation, then settles to checkmark

### Tool Execution Spinners

| Tool Type | Spinner |
|-----------|---------|
| File read/write | `◐◓◑◒` (rotating half-circle) |
| Shell command | `⣾⣽⣻⢿⡿⣟⣯⣷` (braille dots, "computing") |
| Search/grep | `◇◈◆◈` (pulsing diamond) |
| Web fetch | `○◔◑◕●◕◑◔` (filling/emptying circle) |
| LLM call | `∙∘○◎○∘` (ripple outward) |

### Context Burn Gauge

```
Context ████████████░░░░░░░░░░░░░░ 47% ↗
         ╰── green ──╯╰─ amber ─╯╰ red ╯
```

- Gradient fill: green → amber → coral
- Small `↗` or `↘` arrow showing burn trend
- Sparkline option: `▁▂▃▃▄▅▅▆▆▇ 47%`

### Token Counter Animation

- Numbers "roll" like an odometer (each digit animates)
- Subtle flash on cost when it increments
- Color shift if approaching budget warning

### Streaming Indicator

While model is generating:
- Soft animated gradient "wave" along message border
- Or: traveling dot along top edge `·───●────────`
- Intensity correlates with tokens/sec

### Toast Notifications

Already implemented with slide/fade animation; refine styling and interactions as needed.

- Slide-up animation (150ms)
- Brief glow on arrival
- Auto-dismiss with fade-out
- Color-coded border: success (green), warning (amber), error (coral)

### State Transition Flourishes

| Transition | Effect |
|------------|--------|
| Task completes | Brief checkmark flash, progress ring pulses once |
| Tool finishes | Spinner → checkmark morph, row dims after 2s |
| Plan completes | Ring fills, sparkle burst, then fades to static |
| Error occurs | Border flash red, shake animation (subtle, 2-3 frames) |
| Context compaction | Gauge "resets" with wipe animation |

---

## Web UI Integration

Already implemented with `web_ui.base_url` and open/copy behavior; expand targets where useful.

### Clickable Elements

| TUI Element | Click/Shortcut | Opens |
|-------------|----------------|-------|
| Session ID in header | Click or `Alt+W` | Web session detail view |
| Token/cost in status bar | Click | Web billing/usage dashboard |
| Plan progress ring | Click | Web plan visualization (Gantt, dependency graph) |
| Individual plan task | Click | Web task detail with full context |
| Tool execution row | Click | Web tool execution log with full output |
| Context gauge | Click | Web session stats (token history, compaction events) |
| Error toast | Click | Web error detail with stack trace |
| Model name in header | Click | Web model config / routing rules |

### Keyboard Shortcuts

- `Alt+W` - Open current session in web
- `Alt+Shift+W` - Open web dashboard (session list)

### URL Configuration

```yaml
web_ui:
  base_url: "https://buckley.internal.company.com"  # or http://localhost:8080
```

Resolution order:
1. Explicit `web_ui.base_url` in config
2. `BUCKLEY_WEB_URL` environment variable
3. Auto-detect local server on default port
4. Disabled (elements show "Web UI not configured")

### Visual Affordance

Clickable elements:
- Underline on hover (if mouse tracking)
- Small `↗` icon appears on focus
- Tooltip hint for shortcut

### Copy Instead of Open

`Shift+Click` copies the web URL to clipboard instead of opening.

---

## Implementation

### New Theme Colors

```go
// Additions to theme.Theme
ElectricBlue    // #4FC3F7 - Active processes, streaming
Coral           // #FF8A65 - Warnings, errors, attention
Teal            // #4DB6AC - Informational, metrics

// Glow variants (dimmed for adjacent cells)
AccentGlow, BlueGlow, PurpleGlow, CoralGlow
```

### New Widget Components

| Widget | Purpose |
|--------|---------|
| `ProgressRing` | Circular arc with center label, glow edge |
| `Sparkline` | Mini line chart for trends |
| `Gauge` | Horizontal bar with gradient fill |
| `AnimatedSpinner` | Configurable spinner styles |
| `MessageCard` | Wrapper with role strip, metadata footer |
| `PresenceStrip` | Collapsed sidebar indicator |

### Layout System Additions

- `Adaptive` container - responds to breakpoints
- `Collapsible` wrapper - expand/collapse with animation
- `Priority` stack - auto-reorders children by priority signal
- Animation support in `Flex` for width/height transitions

### Config Additions

```yaml
ui:
  animations: true          # Master toggle
  transition_duration: 150  # ms
  sidebar_auto_hide: true   # Collapse on narrow terminals
  message_metadata: "hover" # "always" | "hover" | "never"
  focus_mode_shortcut: "ctrl+shift+f"

web_ui:
  base_url: ""              # Auto-detect if empty
```

### Implementation Phases

1. **Foundation** - New theme colors, glow rendering utility, animation tick integration
2. **Progress Components** - ProgressRing, Sparkline, Gauge, AnimatedSpinner (refine existing widgets)
3. **Sidebar Overhaul** - Priority stack, collapsible sections, presence strip
4. **Chat Enhancements** - MessageCard wrapper, code block chrome, metadata footer (refine existing)
5. **Adaptive Layout** - Breakpoint system, content-driven shifts, transitions
6. **Web Integration** - Clickable elements, URL generation, config handling (refine existing)
7. **Polish** - Transition animations, state flourishes, edge cases

---

## Animation Inventory

| Animation | Location | Trigger |
|-----------|----------|---------|
| Pulse | Cursor, active progress edge | Continuous |
| Spin | Tool execution rows | Tool running |
| Glow | Focused borders, notifications | Focus, arrival |
| Fade | Layout transitions, toasts | State changes |
| Roll | Token/cost counters | Value updates |
| Sparkle | Plan completion | Task/plan finish |
| Wave | Streaming message border | LLM generating |
