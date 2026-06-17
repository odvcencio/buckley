package model

import (
	"errors"
	"fmt"
	"strings"
)

// NoResponseChoicesError describes an invalid chat response without exposing
// prompt content. Providers occasionally return an HTTP success with no choices;
// callers need request shape, not raw messages, to debug that path.
func NoResponseChoicesError(req ChatRequest, resp *ChatResponse) error {
	return errors.New(NoResponseChoicesMessage(req, resp))
}

func NoResponseChoicesMessage(req ChatRequest, resp *ChatResponse) string {
	modelID := strings.TrimSpace(req.Model)
	if modelID == "" && resp != nil {
		modelID = strings.TrimSpace(resp.Model)
	}
	if modelID == "" {
		modelID = "unknown"
	}

	parts := []string{fmt.Sprintf("model %s returned no response choices", modelID)}
	if resp != nil {
		if id := strings.TrimSpace(resp.ID); id != "" {
			parts = append(parts, "response_id="+id)
		}
		if responseModel := strings.TrimSpace(resp.Model); responseModel != "" && responseModel != modelID {
			parts = append(parts, "response_model="+responseModel)
		}
	}
	parts = append(parts, fmt.Sprintf("messages=%d", len(req.Messages)))
	if len(req.Tools) > 0 {
		parts = append(parts, fmt.Sprintf("tools=%d", len(req.Tools)))
	}
	if choice := strings.TrimSpace(req.ToolChoice); choice != "" {
		parts = append(parts, "tool_choice="+choice)
	}
	if req.Reasoning != nil {
		if effort := strings.TrimSpace(req.Reasoning.Effort); effort != "" {
			parts = append(parts, "reasoning="+effort)
		} else if req.Reasoning.Enabled != nil {
			parts = append(parts, fmt.Sprintf("reasoning_enabled=%t", *req.Reasoning.Enabled))
		}
	}
	if req.MaxTokens > 0 {
		parts = append(parts, fmt.Sprintf("max_tokens=%d", req.MaxTokens))
	}
	if req.MaxCompletionTokens > 0 {
		parts = append(parts, fmt.Sprintf("max_completion_tokens=%d", req.MaxCompletionTokens))
	}
	if sessionID := strings.TrimSpace(req.SessionID); sessionID != "" {
		parts = append(parts, "session="+sessionID)
	}
	return strings.Join(parts, " ")
}
