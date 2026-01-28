# TUI Rendering Enhancements Design

## Overview

This document describes architectural improvements to the TUI rendering system to fix performance bugs, reduce overhead, and establish patterns for future optimization.

## Validation Notes (Current Code)

- `pkg/ui/runtime/screen.go` calls `s.buffer.Clear()` inside `Screen.Render()`. `Buffer.Clear()` calls `Fill()` and marks the full buffer dirty every frame.
- `pkg/ui/tui/app_widget.go` already uses `Buffer.DirtyCount()` to decide between full redraw and partial redraw.
- `RenderMetrics` already exists in `pkg/ui/tui/app_widget.go` and is surfaced via `WidgetApp.Metrics()` and debug dumps.
- Several widgets fill their backgrounds (header, input, status, palette, toast stack), but some do not (chat view, sidebar). Those widgets rely on the global clear today.

## Current Architecture

```
Widget.Render() → runtime.Buffer (with dirty tracking)
                       ↓
              app_widget.render()
                       ↓
              ForEachDirtyCell() → backend.SetContent()
                       ↓
                 backend.Show()
```

**Key components:**
- `runtime.Buffer` - 2D cell grid with per-cell dirty tracking
- `runtime.Screen` - Widget tree manager, calls `Render()` on layers
- `compositor.Screen` - Separate double-buffered screen (unused in main path)
- `app_widget.render()` - Orchestrates render cycle, writes to backend
- `app_widget.render()` - Uses `DirtyCount()` to choose partial vs full redraw

## Problems Identified

### 1. Dirty Tracking Bypass (Critical)

**Location:** `pkg/ui/runtime/screen.go:149-150`

```go
func (s *Screen) Render() {
    s.buffer.Clear()  // ← THIS DEFEATS DIRTY TRACKING
    // ...
}
```

`Clear()` calls `Fill()` which marks ALL cells dirty. Every frame triggers a full redraw regardless of what actually changed. The dirty tracking in `Buffer` is well-implemented but completely bypassed.

**Impact:**
- ~4800 cells marked dirty per frame (120×40)
- Full redraws instead of partial updates
- Wasted backend.SetContent() calls

### 2. Style Conversion Overhead

**Location:** `pkg/ui/tui/app_widget.go`, multiple call sites

```go
style := themeToBackendStyle(a.theme.Text)
```

Called repeatedly with the same theme styles. No caching. Each call allocates and converts color values.

**Impact:**
- Unnecessary allocations per render
- Same conversion computed multiple times per frame

### 3. Dual Buffer Systems

Two separate screen/buffer abstractions exist:

| Package | Type | Purpose |
|---------|------|---------|
| `runtime` | `Buffer` | Widget render target, dirty tracking |
| `runtime` | `Screen` | Widget tree, layers, focus |
| `compositor` | `Screen` | Double-buffered cells, diff engine |
| `compositor` | `Cell` | Rune + width + style |

The `compositor.Screen` isn't used in the main render path. `app_widget.render()` reads from `runtime.Buffer` and writes directly to the backend. This is fine, but the dual systems add confusion.

### 4. No Widget-Level Invalidation

Currently: Frame is dirty → re-render entire widget tree → diff at cell level

Missing: Widget knows it changed → only re-render that widget → smaller dirty region

Widgets have no way to signal "I changed" independent of the frame tick. Everything re-renders every frame.

### 5. Widgets That Do Not Clear Their Bounds

Some widgets (notably `ChatView` and `Sidebar`) do not fill their bounds. They depend on the global `Screen.Render()` clear. Once the global clear is removed, these widgets will leave stale content when their content shrinks or when overlays close.

## Design

### Fix 1: Remove Global Clear + Per-Widget Clear

**Change:** Remove `s.buffer.Clear()` from `Screen.Render()`.

**Consequence:** Widgets must clear their own bounds before rendering, or artifacts remain when widgets shrink.

