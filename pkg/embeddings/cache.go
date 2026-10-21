package embeddings

import (
	"encoding/json"

	"m31labs.dev/buckley/internal/filecache"
)

const (
	DefaultCacheMaxBytes      = 32 << 20
	DefaultCacheMaxEntries    = 2048
	DefaultCacheMaxEntryBytes = 256 << 10
)

// Cache provides disk-based caching for embeddings
type Cache struct {
	files *filecache.Cache
}

// NewCache creates a new embeddings cache
func NewCache(dir string) *Cache {
	return &Cache{files: filecache.New(dir, "emb-", filecache.Limits{Bytes: DefaultCacheMaxBytes, Entries: DefaultCacheMaxEntries, EntryBytes: DefaultCacheMaxEntryBytes})}
}

// Get retrieves a cached embedding
func (c *Cache) Get(key string) ([]float64, bool) {
	data, ok := c.files.Get(key)
	if !ok {
		return nil, false
	}

	var embedding []float64
	if err := json.Unmarshal(data, &embedding); err != nil {
		return nil, false
	}

	return embedding, true
}

// Set stores an embedding in the cache
func (c *Cache) Set(key string, embedding []float64) error {
	data, err := json.Marshal(embedding)
	if err != nil {
		return err
	}

	return c.files.Set(key, data)
}

// Clear removes all cached embeddings
func (c *Cache) Clear() error {
	return c.files.Clear()
}
