package model

import (
	"context"
	"strings"
)

// ExecutionIdentity carries the trustworthy routing identity for one model
// execution plus any identity fields observed in the provider response.
// RequestedModel, SelectedModel, and ProviderID are authoritative only after
// Manager routing stamps them; providers may populate ResponseModel/ResponseID
// when those values came from the provider response itself.
type ExecutionIdentity struct {
	RequestedModel string `json:"requested_model,omitempty"`
	SelectedModel  string `json:"selected_model,omitempty"`
	ProviderID     string `json:"provider_id,omitempty"`
	ResponseModel  string `json:"response_model,omitempty"`
	ResponseID     string `json:"response_id,omitempty"`
	Conflicted     bool   `json:"conflicted,omitempty"`
}

func cloneExecutionIdentity(identity *ExecutionIdentity) *ExecutionIdentity {
	if identity == nil {
		return nil
	}
	cloned := *identity
	return &cloned
}

func observedExecutionIdentity(responseID, responseModel string, existing *ExecutionIdentity) *ExecutionIdentity {
	out := cloneExecutionIdentity(existing)
	if out == nil {
		out = &ExecutionIdentity{}
	}
	if trimmed := strings.TrimSpace(responseID); trimmed != "" {
		out.ResponseID = trimmed
	} else if strings.TrimSpace(out.ResponseID) != "" {
		out.ResponseID = strings.TrimSpace(out.ResponseID)
	}
	if trimmed := strings.TrimSpace(responseModel); trimmed != "" {
		out.ResponseModel = trimmed
	} else if strings.TrimSpace(out.ResponseModel) != "" {
		out.ResponseModel = strings.TrimSpace(out.ResponseModel)
	}
	if *out == (ExecutionIdentity{}) {
		return nil
	}
	return out
}

func routedExecutionIdentity(requested, selected, providerID string, observed *ExecutionIdentity) *ExecutionIdentity {
	out := &ExecutionIdentity{
		RequestedModel: strings.TrimSpace(requested),
		SelectedModel:  strings.TrimSpace(selected),
		ProviderID:     strings.TrimSpace(providerID),
	}
	if observed != nil {
		out.ResponseModel = strings.TrimSpace(observed.ResponseModel)
		out.ResponseID = strings.TrimSpace(observed.ResponseID)
		out.Conflicted = observed.Conflicted
	}
	return out
}

func stampChatResponseExecutionIdentity(resp *ChatResponse, requested, selected, providerID string) {
	if resp == nil {
		return
	}
	observedID, observedModel := legacyObservedResponseFields(providerID, resp.ID, resp.Model)
	observed := observedExecutionIdentity(observedID, observedModel, resp.ExecutionIdentity)
	resp.ExecutionIdentity = routedExecutionIdentity(requested, selected, providerID, observed)
}

func stampStreamChunkExecutionIdentity(chunk *StreamChunk, requested, selected, providerID string) {
	if chunk == nil {
		return
	}
	observedID, observedModel := legacyObservedResponseFields(providerID, chunk.ID, chunk.Model)
	observed := observedExecutionIdentity(observedID, observedModel, chunk.ExecutionIdentity)
	chunk.ExecutionIdentity = routedExecutionIdentity(requested, selected, providerID, observed)
}

func legacyObservedResponseFields(providerID, responseID, responseModel string) (string, string) {
	switch strings.TrimSpace(providerID) {
	case "anthropic", "codex", "google":
		return "", ""
	default:
		return responseID, responseModel
	}
}

func stampStreamChunks(ctx context.Context, chunks <-chan StreamChunk, errs <-chan error, requested, selected, providerID string) (<-chan StreamChunk, <-chan error) {
	out := make(chan StreamChunk)
	errOut := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errOut)
		sendErr := func(err error) {
			if err == nil {
				return
			}
			select {
			case errOut <- err:
			default:
			}
		}
		forwardChunk := func(chunk StreamChunk) bool {
			stampStreamChunkExecutionIdentity(&chunk, requested, selected, providerID)
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				sendErr(ctx.Err())
				return false
			}
		}
		drainAvailableChunks := func() bool {
			for chunks != nil {
				select {
				case chunk, ok := <-chunks:
					if !ok {
						chunks = nil
						return true
					}
					if !forwardChunk(chunk) {
						return false
					}
				default:
					return true
				}
			}
			return true
		}
		for {
			if chunks == nil && errs == nil {
				return
			}
			select {
			case <-ctx.Done():
				sendErr(ctx.Err())
				return
			case err, ok := <-errs:
				if !ok {
					errs = nil
					continue
				}
				if err == nil {
					errs = nil
					continue
				}
				if !drainAvailableChunks() {
					return
				}
				sendErr(err)
				return
			case chunk, ok := <-chunks:
				if !ok {
					chunks = nil
					continue
				}
				if !forwardChunk(chunk) {
					return
				}
			}
		}
	}()
	return out, errOut
}

func mergeIdentityField(dst *string, update string, identity *ExecutionIdentity) {
	update = strings.TrimSpace(update)
	if update == "" {
		return
	}
	current := strings.TrimSpace(*dst)
	switch {
	case current == "":
		*dst = update
	case current == update:
		*dst = current
	default:
		*dst = ""
		identity.Conflicted = true
	}
}

func mergeExecutionIdentity(dst **ExecutionIdentity, update *ExecutionIdentity) {
	if update == nil {
		return
	}
	if *dst == nil {
		*dst = cloneExecutionIdentity(update)
		if *dst != nil {
			(*dst).RequestedModel = strings.TrimSpace((*dst).RequestedModel)
			(*dst).SelectedModel = strings.TrimSpace((*dst).SelectedModel)
			(*dst).ProviderID = strings.TrimSpace((*dst).ProviderID)
			(*dst).ResponseModel = strings.TrimSpace((*dst).ResponseModel)
			(*dst).ResponseID = strings.TrimSpace((*dst).ResponseID)
		}
		return
	}
	if update.Conflicted {
		(*dst).Conflicted = true
	}
	mergeIdentityField(&(*dst).RequestedModel, update.RequestedModel, *dst)
	mergeIdentityField(&(*dst).SelectedModel, update.SelectedModel, *dst)
	mergeIdentityField(&(*dst).ProviderID, update.ProviderID, *dst)
	mergeIdentityField(&(*dst).ResponseModel, update.ResponseModel, *dst)
	mergeIdentityField(&(*dst).ResponseID, update.ResponseID, *dst)
}
