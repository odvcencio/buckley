package model

import (
	"errors"
	"strings"
)

// IsContextLengthError reports whether a provider rejected a request because
// its effective prompt/context window was exceeded. Providers use inconsistent
// status codes and wording, so both structured fields and conservative text
// signatures are checked.
func IsContextLengthError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if contextErrorText(apiErr.Code) || contextErrorText(apiErr.Type) ||
			contextErrorText(apiErr.Message) || contextErrorText(apiErr.Details) {
			return true
		}
	}
	return contextErrorText(err.Error())
}

func contextErrorText(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return false
	}
	for _, signature := range []string{
		"context_length_exceeded",
		"context length exceeded",
		"maximum context length",
		"max context length",
		"context window exceeded",
		"exceeds the context window",
		"prompt is too long",
		"prompt too long",
		"too many tokens",
		"token limit exceeded",
		"input token limit",
		"request too large for model",
	} {
		if strings.Contains(value, signature) {
			return true
		}
	}
	return false
}
