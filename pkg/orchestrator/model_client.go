package orchestrator

import (
	"context"

	"m31labs.dev/buckley/pkg/model"
)

// ModelClient defines the subset of model.Manager capabilities the orchestrator needs.
// This indirection lets us mock live API calls in tests via gomock.
type ModelClient interface {
	ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error)
	SupportsReasoning(modelID string) bool
}
