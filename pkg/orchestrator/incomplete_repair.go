package orchestrator

import (
	"strings"

	"m31labs.dev/buckley/pkg/model"
)

const maxIncompleteRepairDraftBytes = 32 * 1024

// IncompleteRepairError carries a bounded public-only draft from a repair
// response that cannot be safely applied to the filesystem.
type IncompleteRepairError struct {
	draft        string
	finishReason string
	cause        error
}

func NewIncompleteRepairError(draft, finishReason string, cause error) *IncompleteRepairError {
	return &IncompleteRepairError{
		draft:        truncatePublicDraft(strings.TrimSpace(draft), maxIncompleteRepairDraftBytes),
		finishReason: strings.TrimSpace(finishReason),
		cause:        cause,
	}
}

func (e *IncompleteRepairError) Error() string {
	return "repair response incomplete"
}

func (e *IncompleteRepairError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *IncompleteRepairError) PublicDraft() string {
	if e == nil {
		return ""
	}
	return e.draft
}

func (e *IncompleteRepairError) FinishReason() string {
	if e == nil {
		return ""
	}
	return e.finishReason
}

func firstRepairFinishReason(resp *model.ChatResponse) string {
	if resp == nil || len(resp.Choices) == 0 {
		return ""
	}
	return strings.TrimSpace(resp.Choices[0].FinishReason)
}

func repairFinishReasonIsStop(reason string) bool {
	return strings.EqualFold(strings.TrimSpace(reason), "stop")
}

func publicRepairDraftFromResponse(resp *model.ChatResponse) string {
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
