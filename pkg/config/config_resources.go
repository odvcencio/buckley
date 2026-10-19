package config

import "fmt"

const (
	DefaultStorageBytes = 512 << 20
)

// ResourceConfig bounds accounted storage per database, including replay evidence. Zero explicitly disables a limit.
type ResourceConfig struct {
	StorageBytes int64 `yaml:"storage_bytes"`
}

func defaultResourceConfig() ResourceConfig {
	return ResourceConfig{StorageBytes: DefaultStorageBytes}
}

func validateResources(c *Config) error {
	if c.Resources.StorageBytes < 0 {
		return fmt.Errorf("resource budgets must be nonnegative")
	}
	if c.Resources.StorageBytes > 0 && c.Resources.StorageBytes < 16<<20 {
		return fmt.Errorf("resources.storage_bytes must be at least 16777216")
	}
	return nil
}
