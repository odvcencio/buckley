package config

import "fmt"

const (
	DefaultSessionMemoryBytes = 8 << 20
	// DefaultStorageBytes was sized from a busy store: 486 MB on disk measured
	// 637 MB of accounted use, so 512 MiB blocked receipts within a day.
	DefaultStorageBytes     = 2 << 30
	DefaultUndoStorageBytes = 128 << 20
)

// ResourceConfig bounds retained working history per session and accounted
// storage per database, including replay evidence. Zero explicitly disables a limit.
type ResourceConfig struct {
	SessionMemoryBytes int   `yaml:"session_memory_bytes"`
	StorageBytes       int64 `yaml:"storage_bytes"`
	UndoStorageBytes   int64 `yaml:"undo_storage_bytes"`
}

func defaultResourceConfig() ResourceConfig {
	return ResourceConfig{SessionMemoryBytes: DefaultSessionMemoryBytes, StorageBytes: DefaultStorageBytes, UndoStorageBytes: DefaultUndoStorageBytes}
}

func validateResources(c *Config) error {
	if c.Resources.SessionMemoryBytes < 0 || c.Resources.StorageBytes < 0 || c.Resources.UndoStorageBytes < 0 {
		return fmt.Errorf("resource budgets must be nonnegative")
	}
	if c.Resources.SessionMemoryBytes > 0 && c.Resources.SessionMemoryBytes < 64<<10 {
		return fmt.Errorf("resources.session_memory_bytes must be at least 65536")
	}
	if c.Resources.StorageBytes > 0 && c.Resources.StorageBytes < 16<<20 {
		return fmt.Errorf("resources.storage_bytes must be at least 16777216")
	}
	if c.Resources.UndoStorageBytes > 0 && c.Resources.UndoStorageBytes < 1<<20 {
		return fmt.Errorf("resources.undo_storage_bytes must be at least 1048576")
	}
	return nil
}
