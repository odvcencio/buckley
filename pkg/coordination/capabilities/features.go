package capabilities

// Feature flags - server capabilities that clients can query
type Feature string

const (
	FeatureStreamingTasks     Feature = "streaming_tasks"
	FeatureToolApproval       Feature = "tool_approval"
	FeatureContextSharing     Feature = "context_sharing"
	FeatureP2PMesh            Feature = "p2p_mesh"
	FeatureEventSourcing      Feature = "event_sourcing"
	FeaturePubSubBroadcast    Feature = "pubsub_broadcast"
	FeatureDistributedTracing Feature = "distributed_tracing"
)

// AllFeatures returns all available features
func AllFeatures() []Feature {
	return []Feature{
		FeatureStreamingTasks,
		FeatureToolApproval,
		FeatureContextSharing,
		FeatureP2PMesh,
		FeatureEventSourcing,
		FeaturePubSubBroadcast,
		FeatureDistributedTracing,
	}
}

// Capability types - permissions granted to agents
type Capability string

const (
	CapReadFiles      Capability = "read_files"
	CapWriteFiles     Capability = "write_files"
	CapExecuteTools   Capability = "execute_tools"
	CapExecuteShell   Capability = "execute_shell"
	CapSpawnAgents    Capability = "spawn_agents"
	CapP2PMesh        Capability = "p2p_mesh"
	CapContextSharing Capability = "context_sharing"
	CapStreamingTasks Capability = "streaming_tasks"
)

// AllCapabilities returns all available capabilities
func AllCapabilities() []Capability {
	return []Capability{
		CapReadFiles,
		CapWriteFiles,
		CapExecuteTools,
		CapExecuteShell,
		CapSpawnAgents,
		CapP2PMesh,
		CapContextSharing,
		CapStreamingTasks,
	}
}

// HasFeature checks if a feature is in the list
func HasFeature(features []Feature, check Feature) bool {
	for _, f := range features {
		if f == check {
			return true
		}
	}
	return false
}

// HasCapability checks if a capability is in the list
func HasCapability(capabilities []Capability, check Capability) bool {
	for _, c := range capabilities {
		if c == check {
			return true
		}
	}
	return false
}
