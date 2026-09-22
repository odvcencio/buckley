package tool

import (
	"encoding/json"

	"m31labs.dev/buckley/pkg/toolargs"
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
	return params, nil
}

// NormalizeArgumentsJSON returns the object text used for execution. It is
// shared with provider history so the next request describes the same call.
func NormalizeArgumentsJSON(raw string) ([]byte, error) {
	return toolargs.NormalizeObject(raw)
}
