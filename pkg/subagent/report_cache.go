package subagent

const (
	DefaultCompletedRunCacheEntries       = 64
	DefaultCompletedRunCacheBytes   int64 = 4 * 1024 * 1024
)

type completedRunCacheEntry struct {
	id    string
	bytes int64
}

// WithCompletedRunCacheLimits bounds local copies of durably stored reports.
// Nonpositive limits use the defaults. Active workers retain their snapshots
// until evidence publication and execution cleanup finish.
func WithCompletedRunCacheLimits(entries int, bytes int64) CoordinatorOption {
	return func(c *Coordinator) {
		if entries > 0 {
			c.completedCacheEntries = entries
		}
		if bytes > 0 {
			c.completedCacheLimit = bytes
		}
	}
}

func (c *Coordinator) cacheCompletedRun(snapshot Snapshot) {
	c.mu.Lock()
	c.completedCache = append(c.completedCache, completedRunCacheEntry{id: snapshot.ID, bytes: int64(len(snapshot.Output))})
	c.completedCacheBytes += int64(len(snapshot.Output))
	c.mu.Unlock()
	c.trimCompletedCache()
}

func (c *Coordinator) trimCompletedCache() {
	if c.manager == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for index := 0; index < len(c.completedCache) &&
		(len(c.completedCache) > c.completedCacheEntries || c.completedCacheBytes > c.completedCacheLimit); {
		entry := c.completedCache[index]
		if !c.manager.evictCompleted(entry.id) {
			index++
			continue
		}
		c.completedCacheBytes -= entry.bytes
		copy(c.completedCache[index:], c.completedCache[index+1:])
		c.completedCache[len(c.completedCache)-1] = completedRunCacheEntry{}
		c.completedCache = c.completedCache[:len(c.completedCache)-1]
		c.dropCachedRunLocked(entry.id)
	}
}

func (c *Coordinator) dropCachedRunLocked(id string) {
	delete(c.runs, id)
	delete(c.evidenceByRun, id)
	delete(c.durabilityError, id)
	delete(c.mailboxes, id)
}
