package toolrunner

import (
	"strings"
	"sync"
	"time"
)

// cacheEntry represents a single cache entry with metadata.
type cacheEntry struct {
	key       string
	toolNames []string
	createdAt time.Time
}

// IsExpired checks if the entry has exceeded its TTL.
func (e *cacheEntry) IsExpired(ttl time.Duration) bool {
	return time.Since(e.createdAt) > ttl
}

// lruNode represents a node in the doubly-linked list for O(1) LRU operations.
type lruNode struct {
	key        string
	value      *cacheEntry
	prev, next *lruNode
}

// nodePool provides memory-efficient recycling of lruNode instances
// to reduce GC pressure during high cache churn.
var nodePool = sync.Pool{
	New: func() any {
		return &lruNode{}
	},
}

// toolCallRecordPool provides memory-efficient recycling of ToolCallRecord slices
// to reduce GC pressure during tool execution.
var toolCallRecordPool = sync.Pool{
	New: func() any {
		// Pre-allocate with capacity for typical batch sizes
		s := make([]ToolCallRecord, 0, 8)
		return &s
	},
}

// builderPool provides memory-efficient recycling of strings.Builder
// instances for tool result formatting.
var builderPool = sync.Pool{
	New: func() any {
		return &strings.Builder{}
	},
}

// getNode retrieves a node from the pool or allocates a new one.
func getNode() *lruNode {
	return nodePool.Get().(*lruNode)
}

// putNode returns a node to the pool for reuse.
func putNode(n *lruNode) {
	n.key = ""
	n.value = nil
	n.prev = nil
	n.next = nil
	nodePool.Put(n)
}

// acquireToolCallRecordSlice retrieves a ToolCallRecord slice from the pool.
// Returns a slice with 0 length but pre-allocated capacity.
func acquireToolCallRecordSlice() []ToolCallRecord {
	s := toolCallRecordPool.Get().(*[]ToolCallRecord)
	return (*s)[:0]
}

// releaseToolCallRecordSlice returns a ToolCallRecord slice to the pool.
// The slice should not be used after this call.
func releaseToolCallRecordSlice(s []ToolCallRecord) {
	if s == nil {
		return
	}
	// Only pool reasonably-sized slices to avoid memory bloat
	if cap(s) > 1024 {
		return
	}
	// Clear the slice to allow GC of referenced data
	for i := range s {
		s[i] = ToolCallRecord{}
	}
	toolCallRecordPool.Put(&s)
}

// acquireBuilder retrieves a strings.Builder from the pool.
func acquireBuilder() *strings.Builder {
	b := builderPool.Get().(*strings.Builder)
	b.Reset()
	return b
}

// releaseBuilder returns a strings.Builder to the pool.
func releaseBuilder(b *strings.Builder) {
	if b == nil {
		return
	}
	// Don't pool builders with very large buffers
	if b.Cap() > 64*1024 {
		return
	}
	builderPool.Put(b)
}
