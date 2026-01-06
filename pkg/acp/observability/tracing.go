package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerName = "github.com/odvcencio/buckley/pkg/acp"
)

// TracerProvider holds the OpenTelemetry tracer provider
type TracerProvider struct {
	provider *sdktrace.TracerProvider
}

// NewTracerProvider creates a new OpenTelemetry tracer provider
func NewTracerProvider(serviceName string) (*TracerProvider, error) {
	// Create stdout exporter for development
	exporter, err := stdouttrace.New(
		stdouttrace.WithPrettyPrint(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create trace exporter: %w", err)
	}

	// Create resource with service information
	res, err := resource.New(
		context.Background(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceVersionKey.String("1.0.0"),
			attribute.String("environment", "development"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// Create tracer provider
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	// Set global tracer provider
	otel.SetTracerProvider(provider)

	return &TracerProvider{
		provider: provider,
	}, nil
}

// Shutdown gracefully shuts down the tracer provider
func (tp *TracerProvider) Shutdown(ctx context.Context) error {
	return tp.provider.Shutdown(ctx)
}

// Tracer returns a tracer for the ACP package
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// StartSpan starts a new span with the given name
func StartSpan(ctx context.Context, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, spanName, opts...)
}

// AddEvent adds an event to the current span
func AddEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	span.AddEvent(name, trace.WithAttributes(attrs...))
}

// RecordError records an error on the current span
func RecordError(ctx context.Context, err error) {
	span := trace.SpanFromContext(ctx)
	span.RecordError(err)
}

// SetAttributes sets attributes on the current span
func SetAttributes(ctx context.Context, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attrs...)
}

// Common attribute keys for ACP tracing
var (
	// Agent attributes
	AttrAgentID       = attribute.Key("acp.agent.id")
	AttrAgentType     = attribute.Key("acp.agent.type")
	AttrAgentEndpoint = attribute.Key("acp.agent.endpoint")

	// Message attributes
	AttrMessageID      = attribute.Key("acp.message.id")
	AttrMessageType    = attribute.Key("acp.message.type")
	AttrMessagePayload = attribute.Key("acp.message.payload_size")

	// Session attributes
	AttrSessionID = attribute.Key("acp.session.id")

	// P2P attributes
	AttrP2PTokenID = attribute.Key("acp.p2p.token_id")
	AttrP2PTarget  = attribute.Key("acp.p2p.target_id")

	// Tool attributes
	AttrToolName   = attribute.Key("acp.tool.name")
	AttrToolResult = attribute.Key("acp.tool.result")

	// LSP attributes
	AttrLSPMethod = attribute.Key("acp.lsp.method")
	AttrLSPParams = attribute.Key("acp.lsp.params_size")
)
