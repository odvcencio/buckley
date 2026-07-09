package pr

import (
	"bytes"
	"encoding/json"
	"strings"
)

// decodePRResult unmarshals model tool-call arguments into a PRResult.
//
// It first attempts a strict decode. On failure it falls back to a lenient
// decode that coerces the malformations non-Anthropic models (e.g. GLM)
// intermittently emit: a bare string where the schema wants an array of
// strings, a stringified boolean, or array elements that are not strings.
//
// Syntactically invalid JSON — for example a number with an embedded space,
// which json's tokenizer rejects before any field is examined — is not
// recoverable here and is returned as an error so the caller can re-invoke the
// model.
func decodePRResult(raw []byte) (PRResult, error) {
	var strict PRResult
	if err := json.Unmarshal(raw, &strict); err == nil {
		return strict, nil
	}

	var lenient prResultLenient
	if err := json.Unmarshal(raw, &lenient); err != nil {
		return PRResult{}, err
	}
	return PRResult{
		Title:         lenient.Title,
		Summary:       lenient.Summary,
		Changes:       lenient.Changes,
		Testing:       lenient.Testing,
		Breaking:      bool(lenient.Breaking),
		Issues:        lenient.Issues,
		ReviewersHint: lenient.ReviewersHint,
	}, nil
}

// prResultLenient mirrors PRResult but uses tolerant field types so a schema
// drift in one field does not fail the whole decode.
type prResultLenient struct {
	Title         string          `json:"title"`
	Summary       string          `json:"summary"`
	Changes       flexStringSlice `json:"changes"`
	Testing       flexStringSlice `json:"testing"`
	Breaking      flexBool        `json:"breaking"`
	Issues        flexStringSlice `json:"issues"`
	ReviewersHint string          `json:"reviewers_hint"`
}

// flexStringSlice unmarshals from a JSON array of strings, a single JSON string
// (wrapped into a one-element slice), or an array whose elements are not all
// strings (each element stringified).
type flexStringSlice []string

func (f *flexStringSlice) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = nil
		return nil
	}
	if b[0] == '[' {
		var elems []json.RawMessage
		if err := json.Unmarshal(b, &elems); err != nil {
			return err
		}
		out := make([]string, 0, len(elems))
		for _, e := range elems {
			out = append(out, rawToString(e))
		}
		*f = out
		return nil
	}
	*f = []string{rawToString(b)}
	return nil
}

// flexBool unmarshals from a JSON boolean or a stringified boolean
// ("true"/"yes"/"1" → true; anything else → false).
type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	var asBool bool
	if err := json.Unmarshal(b, &asBool); err == nil {
		*f = flexBool(asBool)
		return nil
	}
	var asStr string
	if err := json.Unmarshal(b, &asStr); err == nil {
		switch strings.ToLower(strings.TrimSpace(asStr)) {
		case "true", "yes", "1":
			*f = true
		default:
			*f = false
		}
		return nil
	}
	*f = false
	return nil
}

// rawToString renders a JSON raw value as a string: JSON strings are unquoted,
// any other literal (number, bool, object) uses its trimmed source text.
func rawToString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	return strings.TrimSpace(string(raw))
}
