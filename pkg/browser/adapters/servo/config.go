package servo

import (
	"errors"
	"strings"
	"time"
)

// Config controls how the Servo browser adapter launches browserd.
type Config struct {
	BrowserdPath   string
	SocketDir      string
	ConnectTimeout time.Duration
	FrameRate      int
}

// DefaultConfig returns the default adapter configuration.
func DefaultConfig() Config {
	return Config{
		BrowserdPath:   "browserd",
		ConnectTimeout: 5 * time.Second,
		FrameRate:      12,
	}
}

func (c Config) withDefaults() Config {
	defaults := DefaultConfig()
	if strings.TrimSpace(c.BrowserdPath) != "" {
		defaults.BrowserdPath = c.BrowserdPath
	}
	if strings.TrimSpace(c.SocketDir) != "" {
		defaults.SocketDir = c.SocketDir
	}
	if c.ConnectTimeout != 0 {
		defaults.ConnectTimeout = c.ConnectTimeout
	}
	if c.FrameRate != 0 {
		defaults.FrameRate = c.FrameRate
	}
	return defaults
}

// Validate checks whether the config is usable.
func (c Config) Validate() error {
	if strings.TrimSpace(c.BrowserdPath) == "" {
		return errors.New("browserd_path is required")
	}
	if c.FrameRate <= 0 {
		return errors.New("frame_rate must be greater than zero")
	}
	if c.ConnectTimeout < 0 {
		return errors.New("connect_timeout must be zero or positive")
	}
	return nil
}
