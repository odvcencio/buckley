package model

import "strings"

// IsTruncatedFinishReason reports provider finish reasons that mean the
// returned message is incomplete rather than a conclusive assistant answer.
func IsTruncatedFinishReason(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_output_tokens":
		return true
	default:
		return false
	}
}
