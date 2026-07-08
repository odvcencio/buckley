// Package jsonrepair provides best-effort repair of near-valid JSON emitted
// by reasoning models (observed from GLM-5.x and Qwen agentic checkpoints
// routed through OpenRouter/vLLM tool-call parsers).
//
// The motivating bug: GLM-5.2's tool-call argument JSON occasionally arrives
// with a stray whitespace character injected inside a numeric literal (e.g.
// "0.92" rendered as "0. 92", or "1234" rendered as "1 234"), which fails
// encoding/json with `invalid character ' ' in numeric literal`. This
// package repairs that quirk (and the similarly common trailing-comma
// mistake) without touching anything inside quoted JSON string values, so it
// is safe to apply speculatively to any tool-call argument payload before
// unmarshaling.
package jsonrepair

import "encoding/json"

// Repair returns a best-effort fixed copy of data with common LLM JSON
// malformations corrected:
//
//  1. Numeric literals broken by stray whitespace, e.g. "0. 92" or "1 234"
//     instead of "0.92" / "1234". This is the exact quirk behind the
//     observed `invalid character ' ' in numeric literal` unmarshal error.
//  2. Trailing commas before a closing '}' or ']', e.g. {"a": 1,}.
//
// Both repairs are applied with a single-pass, string-literal-aware scanner:
// whitespace and commas inside quoted JSON string values are never touched,
// only bytes that sit outside any string literal. If data is already valid
// JSON, Repair returns it unchanged. Repair does not guarantee the result is
// valid JSON -- callers should still validate/unmarshal the result and fall
// back to the original error if repair didn't help.
func Repair(data []byte) []byte {
	if len(data) == 0 || json.Valid(data) {
		return data
	}

	out := make([]byte, 0, len(data))
	inString := false
	escaped := false
	var lastSig byte // last significant (non-whitespace, non-comma-dropped) byte emitted

	for i := 0; i < len(data); i++ {
		b := data[i]

		if inString {
			out = append(out, b)
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
				lastSig = b
			}
			continue
		}

		switch {
		case b == '"':
			inString = true
			out = append(out, b)
		case isJSONSpace(b):
			// Find the next non-whitespace byte without consuming it yet.
			j := i
			for j < len(data) && isJSONSpace(data[j]) {
				j++
			}
			var next byte
			if j < len(data) {
				next = data[j]
			}
			if isNumberChar(lastSig) && isNumberChar(next) {
				// Spurious whitespace inside a numeric literal (e.g.
				// "0. 92" -> "0.92"): drop the entire whitespace run.
				i = j - 1
				continue
			}
			out = append(out, b)
		case b == ',':
			// Trailing comma before a closing brace/bracket: look ahead
			// (skipping whitespace) and drop the comma if so.
			j := i + 1
			for j < len(data) && isJSONSpace(data[j]) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
			out = append(out, b)
			lastSig = b
		default:
			out = append(out, b)
			lastSig = b
		}
	}

	return out
}

// isJSONSpace reports whether b is JSON insignificant whitespace (the exact
// set defined by the JSON grammar: space, tab, newline, carriage return).
func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// isNumberChar reports whether b can legally appear inside a JSON numeric
// literal (digits, decimal point, sign, exponent marker).
func isNumberChar(b byte) bool {
	return (b >= '0' && b <= '9') || b == '.' || b == '-' || b == '+' || b == 'e' || b == 'E'
}

// Valid reports whether data is valid JSON, either as-is or after Repair.
func Valid(data []byte) bool {
	if json.Valid(data) {
		return true
	}
	return json.Valid(Repair(data))
}

// TryUnmarshal unmarshals data into v, retrying once against Repair(data) if
// the first attempt fails. The error from the *original* (unrepaired)
// attempt is returned on failure, since that's the error a caller/log should
// surface (repair either fixed it silently, or the original error remains
// the accurate diagnostic).
func TryUnmarshal(data []byte, v any) error {
	firstErr := json.Unmarshal(data, v)
	if firstErr == nil {
		return nil
	}

	repaired := Repair(data)
	if string(repaired) == string(data) {
		return firstErr
	}
	if err := json.Unmarshal(repaired, v); err == nil {
		return nil
	}
	return firstErr
}

// FixArguments returns raw (a tool-call arguments JSON string) unchanged if
// it is already valid JSON. Otherwise it attempts Repair and returns the
// repaired string if that produces valid JSON; if repair doesn't help, raw
// is returned unchanged so the original parse error remains visible to
// whichever caller unmarshals it next.
func FixArguments(raw string) string {
	if raw == "" {
		return raw
	}
	data := []byte(raw)
	if json.Valid(data) {
		return raw
	}
	repaired := Repair(data)
	if json.Valid(repaired) {
		return string(repaired)
	}
	return raw
}
