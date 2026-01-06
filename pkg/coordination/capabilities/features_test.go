package capabilities

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFeatureConstants(t *testing.T) {
	// Feature flags should be well-defined strings
	assert.Equal(t, Feature("streaming_tasks"), FeatureStreamingTasks)
	assert.Equal(t, Feature("tool_approval"), FeatureToolApproval)
	assert.Equal(t, Feature("context_sharing"), FeatureContextSharing)
	assert.Equal(t, Feature("p2p_mesh"), FeatureP2PMesh)
	assert.Equal(t, Feature("event_sourcing"), FeatureEventSourcing)
	assert.Equal(t, Feature("pubsub_broadcast"), FeaturePubSubBroadcast)
	assert.Equal(t, Feature("distributed_tracing"), FeatureDistributedTracing)
}

func TestCapabilityConstants(t *testing.T) {
	assert.Equal(t, Capability("read_files"), CapReadFiles)
	assert.Equal(t, Capability("write_files"), CapWriteFiles)
	assert.Equal(t, Capability("execute_tools"), CapExecuteTools)
	assert.Equal(t, Capability("execute_shell"), CapExecuteShell)
	assert.Equal(t, Capability("spawn_agents"), CapSpawnAgents)
	assert.Equal(t, Capability("p2p_mesh"), CapP2PMesh)
	assert.Equal(t, Capability("context_sharing"), CapContextSharing)
	assert.Equal(t, Capability("streaming_tasks"), CapStreamingTasks)
}

func TestHasFeature(t *testing.T) {
	tests := []struct {
		name     string
		features []Feature
		check    Feature
		expected bool
	}{
		{
			name:     "feature exists",
			features: []Feature{FeatureStreamingTasks, FeatureToolApproval},
			check:    FeatureStreamingTasks,
			expected: true,
		},
		{
			name:     "feature does not exist",
			features: []Feature{FeatureStreamingTasks},
			check:    FeatureP2PMesh,
			expected: false,
		},
		{
			name:     "empty features",
			features: []Feature{},
			check:    FeatureStreamingTasks,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasFeature(tt.features, tt.check)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestHasCapability(t *testing.T) {
	tests := []struct {
		name         string
		capabilities []Capability
		check        Capability
		expected     bool
	}{
		{
			name:         "capability exists",
			capabilities: []Capability{CapReadFiles, CapWriteFiles},
			check:        CapReadFiles,
			expected:     true,
		},
		{
			name:         "capability does not exist",
			capabilities: []Capability{CapReadFiles},
			check:        CapExecuteShell,
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasCapability(tt.capabilities, tt.check)
			assert.Equal(t, tt.expected, result)
		})
	}
}
