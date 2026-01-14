# TUI Rendering Enhancements Design

## Overview

This document describes architectural improvements to the TUI rendering system to fix performance bugs, reduce overhead, and establish patterns for future optimization.

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

## Design

### Fix 1: Remove Global Clear

**Change:** Remove `s.buffer.Clear()` from `Screen.Render()`

**Consequence:** Widgets must clear their own bounds before rendering, or artifacts remain when widgets shrink.

**New pattern:** Add `RenderContext.Clear()` helper that widgets call at render start:

```go
func (ctx RenderContext) Clear() {
    bg := ctx.Theme.Base.BG
    style := backend.DefaultStyle().Background(backend.Color{R: bg.R, G: bg.G, B: bg.B})
    ctx.Buffer.Fill(ctx.Bounds, ' ', style)
}

// Widget usage:
func (w *ChatView) Render(ctx runtime.RenderContext) {
    ctx.Clear()
    // ... render content
}
```

**Why this works:** Each widget clears only its bounds. If a widget's content didn't change, it writes the same cells, and dirty tracking correctly identifies no change. Only actually-modified cells get sent to the backend.

### Fix 2: Style Cache

**New type:** `StyleCache` in `pkg/ui/tui/`

```go
type StyleCache struct {
    mu    sync.RWMutex
    cache map[theme.Style]backend.Style
}

func (c *StyleCache) Get(ts theme.Style) backend.Style {
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

**Integration:** Add `styleCache *StyleCache` to `WidgetApp`, initialize in constructor, clear on theme change.

### Fix 3: Widget Invalidation Foundation

**New type:** `WidgetBase` embeddable struct

```go
type WidgetBase struct {
    bounds      Rect
    needsRender bool
}

func (w *WidgetBase) Invalidate()        { w.needsRender = true }
func (w *WidgetBase) NeedsRender() bool  { return w.needsRender }
func (w *WidgetBase) ClearInvalidation() { w.needsRender = false }
func (w *WidgetBase) SetBounds(r Rect) {
    if w.bounds != r {
        w.bounds = r
        w.needsRender = true
    }
}
```

**Future use:** Screen.Render() can skip widgets where `!NeedsRender()`. Not implemented in this pass—just establishing the pattern.

### Fix 4: Render Metrics (Debug)

Add optional logging to observe dirty tracking effectiveness:

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

## Implementation Order

1. **Fix dirty tracking bypass** - Remove `Clear()` from `Screen.Render()`
2. **Add `RenderContext.Clear()`** - Helper for widget self-clearing
3. **Update widgets** - Add `ctx.Clear()` to each widget's `Render()`
4. **Add StyleCache** - New file + integration into WidgetApp
5. **Add WidgetBase** - Foundation for future selective updates
6. **Add debug metrics** - Optional render logging
7. **Document architecture** - Clarify buffer/screen relationship

## Testing Strategy

**Unit tests:**
- `TestScreen_RenderPreservesDirtyTracking` - Verify dirty count doesn't explode
- `TestStyleCache_*` - Cache hit/miss behavior
- `TestWidgetBase_Invalidate` - Invalidation state machine

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

## Non-Goals

- **Unifying runtime.Buffer and compositor.Screen** - Would be nice but high risk, low reward
- **Virtual DOM / reconciliation** - Overkill for current widget count
- **Async rendering** - Current 60fps target is achievable synchronously
