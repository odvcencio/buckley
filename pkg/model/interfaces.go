package model

import "context"

// CompletionClient can perform a non-streaming chat completion.
type CompletionClient interface {
	ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// StreamingClient can perform a streaming chat completion.
type StreamingClient interface {
	ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error)
}

// ExecutionModelProvider exposes the active execution model identifier.
type ExecutionModelProvider interface {
	GetExecutionModel() string
}

// ContextWindowProvider exposes the input/output token capacity for a model.
type ContextWindowProvider interface {
	GetContextLength(modelID string) (int, error)
}

// ReasoningSupportProvider reports whether a model supports reasoning mode.
type ReasoningSupportProvider interface {
	SupportsReasoning(modelID string) bool
}

// ExecutionClient is used by execution loops that need chat + stream + model selection.
type ExecutionClient interface {
	CompletionClient
	StreamingClient
	ExecutionModelProvider
}

// ContextualCompletionClient is used when completion also needs execution model selection.
type ContextualCompletionClient interface {
	CompletionClient
	ExecutionModelProvider
}

// ReasoningClient is used when completion also needs reasoning capability checks.
type ReasoningClient interface {
	CompletionClient
	ReasoningSupportProvider
}

// ContinuationClient is implemented by providers that can carry native
// working state across turns instead of retransmitting the full transcript.
type ContinuationClient interface {
	// SupportsContinuation reports whether a model can carry provider-native
	// continuation state through the turn engine.
	SupportsContinuation(modelID string) bool
	// ChatCompletionWithContinuation executes a turn using an optional prior
	// continuation and returns the opaque state for the next turn.
	ChatCompletionWithContinuation(ctx context.Context, req ContinuationRequest) (*ContinuationResponse, error)
}
