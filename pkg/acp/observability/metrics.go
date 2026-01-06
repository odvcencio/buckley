package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Agent metrics
	AgentRegistrations = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "agent",
			Name:      "registrations_total",
			Help:      "Total number of agent registrations",
		},
		[]string{"agent_type"},
	)

	AgentUnregistrations = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "agent",
			Name:      "unregistrations_total",
			Help:      "Total number of agent unregistrations",
		},
		[]string{"agent_type", "reason"},
	)

	ActiveAgents = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "acp",
			Subsystem: "agent",
			Name:      "active_total",
			Help:      "Number of currently active agents",
		},
		[]string{"agent_type"},
	)

	// Message metrics
	MessagesReceived = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "message",
			Name:      "received_total",
			Help:      "Total number of messages received",
		},
		[]string{"message_type"},
	)

	MessagesSent = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "message",
			Name:      "sent_total",
			Help:      "Total number of messages sent",
		},
		[]string{"message_type"},
	)

	MessageLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "acp",
			Subsystem: "message",
			Name:      "latency_seconds",
			Help:      "Message processing latency in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.001, 2, 10), // 1ms to ~1s
		},
		[]string{"message_type"},
	)

	// P2P metrics
	P2PConnectionsEstablished = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "p2p",
			Name:      "connections_established_total",
			Help:      "Total number of P2P connections established",
		},
		[]string{"requester_type", "target_type"},
	)

	P2PConnectionsFailed = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "p2p",
			Name:      "connections_failed_total",
			Help:      "Total number of P2P connection failures",
		},
		[]string{"reason"},
	)

	ActiveP2PConnections = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "acp",
			Subsystem: "p2p",
			Name:      "connections_active",
			Help:      "Number of currently active P2P connections",
		},
	)

	P2PTokensGenerated = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "p2p",
			Name:      "tokens_generated_total",
			Help:      "Total number of P2P tokens generated",
		},
	)

	P2PTokensValidated = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "p2p",
			Name:      "tokens_validated_total",
			Help:      "Total number of P2P token validations",
		},
		[]string{"result"}, // "success" or "failure"
	)

	// LSP Bridge metrics
	LSPRequests = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "lsp",
			Name:      "requests_total",
			Help:      "Total number of LSP requests",
		},
		[]string{"method"},
	)

	LSPRequestLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "acp",
			Subsystem: "lsp",
			Name:      "request_latency_seconds",
			Help:      "LSP request latency in seconds",
			Buckets:   prometheus.ExponentialBuckets(0.01, 2, 10), // 10ms to ~10s
		},
		[]string{"method"},
	)

	LSPStreamChunks = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "lsp",
			Name:      "stream_chunks_total",
			Help:      "Total number of stream chunks sent",
		},
		[]string{"stream_id"},
	)

	ActiveLSPStreams = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "acp",
			Subsystem: "lsp",
			Name:      "streams_active",
			Help:      "Number of currently active LSP streams",
		},
	)

	// Event Store metrics
	EventsAppended = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "events",
			Name:      "appended_total",
			Help:      "Total number of events appended",
		},
		[]string{"stream_id", "event_type"},
	)

	EventsRead = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "events",
			Name:      "read_total",
			Help:      "Total number of events read",
		},
		[]string{"stream_id"},
	)

	SnapshotsCreated = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "events",
			Name:      "snapshots_created_total",
			Help:      "Total number of snapshots created",
		},
		[]string{"stream_id"},
	)

	// Circuit Breaker metrics
	CircuitBreakerStateChanges = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "circuit_breaker",
			Name:      "state_changes_total",
			Help:      "Total number of circuit breaker state changes",
		},
		[]string{"name", "from_state", "to_state"},
	)

	CircuitBreakerExecutions = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "circuit_breaker",
			Name:      "executions_total",
			Help:      "Total number of circuit breaker executions",
		},
		[]string{"name", "result"}, // "success", "failure", "rejected"
	)

	// Mission Control metrics
	PendingChangesTotal = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "acp",
			Subsystem: "mission",
			Name:      "pending_changes_total",
			Help:      "Number of pending code changes",
		},
		[]string{"status"}, // "pending", "approved", "rejected"
	)

	ChangeReviews = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "mission",
			Name:      "change_reviews_total",
			Help:      "Total number of change reviews",
		},
		[]string{"action", "reviewer"}, // action: "approve" or "reject"
	)

	AgentActivitiesRecorded = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "mission",
			Name:      "activities_recorded_total",
			Help:      "Total number of agent activities recorded",
		},
		[]string{"agent_type", "status"},
	)

	// Event Stream metrics
	ActiveEventStreamConnections = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "acp",
			Subsystem: "event_stream",
			Name:      "connections_active",
			Help:      "Number of currently active event stream WebSocket connections",
		},
	)

	EventStreamEventsBroadcast = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "event_stream",
			Name:      "events_broadcast_total",
			Help:      "Total number of events broadcast to subscribers",
		},
	)

	EventStreamMessagesSent = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "event_stream",
			Name:      "messages_sent_total",
			Help:      "Total number of messages sent to WebSocket clients",
		},
	)

	EventStreamBackpressureDrops = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "acp",
			Subsystem: "event_stream",
			Name:      "backpressure_drops_total",
			Help:      "Total number of events dropped due to backpressure",
		},
	)
)
