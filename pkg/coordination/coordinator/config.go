package coordinator

import (
	"github.com/odvcencio/buckley/pkg/coordination/capabilities"
)

// Config holds coordinator configuration
type Config struct {
	// Server address
	Address string

	// Protocol version
	ProtocolVersion string

	// Enabled features
	Features []capabilities.Feature

	// Maximum number of agents
	MaxAgents int

	// Supported authentication methods
	SupportedAuth []string
}

// DefaultConfig returns default coordinator configuration
func DefaultConfig() *Config {
	return &Config{
		Address:         "localhost:50052",
		ProtocolVersion: "1.0.0",
		Features: []capabilities.Feature{
			capabilities.FeatureStreamingTasks,
			capabilities.FeatureToolApproval,
			capabilities.FeatureContextSharing,
		},
		MaxAgents:     100,
		SupportedAuth: []string{"bearer", "session"},
	}
}
