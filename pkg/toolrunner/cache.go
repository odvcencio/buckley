package toolrunner

import (
	"fmt"
	"hash/fnv"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/odvcencio/buckley/pkg/tool"
)

// toolSelectionCache is an efficient LRU cache with TTL expiration and statistics.
// It uses a doubly-linked list + map for O(1) LRU tracking and FNV-1a hashing for fast key lookups.
// All operations are thread-safe.
type toolSelectionCache struct {
	mu         sync.RWMutex
	entries    map[string]*lruNode
	head, tail *lruNode
	size       int
	ttl        time.Duration
	hits       atomic.Uint64
	misses     atomic.Uint64
	evictions  atomic.Uint64
}

// newToolSelectionCache creates a new cache with the specified size and TTL.
func newToolSelectionCache(size int, ttl time.Duration) *toolSelectionCache {
	if size <= 0 {
		size = 100
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &toolSelectionCache{
		entries: make(map[string]*lruNode, size),
		size:    size,
		ttl:     ttl,
	}
}

// hashKey generates a compact FNV-1a hash for the cache key.
func (c *toolSelectionCache) hashKey(key string) string {
	h := fnv.New64a()
	_, _ = io.WriteString(h, key)
	return fmt.Sprintf("%016x", h.Sum64())
}

// get retrieves a cached entry and updates LRU order.
// The entry's TTL is refreshed on successful access.
func (c *toolSelectionCache) get(key string) ([]string, bool) {
	hashedKey := c.hashKey(key)

	c.mu.RLock()
	node, ok := c.entries[hashedKey]
	c.mu.RUnlock()

	if !ok {
		c.misses.Add(1)
		return nil, false
	}

	entry := node.value

	if entry.IsExpired(c.ttl) {
		c.mu.Lock()
		// Double-check after acquiring write lock
		if node, stillOk := c.entries[hashedKey]; stillOk && node.value.IsExpired(c.ttl) {
			c.removeNode(node)
			c.evictions.Add(1)
		}
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, false
	}

	// Update LRU order by moving to head and refresh TTL
	c.mu.Lock()
	c.moveToHead(node)
	entry.createdAt = time.Now()
	c.mu.Unlock()

	c.hits.Add(1)
	return entry.toolNames, true
}

// set adds or updates a cache entry.
func (c *toolSelectionCache) set(key string, toolNames []string) {
	hashedKey := c.hashKey(key)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Update existing entry
	if node, ok := c.entries[hashedKey]; ok {
		node.value.toolNames = toolNames
		node.value.createdAt = time.Now()
		c.moveToHead(node)
		return
	}

	// Evict oldest if at capacity
	if len(c.entries) >= c.size {
		c.evictOldest()
	}

	// Add new entry using pooled node
	node := getNode()
	node.key = hashedKey
	node.value = &cacheEntry{
		key:       key,
		toolNames: toolNames,
		createdAt: time.Now(),
	}
	c.entries[hashedKey] = node
	c.addToHead(node)
}

// Stats returns current cache statistics.
func (c *toolSelectionCache) Stats() CacheStats {
	return CacheStats{
		Hits:      c.hits.Load(),
		Misses:    c.misses.Load(),
		Evictions: c.evictions.Load(),
	}
}

// ResetStats resets all cache statistics to zero.
func (c *toolSelectionCache) ResetStats() {
	c.hits.Store(0)
	c.misses.Store(0)
	c.evictions.Store(0)
}

// WarmCache pre-populates the cache with common contexts.
// Each context is hashed and stored with its associated tools.
func (c *toolSelectionCache) WarmCache(commonContexts []string, tools []tool.Tool) {
	toolNames := make([]string, len(tools))
	for i, t := range tools {
		toolNames[i] = t.Name()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, ctx := range commonContexts {
		hashedKey := c.hashKey(ctx)
		// Skip if already exists
		if _, ok := c.entries[hashedKey]; ok {
			continue
		}

		// Evict oldest if at capacity
		if len(c.entries) >= c.size {
			c.evictOldest()
		}

		// Add new entry using pooled node
		node := getNode()
		node.key = hashedKey
		node.value = &cacheEntry{
			key:       ctx,
			toolNames: toolNames,
			createdAt: time.Now(),
		}
		c.entries[hashedKey] = node
		c.addToHead(node)
	}
}

// WarmCacheAsync pre-populates the cache asynchronously without blocking the caller.
func (c *toolSelectionCache) WarmCacheAsync(commonContexts []string, tools []tool.Tool) {
	go c.WarmCache(commonContexts, tools)
}

// addToHead adds a node to the head (most recently used) of the LRU list.
// Must be called with lock held.
func (c *toolSelectionCache) addToHead(node *lruNode) {
	node.prev = nil
	node.next = c.head

	if c.head != nil {
		c.head.prev = node
	}
	c.head = node

	if c.tail == nil {
		c.tail = node
	}
}

// moveToHead moves an existing node to the head of the LRU list.
// Must be called with lock held.
func (c *toolSelectionCache) moveToHead(node *lruNode) {
	if node == c.head {
		return // Already at head
	}

	// Remove from current position
	if node.prev != nil {
		node.prev.next = node.next
	}
	if node.next != nil {
		node.next.prev = node.prev
	}
	if node == c.tail {
		c.tail = node.prev
	}

	// Add to head
	c.addToHead(node)
}

// evictOldest removes the oldest entry from the cache.
// Must be called with lock held.
func (c *toolSelectionCache) evictOldest() {
	if c.tail == nil {
		return // Empty cache
	}
	c.removeNode(c.tail)
	c.evictions.Add(1)
}

// removeNode removes a node from both the map and LRU list.
// The node is returned to the pool for reuse.
// Must be called with lock held.
func (c *toolSelectionCache) removeNode(node *lruNode) {
	delete(c.entries, node.key)

	// Unlink from list
	if node.prev != nil {
		node.prev.next = node.next
	} else {
		// Node is head
		c.head = node.next
	}

	if node.next != nil {
		node.next.prev = node.prev
	} else {
		// Node is tail
		c.tail = node.prev
	}

	// Return node to pool
	putNode(node)
}
