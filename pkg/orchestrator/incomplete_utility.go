package orchestrator

import (
	"errors"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/model"
)

const maxIncompleteUtilityDraftBytes = 32 * 1024

// IncompleteUtilityResponseError carries a bounded public-only draft from a
// utility model response that must not be used for external side effects.
type IncompleteUtilityResponseError struct {
	operation    string
	draft        string
	finishReason string
	cause        error
}

func NewIncompleteUtilityResponseError(operation, draft, finishReason string, cause error) *IncompleteUtilityResponseError {
	operation = strings.TrimSpace(operation)
	if operation == "" {
		operation = "utility model"
	}
	return &IncompleteUtilityResponseError{
		operation:    operation,
		draft:        truncatePublicDraft(strings.TrimSpace(draft), maxIncompleteUtilityDraftBytes),
		finishReason: strings.TrimSpace(finishReason),
		cause:        cause,
	}
}

func (e *IncompleteUtilityResponseError) Error() string {
	if e == nil || e.operation == "" {
		return "utility response incomplete"
	}
	return fmt.Sprintf("%s response incomplete", e.operation)
}

func (e *IncompleteUtilityResponseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *IncompleteUtilityResponseError) PublicDraft() string {
	if e == nil {
		return ""
	}
	return e.draft
}

func (e *IncompleteUtilityResponseError) FinishReason() string {
	if e == nil {
		return ""
	}
	return e.finishReason
}

func IncompleteUtilityDraft(err error) (string, bool) {
	var incomplete *IncompleteUtilityResponseError
	if !errors.As(err, &incomplete) {
		return "", false
	}
	draft := incomplete.PublicDraft()
	return draft, strings.TrimSpace(draft) != ""
}

func firstUtilityFinishReason(resp *model.ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(resp.Choices[0].FinishReason)
}

func utilityFinishReasonIsStop(reason string) bool {
	return strings.EqualFold(strings.TrimSpace(reason), "stop")
}

func publicUtilityDraftFromResponse(resp *model.ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	content, err := model.ExtractTextContent(resp.Choices[0].Message.Content)
	if err != nil {
		return ""
	}
	_, public := model.ExtractThinkingContent(content)
	return strings.TrimSpace(public)
}
