package tool

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DecodeArguments decodes a model tool call's JSON object. It tolerates two
// unambiguous presentation mistakes made by otherwise capable models: wrapping
// the complete object in a Markdown JSON fence, and encoding the complete
// object as a JSON string. It does not repair malformed JSON or extract an
// object from surrounding prose.
func DecodeArguments(raw string) (map[string]any, error) {
	normalized, err := NormalizeArgumentsJSON(raw)
	if err != nil {
		return nil, err
	}

	var params map[string]any
	if err := json.Unmarshal(normalized, &params); err != nil {
		return nil, err
	}
	if params == nil {
		params = make(map[string]any)
	}
	return params, nil
}

// NormalizeArgumentsJSON returns the complete JSON object represented by raw.
// The returned bytes retain the model's object text so callers can perform
// stricter validation, such as duplicate-key detection, before decoding it.
func NormalizeArgumentsJSON(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return []byte("{}"), nil
	}

	var err error
	if strings.HasPrefix(trimmed, "```") {
		trimmed, err = unwrapArgumentsFence(trimmed)
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
	return []byte(trimmed), nil
}

func unwrapArgumentsFence(raw string) (string, error) {
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
