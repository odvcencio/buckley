package builtin

import (
	"fmt"
	"strings"

	artifactv1 "m31labs.dev/buckley/pkg/artifact/v1"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/telemetry"
)

const (
	maxRetainedToolFailures = 8
	maxToolFailureMessage   = 1024
	maxToolNameRawBytes     = 256
	maxToolErrorRawBytes    = 64 * 1024
)

// RecordToolFailure retains only a bounded tool name and error message for
// incomplete-run recovery, not the call's arguments or result payload.
// Credential screening is conservative and best-effort, not a secrecy proof.
func (s *ArtifactSubmission) RecordToolFailure(name, message string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.submitted {
		return
	}
	if strings.TrimSpace(message) == "" {
		message = "tool reported failure without an error message"
	}
	s.toolFailures = append(s.toolFailures, artifactv1.Diagnostic{
		Level: "warning",
		Code:  "tool_execution_failed",
		Message: telemetry.BoundText(fmt.Sprintf("Tool %s reported failure: %s",
			sanitizeToolFailureField(name, maxToolNameRawBytes),
			sanitizeToolFailureField(message, maxToolErrorRawBytes)), maxToolFailureMessage, "tool failure"),
	})
	if len(s.toolFailures) > maxRetainedToolFailures {
		s.toolFailures = append([]artifactv1.Diagnostic(nil), s.toolFailures[1:]...)
		s.omittedToolFailures++
	}
}

func sanitizeToolFailureField(raw string, maxRaw int) string {
	if len(raw) > maxRaw {
		return "[detail omitted: input exceeds byte limit]"
	}
	clean := telemetry.SanitizeText(raw, 0)
	if clean == "" {
		return "[unspecified]"
	}
	// Withhold the whole field for remaining credential indicators, including
	// embedded credential JSON and PEM bodies, instead of clipping raw secrets.
	if telemetry.SensitiveKey(clean) || evidence.DetectSecret([]byte(clean)) {
		return "[detail withheld: credential-like content]"
	}
	return clean
}
