package toolargs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrDuplicateObjectKey = errors.New("duplicate object key")

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
	if err := rejectDuplicateKeys([]byte(trimmed)); err != nil {
		return nil, err
	}
	return []byte(trimmed), nil
}

func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return checkDuplicateKeys(dec)
}

func checkDuplicateKeys(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyTok.(string)
			if !ok {
				return fmt.Errorf("unexpected object key token %v", keyTok)
			}
			if _, dup := seen[key]; dup {
				return fmt.Errorf("%w %q", ErrDuplicateObjectKey, key)
			}
			seen[key] = struct{}{}
			if err := checkDuplicateKeys(dec); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
	case '[':
		for dec.More() {
			if err := checkDuplicateKeys(dec); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
	}
	return nil
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