**New pattern:** Add `RenderContext.Clear(style backend.Style)` helper so widgets can clear only their own bounds.

```go
func (ctx RenderContext) Clear(style backend.Style) {
    ctx.Buffer.Fill(ctx.Bounds, ' ', style)
}

// Widget usage:
func (w *ChatView) Render(ctx runtime.RenderContext) {
    ctx.Clear(w.bgStyle)
    // ... render content
}
```

**Why this works:** Each widget clears only its bounds. If a widget's content didn't change, the `Fill()` call writes the same cells and does not mark them dirty. Only actual changes get sent to the backend.

**Widget audit:** Update widgets that currently depend on the global clear to clear their bounds:
- `ChatView` (add `bgStyle` and clear at the top of `Render`)
- `Sidebar` (add `bgStyle` and clear at the top of `Render`)
- Audit any other widgets that render sparse content (search for `Render` methods without `Fill`)

For widgets that intentionally need transparency, skip `ctx.Clear()` or clear with `backend.DefaultStyle()` to preserve the underlying layer.

### Fix 2: Style Cache

**New type:** `StyleCache` in `pkg/ui/tui/`

```go
type StyleCache struct {
    mu    sync.RWMutex
    cache map[compositor.Style]backend.Style
}

func (c *StyleCache) Get(ts compositor.Style) backend.Style {
    // Read lock fast path
    c.mu.RLock()
    if bs, ok := c.cache[ts]; ok {
        c.mu.RUnlock()
        return bs
    }
    c.mu.RUnlock()

    // Write lock slow path
    c.mu.Lock()
    defer c.mu.Unlock()
    if bs, ok := c.cache[ts]; ok {
        return bs
    }
    bs := themeToBackendStyle(ts)
    c.cache[ts] = bs
    return bs
}

func (c *StyleCache) Clear() {
    // Call on theme change
}
```

**Integration:** Add `styleCache *StyleCache` to `WidgetApp`, initialize in constructor, clear on theme change. Replace `themeToBackendStyle(...)` call sites with `styleCache.Get(...)`. Apply `Bold/Dim/Italic` on the returned `backend.Style` as needed.

### Fix 3: Widget Invalidation Foundation (Optional)

Extend `widgets.Base` with invalidation helpers. This does not change rendering yet; it establishes a future path for skipping widgets that did not change.

```go
type Base struct {
    bounds      runtime.Rect
    needsRender bool
}

func (b *Base) Invalidate()        { b.needsRender = true }
func (b *Base) NeedsRender() bool  { return b.needsRender }
func (b *Base) ClearInvalidation() { b.needsRender = false }
func (b *Base) Layout(r runtime.Rect) {
    if b.bounds != r {
        b.bounds = r
        b.needsRender = true
    }
}
```

**Future use:** Screen.Render() can skip widgets where `!NeedsRender()`. Not implemented in this pass, just establishing the pattern.

### Fix 4: Render Metrics (Debug)

Render metrics already exist. Add an optional debug toggle to log or export them periodically.

```go
type WidgetApp struct {
    debugRender bool
}

func (a *WidgetApp) render() {
    // ... existing render code

    if a.debugRender && a.metrics.FrameCount%60 == 0 {
        log.Printf("[render] frames=%d avg=%v dropped=%.1f%% cells=%d full=%d partial=%d",
            a.metrics.FrameCount,
            a.metrics.TotalRenderTime/time.Duration(a.metrics.FrameCount),
            float64(a.metrics.DroppedFrames)/float64(a.metrics.FrameCount)*100,
            a.metrics.CellsUpdated,
            a.metrics.FullRedraws,
            a.metrics.PartialRedraws)
    }
}
```

Optionally gate this on a config flag or env var (e.g., `BUCKLEY_RENDER_DEBUG=1`) so it does not spam logs in normal use.

### Documentation Clarification

Add package-level docs explaining the buffer architecture:

