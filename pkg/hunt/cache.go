package hunt

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os/exec"

	"m31labs.dev/buckley/internal/filecache"
)

const (
	DefaultCacheMaxBytes      = 8 << 20
	DefaultCacheMaxEntries    = 256
	DefaultCacheMaxEntryBytes = 256 << 10
)

// Cache provides disk-based caching for hunt results
type Cache struct {
	files *filecache.Cache
}

// NewCache creates a new cache instance
func NewCache(dir string) *Cache {
	return &Cache{files: filecache.New(dir, "hunt-", filecache.Limits{Bytes: DefaultCacheMaxBytes, Entries: DefaultCacheMaxEntries, EntryBytes: DefaultCacheMaxEntryBytes})}
}

// Get retrieves cached suggestions for a commit SHA
func (c *Cache) Get(commitSHA string) ([]ImprovementSuggestion, bool) {
	data, ok := c.files.Get(commitSHA)
	if !ok {
		return nil, false
	}

	var suggestions []ImprovementSuggestion
	if err := json.Unmarshal(data, &suggestions); err != nil {
		return nil, false
	}

	return suggestions, true
}

// Set stores suggestions for a commit SHA
func (c *Cache) Set(commitSHA string, suggestions []ImprovementSuggestion) error {
	data, err := json.Marshal(suggestions)
	if err != nil {
		return err
	}

	return c.files.Set(commitSHA, data)
}

// getCurrentCommit gets the current git commit SHA
func getCurrentCommit(rootPath string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = rootPath
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%x", sha256.Sum256(output))[:16], nil
}
