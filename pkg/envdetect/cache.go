package envdetect

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"m31labs.dev/buckley/internal/filecache"
)

const (
	DefaultCacheMaxBytes      = 4 << 20
	DefaultCacheMaxEntries    = 128
	DefaultCacheMaxEntryBytes = 256 << 10
)

// Cache provides disk-based caching for environment profiles
type Cache struct {
	files *filecache.Cache
}

// NewCache creates a new cache instance
func NewCache(dir string) *Cache {
	return &Cache{files: filecache.New(dir, "envdetect-", filecache.Limits{Bytes: DefaultCacheMaxBytes, Entries: DefaultCacheMaxEntries, EntryBytes: DefaultCacheMaxEntryBytes})}
}

// Get retrieves a cached environment profile
func (c *Cache) Get(key string) (*EnvironmentProfile, bool) {
	data, ok := c.files.Get(key)
	if !ok {
		return nil, false
	}

	var profile EnvironmentProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, false
	}

	return &profile, true
}

// Set stores an environment profile in the cache
func (c *Cache) Set(key string, profile *EnvironmentProfile) error {
	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}

	return c.files.Set(key, data)
}

// computeCacheKey generates a hash of all lockfiles
func computeCacheKey(rootPath string, lockfiles []string) string {
	h := sha256.New()
	for _, lf := range lockfiles {
		data, _ := os.ReadFile(filepath.Join(rootPath, lf))
		h.Write(data)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}
