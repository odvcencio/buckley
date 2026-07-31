package evidence

import "regexp"

// RedactionVersion identifies the redaction ruleset applied by Redact and
// SummarizeForTelemetry. It is stamped onto exported run ledger events
// (section 14.2, Event.Redaction) so that a consumer of an older export can
// tell which rules produced it.
const RedactionVersion = "evidence-redaction-v1"

// secretPatterns is a best-effort, non-exhaustive set of credential shapes.
// A match marks an object's Sensitivity as SensitivitySecretDetected
// (section 13.3). This is intentionally conservative: false positives over-
// restrict an object, false negatives do not, so patterns favor precision
// only where it is cheap (fixed prefixes, PEM headers).
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                                       // AWS access key ID
	regexp.MustCompile(`(?i)aws_secret_access_key\s*[:=]\s*[A-Za-z0-9/+=]{20,}`), // AWS secret key assignment
	regexp.MustCompile(`-----BEGIN (RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`),   // PEM private key
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),                                    // GitHub personal access token
	regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`),                             // Other GitHub token variants
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),                                    // OpenAI-style API key
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),                           // Slack token
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9\-._~+/]{20,}=*`),                 // Bearer token
}

// DetectSecret reports whether content appears to contain credential
// material, using a fixed set of known secret shapes. It does not attempt
// generic high-entropy detection, which is prone to false positives on
// hashes and IDs that are not secrets.
func DetectSecret(content []byte) bool {
	for _, pattern := range secretPatterns {
		if pattern.Match(content) {
			return true
		}
	}
	return false
}

// classifySensitivity returns the default Sensitivity for newly written
// evidence of the given kind, upgrading to SensitivitySecretDetected when
// DetectSecret matches the content (section 13.3: "Source defaults to
// workspace. Results with detected credentials become secret_detected.").
func classifySensitivity(kind Kind, content []byte) Sensitivity {
	if DetectSecret(content) {
		return SensitivitySecretDetected
	}
	return SensitivityWorkspace
}

// Redact returns a copy of content with any detected secret substrings
// replaced by a fixed-width placeholder. It is applied to telemetry
// payloads, tool argument summaries, exported run ledgers, and remote
// analytics (section 13.3) — any surface where raw evidence content must
// never appear.
func Redact(content []byte) []byte {
	out := content
	for _, pattern := range secretPatterns {
		out = pattern.ReplaceAll(out, []byte("[REDACTED]"))
	}
	return out
}

// SummarizeForTelemetry returns a description of obj safe to write to
// telemetry logs: identity, kind, size, and hash only, never body content.
// This is the mechanism behind the PR 4 acceptance criterion "no raw content
// in telemetry logs."
func SummarizeForTelemetry(obj Object) string {
	return "evidence{id=" + obj.ID +
		" kind=" + string(obj.Kind) +
		" sha256=" + obj.ContentSHA256 +
		" bytes=" + itoa(obj.ByteCount) +
		" sensitivity=" + string(obj.Sensitivity) + "}"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
