package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Byte limits shared by every telemetry payload producer. Arguments are
// bounded tighter than results because tool inputs are usually much smaller
// than tool outputs (file contents, search results, etc).
const (
	MaxArgumentBytes = 16 * 1024
	MaxResultBytes   = 64 * 1024
)

// sensitiveKeyFragments lists lower-cased substrings that mark a field name
// as likely to hold a secret.
var sensitiveKeyFragments = []string{
	"password", "passwd", "secret", "token", "api_key", "apikey",
	"authorization", "credential", "cookie", "private_key", "access_key",
}

var (
	// credentialAssignmentRE covers both shell-style assignments and common
	// log/JSON forms. It intentionally preserves the field name and delimiter
	// so a safe diagnostic still tells the operator which credential class was
	// present without exposing the value.
	credentialAssignmentRE = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|access[_-]?key|authorization|credential|cookie|private[_-]?key)\b(\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)
	bearerCredentialRE     = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	urlCredentialRE        = regexp.MustCompile(`(?i)(://)([^/\s:@]+):([^@\s/]+)@`)
)

// SensitiveKey reports whether a field name looks like it holds a secret.
func SensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return false
	}
	for _, fragment := range sensitiveKeyFragments {
		if strings.Contains(key, fragment) {
			return true
		}
	}
	return false
}

// NormalizeText converts malformed UTF-8 to the replacement rune before a
// value reaches a telemetry payload. This is deliberately a small helper so
// every producer can apply the same wire-safe normalization.
func NormalizeText(text string) string {
	return strings.ToValidUTF8(text, "\uFFFD")
}

// RedactCredentials removes credential-looking values from free-form text.
// Key-based redaction remains the primary path for structured values, but
// shell commands and process output are unstructured and often carry tokens
// in headers or assignments instead of JSON fields.
func RedactCredentials(text string) string {
	text = NormalizeText(text)
	text = credentialAssignmentRE.ReplaceAllString(text, `${1}${2}[REDACTED]`)
	text = bearerCredentialRE.ReplaceAllString(text, "Bearer [REDACTED]")
	text = urlCredentialRE.ReplaceAllString(text, `${1}[REDACTED]@`)
	return text
}

// FingerprintText returns a stable short fingerprint for correlation of opaque
// telemetry values. Credentials are redacted before hashing, but the digest is
// deterministic: equal fingerprints expose equality of normalized, redacted
// inputs (subject to collisions) and may enable guessing of low-entropy text.
// It is not an authentication or secrecy primitive; callers must withhold the
// source value independently.
func FingerprintText(text string) string {
	clean := RedactCredentials(text)
	hash := sha256.Sum256([]byte(clean))
	return hex.EncodeToString(hash[:8])
}

// OpaqueTextSummary is a deliberately safe projection for arbitrary command,
// path, and output text. It preserves only a category, normalized byte size,
// and redacted fingerprint so the UI can correlate activity without receiving
// the source text.
func OpaqueTextSummary(text, category string) string {
	clean := RedactCredentials(text)
	category = strings.TrimSpace(category)
	if category == "" {
		category = "text"
	}
	return fmt.Sprintf("%s (%d bytes, sha256:%s)", category, len(clean), FingerprintText(clean))
}

// SanitizeValue walks value recursively, redacting anything held under a
// sensitive-looking key and bounding string values to limit bytes.
//
// SanitizeValue only recurses into map[string]any, []any, and []string.
// Other container shapes (structs, []map[string]any, map[string]string, and
// so on) pass through the default case unexamined. Callers that don't
// already control the payload shape should use NormalizeAndSanitize, which
// round-trips the value through JSON first so every container becomes
// map[string]any/[]any before this function walks it.
func SanitizeValue(value any, key string, limit int) any {
	if SensitiveKey(key) {
		return "[REDACTED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			out[childKey] = SanitizeValue(childValue, childKey, limit)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = SanitizeValue(typed[i], key, limit)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		for i, item := range typed {
			out[i] = BoundText(RedactCredentials(item), limit, "value")
		}
		return out
	case string:
		return BoundText(RedactCredentials(typed), limit, "value")
	default:
		return typed
	}
}

