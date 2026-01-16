package tui

import (
	"sync"

	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/compositor"
	uistyle "github.com/odvcencio/buckley/pkg/ui/style"
)

// StyleCache caches compositor to backend style conversions.
type StyleCache struct {
	mu    sync.RWMutex
	cache map[compositor.Style]backend.Style
}

// NewStyleCache creates an initialized style cache.
func NewStyleCache() *StyleCache {
	return &StyleCache{cache: make(map[compositor.Style]backend.Style)}
}

// Get returns the cached backend style for the compositor style.
func (c *StyleCache) Get(style compositor.Style) backend.Style {
	if c == nil {
		return uistyle.ToBackend(style)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = make(map[compositor.Style]backend.Style)
	}
	if cached, ok := c.cache[style]; ok {
		return cached
	}
	converted := uistyle.ToBackend(style)
	c.cache[style] = converted
	return converted
}

// Clear resets the cache.
func (c *StyleCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.cache = make(map[compositor.Style]backend.Style)
	c.mu.Unlock()
}
