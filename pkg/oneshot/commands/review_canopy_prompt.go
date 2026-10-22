package commands

import (
	"encoding/json"
	"math"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

func compactCanopyPrompt(summary string, codec *toon.Codec) (string, bool) {
	if len(summary) > maxCanopyProjectBytes {
		return summary, false
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(summary), &data); err != nil || len(data) == 0 || !canopyPromptNumbersExact(data) {
		return summary, false
	}
	encoded, err := codec.MarshalString(data)
	if err != nil || len(encoded) == 0 || len(encoded) >= len(summary) || json.Valid([]byte(encoded)) {
		return summary, false
	}
	return encoded, true
}

// The codec normalizes numbers to float64. Keep JSON when a count could lose
// precision during either JSON decoding or TOON encoding.
func canopyPromptNumbersExact(value any) bool {
	switch value := value.(type) {
	case float64:
		return math.Abs(value) < 1<<53
	case map[string]any:
		for _, field := range value {
			if !canopyPromptNumbersExact(field) {
				return false
			}
		}
	case []any:
		for _, item := range value {
			if !canopyPromptNumbersExact(item) {
				return false
			}
		}
	}
	return true
}
