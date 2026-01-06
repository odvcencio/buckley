package embeddings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Cache provides disk-based caching for embeddings
type Cache struct {
	dir string
	mu  sync.RWMutex
}

// NewCache creates a new embeddings cache
func NewCache(dir string) *Cache {
	os.MkdirAll(dir, 0755)
	return &Cache{dir: dir}
}

// Get retrieves a cached embedding
func (c *Cache) Get(key string) ([]float64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	path := filepath.Join(c.dir, "emb-"+key+".json")
	data, err := os.ReadFile(path)
	if err != nil {
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
	c.mu.Lock()
	defer c.mu.Unlock()

	path := filepath.Join(c.dir, "emb-"+key+".json")
	data, err := json.Marshal(embedding)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}

// Clear removes all cached embeddings
func (c *Cache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			os.Remove(filepath.Join(c.dir, entry.Name()))
		}
	}

	return nil
}
