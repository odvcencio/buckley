package orchestrator

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const maxIncompletePlanDraftBytes = 32 * 1024

// IncompletePlanError carries a bounded public-only draft from a planning
// response that could not be accepted as a complete plan.
type IncompletePlanError struct {
	draft        string
	finishReason string
	cause        error
}

func NewIncompletePlanError(draft, finishReason string, cause error) *IncompletePlanError {
	return &IncompletePlanError{
		draft:        truncatePublicDraft(strings.TrimSpace(draft), maxIncompletePlanDraftBytes),
		finishReason: strings.TrimSpace(finishReason),
		cause:        cause,
	}
}

func (e *IncompletePlanError) Error() string {
	return "planning response incomplete"
}

func (e *IncompletePlanError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *IncompletePlanError) PublicDraft() string {
	if e == nil {
		return ""
	}
	return e.draft
}

func (e *IncompletePlanError) FinishReason() string {
	if e == nil {
		return ""
	}
	return e.finishReason
}

func IncompletePlanDraft(err error) (string, bool) {
	var incomplete *IncompletePlanError
	if !errors.As(err, &incomplete) {
		return "", false
	}
	draft := incomplete.PublicDraft()
	return draft, strings.TrimSpace(draft) != ""
}

func truncatePublicDraft(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	truncated := s[:maxBytes]
	for !utf8.ValidString(truncated) && len(truncated) > 0 {
		truncated = truncated[:len(truncated)-1]
	}
	return strings.TrimSpace(truncated)
}
