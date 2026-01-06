package hunt

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Cache provides disk-based caching for hunt results
type Cache struct {
	dir string
}

// NewCache creates a new cache instance
func NewCache(dir string) *Cache {
	_ = os.MkdirAll(dir, 0755) // Best-effort directory creation
	return &Cache{dir: dir}
}

// Get retrieves cached suggestions for a commit SHA
func (c *Cache) Get(commitSHA string) ([]ImprovementSuggestion, bool) {
	path := filepath.Join(c.dir, "hunt-"+commitSHA+".json")
	data, err := os.ReadFile(path)
	if err != nil {
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
	path := filepath.Join(c.dir, "hunt-"+commitSHA+".json")
	data, err := json.Marshal(suggestions)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
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
