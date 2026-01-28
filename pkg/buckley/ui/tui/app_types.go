// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"sync"
	"time"
)

// ============================================================================
// FILE: app_types.go
// PURPOSE: Shared types, constants, and layout utilities
// FUNCTIONS:
//   - layoutForWidth
//   - layoutForScreen
// ============================================================================

// RenderMetrics tracks rendering performance statistics.
type RenderMetrics struct {
	FrameCount      int64         // Total frames rendered
	DroppedFrames   int64         // Frames skipped due to being too slow
	TotalRenderTime time.Duration // Total time spent rendering
	LastFrameTime   time.Duration // Duration of last frame
	CellsUpdated    int64         // Cells updated in last frame
	FullRedraws     int64         // Number of full screen redraws
	PartialRedraws  int64         // Number of partial redraws
}

// ============================================================================
// RENDER PIPELINE OPTIMIZATION TYPES
// ============================================================================

// dirtyRect represents a rectangular region that needs to be redrawn.
type dirtyRect struct {
	MinX, MinY int
	MaxX, MaxY int
}

// intersects returns true if two dirty rectangles intersect.
func (r dirtyRect) intersects(other dirtyRect) bool {
	return r.MinX < other.MaxX && r.MaxX > other.MinX &&
		r.MinY < other.MaxY && r.MaxY > other.MinY
}

// union merges two dirty rectangles into one that covers both.
func (r dirtyRect) union(other dirtyRect) dirtyRect {
	return dirtyRect{
		MinX: minInt(r.MinX, other.MinX),
		MinY: minInt(r.MinY, other.MinY),
		MaxX: maxInt(r.MaxX, other.MaxX),
		MaxY: maxInt(r.MaxY, other.MaxY),
	}
}

// contains returns true if this rectangle fully contains another.
func (r dirtyRect) contains(other dirtyRect) bool {
	return r.MinX <= other.MinX && r.MaxX >= other.MaxX &&
		r.MinY <= other.MinY && r.MaxY >= other.MaxY
}

// isEmpty returns true if the rectangle has no area.
func (r dirtyRect) isEmpty() bool {
	return r.MinX >= r.MaxX || r.MinY >= r.MaxY
}

// renderOp represents a pending render operation in the queue.
type renderOp struct {
	Region  dirtyRect
	Type    renderOpType
	Priority int
}

// renderOpType indicates the type of render operation.
type renderOpType int

const (
	opRenderRegion renderOpType = iota
	opRenderFull
	opSwapBuffers
)

// messageRenderCache caches rendered message content to avoid recomputation.
type messageRenderCache struct {
	entries    map[string]*cachedMessage
	wrapCache  map[wrapCacheKey][]string
	viewCache  map[viewCacheKey]viewCacheEntry
	maxEntries int
	mu         sync.RWMutex
}

// cachedMessage stores pre-rendered message content.
type cachedMessage struct {
	Content     string
	Wrapped     []string
	LineCount   int
	LastAccess  time.Time
}

// wrapCacheKey uniquely identifies a text wrapping operation.
type wrapCacheKey struct {
	Text   string
	Width  int
}

// viewCacheKey uniquely identifies a viewport calculation.
type viewCacheKey struct {
	ScrollOffset int
	ViewHeight   int
	TotalLines   int
}

// viewCacheEntry stores cached viewport calculations.
type viewCacheEntry struct {
	VisibleStart int
	VisibleEnd   int
	LastAccess   time.Time
}

// newMessageRenderCache creates a new message render cache.
func newMessageRenderCache() *messageRenderCache {
	return &messageRenderCache{
		entries:    make(map[string]*cachedMessage),
		wrapCache:  make(map[wrapCacheKey][]string),
		viewCache:  make(map[viewCacheKey]viewCacheEntry),
		maxEntries: 1000,
	}
}

// get retrieves a cached message entry.
func (c *messageRenderCache) get(key string) (*cachedMessage, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if ok {
		c.mu.Lock()
		entry.LastAccess = time.Now()
		c.mu.Unlock()
	}
	return entry, ok
}

// set stores a message in the cache.
func (c *messageRenderCache) set(key string, entry *cachedMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	// Evict oldest if at capacity
	if len(c.entries) >= c.maxEntries {
		var oldestKey string
		var oldestTime time.Time
		first := true
		for k, v := range c.entries {
			if first || v.LastAccess.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.LastAccess
				first = false
			}
		}
		if oldestKey != "" {
			delete(c.entries, oldestKey)
		}
	}
	
	entry.LastAccess = time.Now()
	c.entries[key] = entry
}

// getWrapped retrieves cached wrapped text.
func (c *messageRenderCache) getWrapped(text string, width int) ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	lines, ok := c.wrapCache[wrapCacheKey{Text: text, Width: width}]
	return lines, ok
}

// setWrapped stores wrapped text in cache.
func (c *messageRenderCache) setWrapped(text string, width int, lines []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Limit wrap cache size
	if len(c.wrapCache) >= c.maxEntries*2 {
		// Clear half the cache when full
		newCache := make(map[wrapCacheKey][]string)
		count := 0
		for k, v := range c.wrapCache {
			if count >= len(c.wrapCache)/2 {
				newCache[k] = v
			}
			count++
		}
		c.wrapCache = newCache
	}
	c.wrapCache[wrapCacheKey{Text: text, Width: width}] = lines
}

