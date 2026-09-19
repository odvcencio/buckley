package orchestrator

import (
	"strings"

	"m31labs.dev/buckley/pkg/model"
)

const maxIncompleteReviewDraftBytes = 32 * 1024

// IncompleteReviewError carries a bounded public-only draft from a review
// response that could not be accepted as complete review approval.
type IncompleteReviewError struct {
	draft        string
	finishReason string
	cause        error
}

func NewIncompleteReviewError(draft, finishReason string, cause error) *IncompleteReviewError {
	return &IncompleteReviewError{
		draft:        truncatePublicDraft(strings.TrimSpace(draft), maxIncompleteReviewDraftBytes),
		finishReason: strings.TrimSpace(finishReason),
		cause:        cause,
	}
}

func (e *IncompleteReviewError) Error() string {
	return "review response incomplete"
}

func (e *IncompleteReviewError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *IncompleteReviewError) PublicDraft() string {
	if e == nil {
		return ""
	}
	return e.draft
}

func (e *IncompleteReviewError) FinishReason() string {
	if e == nil {
		return ""
	}
	return e.finishReason
}

func firstReviewFinishReason(resp *model.ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(resp.Choices[0].FinishReason)
}

func reviewFinishReasonIsStop(reason string) bool {
	return strings.EqualFold(strings.TrimSpace(reason), "stop")
}

func publicReviewDraftFromResponse(resp *model.ChatResponse) string {
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
