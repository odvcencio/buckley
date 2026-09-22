package toolargs

import (
	"encoding/json"
	"fmt"
	"strings"
)

// NormalizeObject removes only complete presentation wrappers around tool
// arguments. The returned bytes are a valid JSON object; missing and null
// arguments become an empty object. It never extracts JSON from prose or
// repairs malformed JSON.
func NormalizeObject(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []byte("{}"), nil
	}

	var err error
	if strings.HasPrefix(trimmed, "```") {
		trimmed, err = unwrapFence(trimmed)
		if err != nil {
			return nil, err
		}
	}

	if strings.HasPrefix(trimmed, `"`) {
		var unquoted string
		if err := json.Unmarshal([]byte(trimmed), &unquoted); err != nil {
			return nil, fmt.Errorf("decode JSON-encoded tool arguments: %w", err)
		}
		trimmed = strings.TrimSpace(unquoted)
	}

	var params map[string]any
	if err := json.Unmarshal([]byte(trimmed), &params); err != nil {
		return nil, err
	}
	if params == nil {
		return []byte("{}"), nil
	}
	return []byte(trimmed), nil
}

func unwrapFence(raw string) (string, error) {
	lines := strings.Split(raw, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return "", fmt.Errorf("invalid fenced tool arguments")
	}
	header := strings.TrimSpace(lines[0])
	if header != "```" && !strings.EqualFold(header, "```json") {
		return "", fmt.Errorf("unsupported tool argument fence %q", header)
	}
	body := strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
	if body == "" {
		return "", fmt.Errorf("empty fenced tool arguments")
	}
	return body, nil
}
