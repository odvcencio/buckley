package model

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxAffordableOutputRetries = 2
	minAffordableOutputTokens  = 128
)

var affordableOutputPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)can\s+only\s+afford\s+([0-9][0-9_,]*)`),
	regexp.MustCompile(`(?i)can\s+afford\s+only\s+([0-9][0-9_,]*)`),
}

// affordableOutputTokenLimit recognizes OpenRouter's pre-execution credit
// reservation failure. Retrying this response is safe: the provider rejected
// the request before generating output. Other payment failures remain final.
func affordableOutputTokenLimit(err error) (int, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 402 {
		return 0, false
	}
	text := strings.Join([]string{apiErr.Message, apiErr.Details}, " ")
	for _, pattern := range affordableOutputPatterns {
		match := pattern.FindStringSubmatch(text)
		if len(match) != 2 {
			continue
		}
		value := strings.NewReplacer(",", "", "_", "").Replace(match[1])
		limit, parseErr := strconv.Atoi(value)
		if parseErr == nil && limit >= minAffordableOutputTokens {
			return limit, true
		}
	}
	return 0, false
}

// lowerRequestOutputLimit leaves five percent of the provider's quoted
// allowance as headroom for rounding or a concurrently changing balance.
// It changes only the completion allowance; model identity and routing stay
// untouched.
func lowerRequestOutputLimit(req ChatRequest, affordable int) (ChatRequest, bool) {
	if affordable < minAffordableOutputTokens {
		return req, false
	}
	limit := affordable * 95 / 100
	if limit < minAffordableOutputTokens {
		limit = affordable
	}

	current := req.MaxTokens
	if current <= 0 {
		current = req.MaxCompletionTokens
	}
	if current > 0 && limit >= current {
		return req, false
	}

	switch {
	case req.MaxTokens > 0:
		req.MaxTokens = limit
	case req.MaxCompletionTokens > 0:
		req.MaxCompletionTokens = limit
	default:
		req.MaxTokens = limit
	}
	if req.Reasoning != nil && req.Reasoning.MaxTokens > 0 {
		// Preserve at least half of the affordable completion envelope for the
		// visible answer. A huge explicit thinking allowance would otherwise
		// make a successful retry useless to the caller.
		reasoningLimit := max(minAffordableOutputTokens/2, limit/2)
		if req.Reasoning.MaxTokens > reasoningLimit {
			copy := *req.Reasoning
			copy.MaxTokens = reasoningLimit
			req.Reasoning = &copy
		}
	}
	return req, true
}
