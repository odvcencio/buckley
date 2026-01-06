package envdetect

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Cache provides disk-based caching for environment profiles
type Cache struct {
	dir string
}

// NewCache creates a new cache instance
func NewCache(dir string) *Cache {
	os.MkdirAll(dir, 0755)
	return &Cache{dir: dir}
}

// Get retrieves a cached environment profile
func (c *Cache) Get(key string) (*EnvironmentProfile, bool) {
	path := filepath.Join(c.dir, "envdetect-"+key+".json")
	data, err := os.ReadFile(path)
	if err != nil {
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
	path := filepath.Join(c.dir, "envdetect-"+key+".json")
	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
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
