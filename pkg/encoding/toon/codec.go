package toon

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/alpkeskin/gotoon"
)

// Codec wraps gotoon serialization with JSON fallback.
type Codec struct {
	useToon bool
}

// New creates a codec that prefers TOON for compact serialization.
func New(useToon bool) *Codec {
	return &Codec{useToon: useToon}
}

// Marshal encodes v into TOON (or JSON when disabled).
func (c *Codec) Marshal(v any) ([]byte, error) {
	if !c.useToon || v == nil {
		return json.Marshal(v)
	}
	encoded, err := gotoon.Encode(v)
	if err != nil {
		return nil, fmt.Errorf("toon encode: %w", err)
	}
	return []byte(encoded), nil
}

// Unmarshal decodes JSON payloads back into Go values. TOON is designed for
// one-way transmission to models, so we always fall back to standard JSON
// parsing when we need to recover data.
func (c *Codec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// TOON format patterns:
// - Header: name[count]{field1,field2,...}:
// - Data rows: value1,value2,...
// - Nested: name{field1,field2}:
var (
	// Matches TOON array headers like: results[3]{key,type,summary}:
	toonArrayHeaderPattern = regexp.MustCompile(`\b\w+\[\d+\]\{[^}]+\}:`)
	// Matches TOON object headers like: data{success,error}:
	toonObjectHeaderPattern = regexp.MustCompile(`\b\w+\{[^}]+\}:`)
	// Matches lines that look like TOON data rows (comma-separated, indented)
	toonDataRowPattern = regexp.MustCompile(`^\s+[^,\s][^,]*(?:,[^,]+)+\s*$`)
)

// ContainsTOON checks if text contains TOON-encoded data fragments.
// This is used to detect when model output has leaked TOON tool results.
func ContainsTOON(text string) bool {
	if text == "" {
		return false
	}
	// Check for TOON header patterns
	if toonArrayHeaderPattern.MatchString(text) {
		return true
	}
	if toonObjectHeaderPattern.MatchString(text) {
		return true
	}
	return false
}

// SanitizeOutput removes TOON fragments from model output while preserving
// natural language content. Use this to clean user-facing responses.
func SanitizeOutput(text string) string {
	if text == "" || !ContainsTOON(text) {
		return text
	}

	lines := strings.Split(text, "\n")
	var result []string
	inToonBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Detect start of TOON block
		if toonArrayHeaderPattern.MatchString(trimmed) || toonObjectHeaderPattern.MatchString(trimmed) {
			inToonBlock = true
			continue
		}

		// Skip TOON data rows (indented comma-separated values)
		if inToonBlock {
			if toonDataRowPattern.MatchString(line) {
				continue
			}
			// Empty line or non-TOON content ends the block
			if trimmed == "" || !strings.HasPrefix(line, "  ") {
				inToonBlock = false
			}
		}

		if !inToonBlock {
			result = append(result, line)
		}
	}

	// Clean up multiple consecutive empty lines
	output := strings.Join(result, "\n")
	for strings.Contains(output, "\n\n\n") {
		output = strings.ReplaceAll(output, "\n\n\n", "\n\n")
	}

	return strings.TrimSpace(output)
}

// FormatForDisplay converts TOON-encoded data to human-readable format.
// Unlike SanitizeOutput which removes TOON, this formats it nicely.
func FormatForDisplay(text string) string {
	if text == "" {
		return text
	}

	// If it looks like pure TOON (starts with a header), try to format it
	trimmed := strings.TrimSpace(text)
	if toonArrayHeaderPattern.MatchString(trimmed) || toonObjectHeaderPattern.MatchString(trimmed) {
		return formatToonBlock(trimmed)
	}

	// Mixed content - sanitize TOON fragments
	if ContainsTOON(text) {
		return SanitizeOutput(text)
	}

	return text
}

// formatToonBlock attempts to format a TOON block into readable text.
func formatToonBlock(toon string) string {
	lines := strings.Split(toon, "\n")
	if len(lines) == 0 {
		return toon
	}

	var result []string
	var currentFields []string
	var currentName string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Parse array header: name[count]{fields}:
		if match := toonArrayHeaderPattern.FindString(trimmed); match != "" {
			// Extract name and fields
			parts := strings.SplitN(match, "[", 2)
			if len(parts) == 2 {
				currentName = parts[0]
				// Extract fields from {field1,field2}:
				if idx := strings.Index(parts[1], "{"); idx >= 0 {
					fieldPart := parts[1][idx+1:]
					if endIdx := strings.Index(fieldPart, "}"); endIdx >= 0 {
						currentFields = strings.Split(fieldPart[:endIdx], ",")
					}
				}
			}
			result = append(result, fmt.Sprintf("%s:", currentName))
			continue
		}

		// Parse object header: name{fields}:
		if match := toonObjectHeaderPattern.FindString(trimmed); match != "" {
			parts := strings.SplitN(match, "{", 2)
			if len(parts) == 2 {
				currentName = parts[0]
				fieldPart := parts[1]
				if endIdx := strings.Index(fieldPart, "}"); endIdx >= 0 {
					currentFields = strings.Split(fieldPart[:endIdx], ",")
				}
			}
			result = append(result, fmt.Sprintf("%s:", currentName))
			continue
		}

		// Parse data row
		if len(currentFields) > 0 && strings.Contains(trimmed, ",") {
			values := strings.Split(trimmed, ",")
			var pairs []string
			for i, val := range values {
				if i < len(currentFields) {
					pairs = append(pairs, fmt.Sprintf("%s=%s", currentFields[i], strings.TrimSpace(val)))
				} else {
					pairs = append(pairs, strings.TrimSpace(val))
				}
			}
			result = append(result, "  "+strings.Join(pairs, ", "))
			continue
		}

		// Pass through other lines
		if trimmed != "" {
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n")
}