// invalidate clears all cached entries.
func (c *messageRenderCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*cachedMessage)
	c.wrapCache = make(map[wrapCacheKey][]string)
	c.viewCache = make(map[viewCacheKey]viewCacheEntry)
}

// dirtyRegionManager manages dirty regions for optimized redraws.
type dirtyRegionManager struct {
	regions []dirtyRect
	full    bool
	mu      sync.RWMutex
}

// newDirtyRegionManager creates a new dirty region manager.
func newDirtyRegionManager() *dirtyRegionManager {
	return &dirtyRegionManager{
		regions: make([]dirtyRect, 0, 8),
	}
}

// add adds a dirty region, merging with existing regions if possible.
func (d *dirtyRegionManager) add(r dirtyRect) {
	d.mu.Lock()
	defer d.mu.Unlock()
	
	if d.full || r.isEmpty() {
		return
	}
	
	// Clip to screen bounds (assume reasonable max size)
	if r.MinX < 0 {
		r.MinX = 0
	}
	if r.MinY < 0 {
		r.MinY = 0
	}
	if r.MaxX > 1000 {
		r.MaxX = 1000
	}
	if r.MaxY > 1000 {
		r.MaxY = 1000
	}
	
	// Try to merge with existing regions
	for i, existing := range d.regions {
		if existing.intersects(r) || d.adjacent(existing, r) {
			d.regions[i] = existing.union(r)
			// Merge any other intersecting regions
			d.mergeOverlapping()
			return
		}
	}
	
	// Add as new region
	d.regions = append(d.regions, r)
	
	// If too many regions, switch to full redraw
	if len(d.regions) > 10 {
		d.full = true
		d.regions = d.regions[:0]
	}
}

// adjacent returns true if two rectangles are adjacent (touching but not overlapping).
func (d *dirtyRegionManager) adjacent(a, b dirtyRect) bool {
	// Check horizontal adjacency
	horizAdj := (a.MaxX == b.MinX || b.MaxX == a.MinX) &&
		a.MinY < b.MaxY && b.MinY < a.MaxY
	// Check vertical adjacency
	vertAdj := (a.MaxY == b.MinY || b.MaxY == a.MinY) &&
		a.MinX < b.MaxX && b.MinX < a.MaxX
	return horizAdj || vertAdj
}

// mergeOverlapping merges any overlapping regions.
func (d *dirtyRegionManager) mergeOverlapping() {
	changed := true
	for changed && len(d.regions) > 1 {
		changed = false
		for i := 0; i < len(d.regions)-1; i++ {
			for j := i + 1; j < len(d.regions); j++ {
				if d.regions[i].intersects(d.regions[j]) || d.adjacent(d.regions[i], d.regions[j]) {
					d.regions[i] = d.regions[i].union(d.regions[j])
					// Remove j
					d.regions = append(d.regions[:j], d.regions[j+1:]...)
					changed = true
					break
				}
			}
			if changed {
				break
			}
		}
	}
}

// getRegions returns the current dirty regions and whether a full redraw is needed.
func (d *dirtyRegionManager) getRegions() ([]dirtyRect, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.full {
		return nil, true
	}
	// Return a copy
	result := make([]dirtyRect, len(d.regions))
	copy(result, d.regions)
	return result, false
}

// clear marks all regions as clean.
func (d *dirtyRegionManager) clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.regions = d.regions[:0]
	d.full = false
}

// setFull marks the entire screen as dirty.
func (d *dirtyRegionManager) setFull() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.full = true
	d.regions = d.regions[:0]
}

// minInt returns the minimum of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// maxInt returns the maximum of two ints.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

const (
	minInputHeight       = 2   // rows reserved for input
	minChatHeight        = 4   // minimum chat viewport height in rows
	sidebarStandardWidth = 25  // columns for standard sidebar layout
	sidebarWideWidth     = 35  // columns for wide sidebar layout
	sidebarMinWidth      = 120 // screen width threshold for standard sidebar
	sidebarWideMinWidth  = 160 // screen width threshold for wide sidebar
)

type layoutSpec struct {
	sidebarVisible  bool
	presenceVisible bool
	sidebarWidth    int
	showHeader      bool
	showStatus      bool
}

func layoutForWidth(width int, hasSidebarContent bool) layoutSpec {
	if !hasSidebarContent {
		return layoutSpec{}
	}
	switch {
	case width >= sidebarWideMinWidth:
		return layoutSpec{sidebarVisible: true, sidebarWidth: sidebarWideWidth}
	case width >= sidebarMinWidth:
		return layoutSpec{sidebarVisible: true, sidebarWidth: sidebarStandardWidth}
	default:
		return layoutSpec{presenceVisible: true}
	}
}

func layoutForScreen(width, height int, hasSidebarContent, focusMode bool) layoutSpec {
	if focusMode {
		return layoutSpec{showHeader: false, showStatus: false}
	}
	spec := layoutForWidth(width, hasSidebarContent)
	spec.showHeader = height >= 20
	spec.showStatus = height >= 20
	return spec
}
