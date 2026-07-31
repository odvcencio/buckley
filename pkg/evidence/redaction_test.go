package evidence

import (
	"strings"
	"testing"
)

func TestDetectSecret(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"aws access key", "AKIAIOSFODNN7EXAMPLE", true},
		{"github pat", "token: ghp_" + strings.Repeat("a", 36), true},
		{"pem private key", "-----BEGIN RSA PRIVATE KEY-----\nMIIB...\n-----END RSA PRIVATE KEY-----", true},
		{"openai style key", "sk-" + strings.Repeat("A", 32), true},
		{"plain source", "func main() {\n\tfmt.Println(\"hi\")\n}", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DetectSecret([]byte(tc.content))
			if got != tc.want {
				t.Fatalf("DetectSecret(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

func TestClassifySensitivity(t *testing.T) {
	if got := classifySensitivity(KindSource, []byte("plain code")); got != SensitivityWorkspace {
		t.Fatalf("classifySensitivity(plain) = %v, want workspace", got)
	}
	secret := []byte("AKIAIOSFODNN7EXAMPLE")
	if got := classifySensitivity(KindSource, secret); got != SensitivitySecretDetected {
		t.Fatalf("classifySensitivity(secret) = %v, want secret_detected", got)
	}
}

func TestRedact(t *testing.T) {
	input := []byte("aws key AKIAIOSFODNN7EXAMPLE embedded in log output")
	redacted := Redact(input)
	if strings.Contains(string(redacted), "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("Redact() left secret in output: %q", redacted)
	}
	if !strings.Contains(string(redacted), "[REDACTED]") {
		t.Fatalf("Redact() did not mark redaction: %q", redacted)
	}
}

func TestSummarizeForTelemetry_NeverIncludesBody(t *testing.T) {
	obj := Object{
		ID:            "ev_test",
		Kind:          KindSource,
		ContentSHA256: "deadbeef",
		ByteCount:     42,
		Sensitivity:   SensitivityWorkspace,
		InlineBody:    []byte("SECRET RAW CONTENT THAT MUST NEVER APPEAR IN TELEMETRY"),
	}
	summary := SummarizeForTelemetry(obj)
	if strings.Contains(summary, "SECRET RAW CONTENT") {
		t.Fatalf("SummarizeForTelemetry leaked raw content: %q", summary)
	}
	if !strings.Contains(summary, obj.ID) || !strings.Contains(summary, obj.ContentSHA256) {
		t.Fatalf("SummarizeForTelemetry missing identity fields: %q", summary)
	}
}
