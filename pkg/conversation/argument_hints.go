package conversation

import (
	"encoding/json"
	"unicode/utf8"
)

// Argument summaries retain only the first 200 decoded bytes of string hints.
// Keep JSON's normal map-key, duplicate-key and number validation behavior.
type argumentHint struct{ value any }

func (v *argumentHint) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || data[0] != '"' {
		return json.Unmarshal(data, &v.value)
	}
	// Six source bytes can encode one decoded byte (\u0000). One extra
	// surrogate pair puts any truncated escape beyond the retained prefix.
	const prefixBytes = 6*200 + 12
	end := 1
	for end < len(data)-1 && end < prefixBytes {
		if data[end] == '\\' {
			if data[end+1] == 'u' {
				end += 6
			} else {
				end += 2
			}
		} else {
			_, width := utf8.DecodeRune(data[end:])
			end += width
		}
	}
	if end < len(data)-1 {
		prefix := make([]byte, end+1)
		copy(prefix, data[:end])
		prefix[end] = '"'
		data = prefix
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	v.value = utf8Prefix(text, 200)
	return nil
}