```go
// pkg/ui/runtime/buffer.go
//
// Widget Rendering Architecture:
//
// Widgets render to runtime.Buffer via RenderContext. The Buffer tracks
// which cells changed (dirty tracking). app_widget.render() iterates
// dirty cells and writes them to the backend.
//
// compositor.Screen exists as an alternative for pure-ANSI output but
// is not used in the tcell backend path.
```

## Implementation Plan

### Phase 1: Dirty Tracking Fix

1. Remove `s.buffer.Clear()` from `Screen.Render()` in `pkg/ui/runtime/screen.go`.
2. Add `RenderContext.Clear(style backend.Style)` (new helper in `pkg/ui/runtime`).
3. Update widgets that do not currently clear:
   - `pkg/ui/widgets/chatview.go` (add `bgStyle` and clear at the top of `Render`)
   - `pkg/ui/widgets/sidebar.go` (add `bgStyle` and clear at the top of `Render`)
   - Audit any other widgets that render sparse content (search for `Render` methods without `Fill`).
4. Ensure root layout still covers the screen without relying on global clears.

### Phase 2: Style Cache

1. Add `StyleCache` in `pkg/ui/tui/style_cache.go`.
2. Add `styleCache` to `WidgetApp` and initialize on startup.
3. Replace `themeToBackendStyle` call sites in `pkg/ui/tui/app_widget.go` with cached lookups.
4. Clear cache if theme is swapped or reloaded.

### Phase 3: Invalidation Foundation (Optional)

1. Extend `pkg/ui/widgets/base.go` to include `needsRender` and helpers.
2. Update `Layout` to set `needsRender` when bounds change.
3. Add a lightweight interface (`Invalidatable`) for future selective rendering.

### Phase 4: Documentation + Debugging

1. Add architecture notes in `pkg/ui/runtime/buffer.go`.
2. Add a debug toggle to print render metrics periodically.

## Testing Strategy

**Unit tests:**
- `TestScreen_RenderPreservesDirtyTracking` - Verify dirty count does not explode
- `TestStyleCache_*` - Cache hit/miss behavior
- `TestWidgetBase_Invalidate` - Invalidation state machine
- `TestSidebar_Render_BackgroundFill` - Ensure background fill for sidebar
- `TestChatView_Render_BackgroundFill` - Ensure background fill for chat view

**Benchmarks:**
- `BenchmarkRender_FullFrame` - Baseline full render
- `BenchmarkRender_PartialUpdate` - Partial update performance
- `BenchmarkStyleCache_Hit` vs `BenchmarkStyleConversion_Uncached`

**Manual verification:**
- Enable debug render logging
- Confirm partial redraws dominate during normal use
- Confirm full redraws only on resize

## Expected Outcomes

| Metric | Before | After |
|--------|--------|-------|
| Cells dirty per frame (idle) | ~4800 (all) | ~0 |
| Cells dirty per frame (typing) | ~4800 (all) | ~100 (input area) |
| Style conversions per frame | Many | Cached |
| Partial vs full redraws | All full | Mostly partial |

## Risks and Mitigations

- Risk: Removing global clear leaves stale content if a widget does not fill its bounds.
  Mitigation: Audit `Render` methods and add explicit background fills for sparse widgets.
- Risk: Style cache uses `compositor.Style` as a map key; changes to that type could break cache keys.
  Mitigation: Keep cache local to TUI and add a small unit test that verifies key stability.

## Acceptance Criteria

- Idle renders result in `DirtyCount()` near zero and mostly partial redraws.
- Closing overlays does not leave artifacts.
- Sidebar and chat view backgrounds render correctly when content shrinks.
- `./scripts/test.sh` passes.

## Non-Goals

- Unifying `runtime.Buffer` and `compositor.Screen` - High risk, low reward for this pass
- Virtual DOM or reconciliation - Overkill for current widget count
- Async rendering - Current 60fps target is achievable synchronously
