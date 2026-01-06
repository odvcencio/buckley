package observability

import (
	"context"
	"log/slog"
	"os"
)

// Logger is a structured logger for ACP components
type Logger struct {
	*slog.Logger
}

// NewLogger creates a new structured logger
func NewLogger(component string, level slog.Level) *Logger {
	opts := &slog.HandlerOptions{
		Level: level,
	}

	handler := slog.NewJSONHandler(os.Stdout, opts)

	logger := slog.New(handler).With(
		slog.String("component", component),
		slog.String("system", "acp"),
	)

	return &Logger{Logger: logger}
}

// WithContext returns a logger with context-specific fields.
// When OpenTelemetry is integrated, this will extract trace/span IDs:
//
//	spanCtx := trace.SpanContextFromContext(ctx)
//	if spanCtx.IsValid() {
//	    logger = logger.With(
//	        slog.String("trace_id", spanCtx.TraceID().String()),
//	        slog.String("span_id", spanCtx.SpanID().String()),
//	    )
//	}
func (l *Logger) WithContext(ctx context.Context) *Logger {
	logger := l.Logger

	return &Logger{Logger: logger}
}

// WithAgent returns a logger with agent-specific fields
func (l *Logger) WithAgent(agentID, agentType string) *Logger {
	return &Logger{
		Logger: l.Logger.With(
			slog.String("agent_id", agentID),
			slog.String("agent_type", agentType),
		),
	}
}

// WithSession returns a logger with session-specific fields
func (l *Logger) WithSession(sessionID string) *Logger {
	return &Logger{
		Logger: l.Logger.With(
			slog.String("session_id", sessionID),
		),
	}
}

// WithP2P returns a logger with P2P-specific fields
func (l *Logger) WithP2P(tokenID, requesterID, targetID string) *Logger {
	return &Logger{
		Logger: l.Logger.With(
			slog.String("p2p_token_id", tokenID),
			slog.String("p2p_requester_id", requesterID),
			slog.String("p2p_target_id", targetID),
		),
	}
}

// WithLSP returns a logger with LSP-specific fields
func (l *Logger) WithLSP(method string) *Logger {
	return &Logger{
		Logger: l.Logger.With(
			slog.String("lsp_method", method),
		),
	}
}

// AgentRegistered logs an agent registration event
func (l *Logger) AgentRegistered(agentID, agentType, endpoint string, capabilities []string) {
	l.Info("agent registered",
		slog.String("agent_id", agentID),
		slog.String("agent_type", agentType),
		slog.String("endpoint", endpoint),
		slog.Any("capabilities", capabilities),
	)
}

// AgentUnregistered logs an agent unregistration event
func (l *Logger) AgentUnregistered(agentID, reason string) {
	l.Info("agent unregistered",
		slog.String("agent_id", agentID),
		slog.String("reason", reason),
	)
}

// MessageReceived logs a message reception event
func (l *Logger) MessageReceived(messageID, messageType string, payloadSize int) {
	l.Debug("message received",
		slog.String("message_id", messageID),
		slog.String("message_type", messageType),
		slog.Int("payload_size", payloadSize),
	)
}

// MessageSent logs a message send event
func (l *Logger) MessageSent(messageID, messageType string, payloadSize int) {
	l.Debug("message sent",
		slog.String("message_id", messageID),
		slog.String("message_type", messageType),
		slog.Int("payload_size", payloadSize),
	)
}

// P2PConnectionEstablished logs a P2P connection establishment
func (l *Logger) P2PConnectionEstablished(tokenID, requesterID, targetID string) {
	l.Info("p2p connection established",
		slog.String("token_id", tokenID),
		slog.String("requester_id", requesterID),
		slog.String("target_id", targetID),
	)
}

// P2PConnectionFailed logs a P2P connection failure
func (l *Logger) P2PConnectionFailed(reason string, err error) {
	l.Error("p2p connection failed",
		slog.String("reason", reason),
		slog.String("error", err.Error()),
	)
}

// LSPRequest logs an LSP request
func (l *Logger) LSPRequest(method string, paramsSize int) {
	l.Debug("lsp request",
		slog.String("method", method),
		slog.Int("params_size", paramsSize),
	)
}

// LSPResponse logs an LSP response
func (l *Logger) LSPResponse(method string, resultSize int, duration float64) {
	l.Debug("lsp response",
		slog.String("method", method),
		slog.Int("result_size", resultSize),
		slog.Float64("duration_ms", duration),
	)
}

// EventAppended logs an event append operation
func (l *Logger) EventAppended(streamID, eventType string, eventCount int) {
	l.Debug("events appended",
		slog.String("stream_id", streamID),
		slog.String("event_type", eventType),
		slog.Int("event_count", eventCount),
	)
}

// CircuitBreakerStateChange logs a circuit breaker state change
func (l *Logger) CircuitBreakerStateChange(name, fromState, toState string) {
	l.Warn("circuit breaker state changed",
		slog.String("breaker_name", name),
		slog.String("from_state", fromState),
		slog.String("to_state", toState),
	)
}

// ChangeApproved logs a code change approval
func (l *Logger) ChangeApproved(changeID, reviewer string) {
	l.Info("change approved",
		slog.String("change_id", changeID),
		slog.String("reviewer", reviewer),
	)
}

// ChangeRejected logs a code change rejection
func (l *Logger) ChangeRejected(changeID, reviewer, comment string) {
	l.Info("change rejected",
		slog.String("change_id", changeID),
		slog.String("reviewer", reviewer),
		slog.String("comment", comment),
	)
}
