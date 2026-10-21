package model

import (
	"strings"
	"unicode/utf8"
)

const (
	maxProviderErrorBodyBytes    = 64 << 10
	maxProviderErrorMessageBytes = 4 << 10
	maxProviderErrorDetailsBytes = 4 << 10
	maxProviderErrorLabelBytes   = 256
)

func boundProviderError(err *APIError) *APIError {
	err.Message = boundErrorText(err.Message, maxProviderErrorMessageBytes)
	err.Details = boundErrorText(err.Details, maxProviderErrorDetailsBytes)
	err.Type = boundErrorText(err.Type, maxProviderErrorLabelBytes)
	err.Code = boundErrorText(err.Code, maxProviderErrorLabelBytes)
	err.Provider = boundErrorText(err.Provider, maxProviderErrorLabelBytes)
	err.RequestID = boundErrorText(err.RequestID, maxProviderErrorLabelBytes)
	err.LimitSource = boundErrorText(err.LimitSource, maxProviderErrorLabelBytes)
	return err
}

// Clone even short values so a diagnostic substring cannot retain a large response.
func boundErrorText(text string, limit int) string {
	if len(text) <= limit {
		return strings.Clone(text)
	}
	end := limit - len("...")
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "..."
}
