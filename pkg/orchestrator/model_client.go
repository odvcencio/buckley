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

// builderRoutedModelClient is an optional Builder-only capability bundle. It
// leaves ModelClient narrow so existing custom clients and generated mocks keep
// their generic dispatch behavior, while the production Manager can bind one
// selected route through request policy and dispatch.
type builderRoutedModelClient interface {
	ResolveModelRoute(string) (model.ModelRoute, error)
	OfferToolsForRoute(model.ModelRoute) bool
	ToolsCatalogConfirmedUnavailableForRoute(model.ModelRoute) bool
	GetContextLengthForRoute(model.ModelRoute) (int, error)
	SupportsReasoningForRoute(model.ModelRoute) bool
	ChatCompletionForRoute(context.Context, model.ChatRequest, model.ModelRoute) (*model.ChatResponse, error)
}
