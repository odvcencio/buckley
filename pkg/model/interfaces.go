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