// NormalizeAndSanitize round-trips value through JSON so that every
// container becomes map[string]any or []any, then sanitizes the result.
// This picks up shapes SanitizeValue alone would miss, such as
// []map[string]any, map[string]string, and structs.
//
// If value can't be marshaled/unmarshaled (for example, it contains a
// channel or a function), NormalizeAndSanitize returns a redacted
// placeholder string instead of risking an unredacted payload leaking.
func NormalizeAndSanitize(value any, limit int) any {
	normalized, ok := normalizeForTelemetry(value)
	if !ok {
		return "[REDACTED: payload could not be normalized for telemetry]"
	}
	return SanitizeValue(normalized, "", limit)
}

func normalizeForTelemetry(value any) (any, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, false
	}
	return normalized, true
}

// SanitizeText redacts and bounds free-form text such as captured process
// output. If text is itself a JSON document (as opposed to a Go value
// callers already hold), SanitizeText parses it, redacts values held under
// sensitive-looking keys, and re-encodes the result. Plain text has no
// key/value structure to redact against, so it is only bounded.
func SanitizeText(text string, limit int) string {
	text = strings.TrimSpace(NormalizeText(text))
	if text == "" {
		return text
	}

	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		clean := SanitizeValue(parsed, "", limit)
		if encoded, err := json.Marshal(clean); err == nil {
			return BoundText(RedactCredentials(string(encoded)), limit, "value")
		}
	}

	return BoundText(RedactCredentials(text), limit, "value")
}

// BoundText truncates content to at most limit bytes, backing off cut
// points to UTF-8 rune boundaries so a truncated payload is always valid
// UTF-8, and reports the true number of omitted bytes.
func BoundText(content string, limit int, label string) string {
	content = strings.TrimSpace(NormalizeText(content))
	if limit <= 0 || len(content) <= limit {
		return content
	}

	label = strings.TrimSpace(label)

	// The marker text embeds the omitted-byte count, but the true omitted
	// count depends on where we cut, which in turn depends on the marker's
	// own length. Iterate to a fixed point: derive head/tail from the
	// marker, then rebuild the marker from the resulting true omitted
	// count, until the marker stops changing (typically one extra pass,
	// since only a change in digit count moves the target).
	marker := truncationMarker(label, len(content)-limit)
	var head, tail int
	for i := 0; i < 5; i++ {
		if len(marker) >= limit {
			return marker[:safeCutHeadLen(marker, limit)]
		}
		available := limit - len(marker)
		head = safeCutHeadLen(content, available*2/3)
		tail = safeCutTailLen(content, available-head)
		omitted := len(content) - head - tail
		next := truncationMarker(label, omitted)
		if next == marker {
			break
		}
		marker = next
	}

	return content[:head] + marker + content[len(content)-tail:]
}

func truncationMarker(label string, omitted int) string {
	if omitted < 0 {
		omitted = 0
	}
	return fmt.Sprintf("\n... %s truncated (%d bytes omitted) ...\n", label, omitted)
}

// safeCutHeadLen returns the largest length <= n such that s[:length] ends
// on a rune boundary.
func safeCutHeadLen(s string, n int) int {
	if n <= 0 {
		return 0
	}
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

// safeCutTailLen returns the largest length <= n such that s[len(s)-length:]
// starts on a rune boundary.
func safeCutTailLen(s string, n int) int {
	if n <= 0 {
		return 0
	}
	if n >= len(s) {
		return len(s)
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return len(s) - start
}

// toolPayloadFields lists the Event.Data keys that carry full tool call
// arguments/results rather than summary metadata.
var toolPayloadFields = []string{"arguments", "result"}

// StripToolPayloads returns a copy of event with the full tool
// arguments/result payload fields removed from Data. Key-name redaction
// alone can't protect file contents or other unstructured tool output, so
// network transports (IPC WebSocket, ACP) should call this before
// forwarding events unless the operator has explicitly opted in to full
// payloads leaving the process.
func StripToolPayloads(event Event) Event {
	if len(event.Data) == 0 {
		return event
	}
	hasPayload := false
	for _, field := range toolPayloadFields {
		if _, ok := event.Data[field]; ok {
			hasPayload = true
			break
		}
	}
	if !hasPayload {
		return event
	}
	data := make(map[string]any, len(event.Data))
	for key, value := range event.Data {
		skip := false
		for _, field := range toolPayloadFields {
			if key == field {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		data[key] = value
	}
	event.Data = data
	return event
}
