package config

import "fmt"

const (
	DefaultSessionMemoryBytes = 8 << 20
	DefaultStorageBytes       = 512 << 20
)

// ResourceConfig bounds retained working history per session and accounted
// storage per database, including replay evidence. Zero explicitly disables a limit.
type ResourceConfig struct {
	SessionMemoryBytes int   `yaml:"session_memory_bytes"`
	StorageBytes       int64 `yaml:"storage_bytes"`
}

func defaultResourceConfig() ResourceConfig {
	return ResourceConfig{SessionMemoryBytes: DefaultSessionMemoryBytes, StorageBytes: DefaultStorageBytes}
}

func validateResources(c *Config) error {
	if c.Resources.SessionMemoryBytes < 0 || c.Resources.StorageBytes < 0 {
		return fmt.Errorf("resource budgets must be nonnegative")
	}
	if c.Resources.SessionMemoryBytes > 0 && c.Resources.SessionMemoryBytes < 64<<10 {
		return fmt.Errorf("resources.session_memory_bytes must be at least 65536")
	}
	if c.Resources.StorageBytes > 0 && c.Resources.StorageBytes < 16<<20 {
		return fmt.Errorf("resources.storage_bytes must be at least 16777216")
	}
	return nil
}
