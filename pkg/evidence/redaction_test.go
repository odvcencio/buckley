package evidence

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedact_LegacyOutputAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"plain", []byte("diagnostic without credentials")},
		{"invalid UTF-8", []byte("before\xff\xfe after")},
		{"aws access", []byte("AKIAIOSFODNN7EXAMPLE")},
		{"aws secret", []byte("AWS_SECRET_ACCESS_KEY = " + strings.Repeat("a", 32))},
		{"private key", []byte("-----BEGIN OPENSSH PRIVATE KEY-----")},
		{"github classic", []byte("ghp_" + strings.Repeat("a", 36))},
		{"github other", []byte("gho_" + strings.Repeat("b", 24))},
		{"openai", []byte("sk-proj-" + strings.Repeat("c", 24))},
		{"slack", []byte("xoxb-" + strings.Repeat("d", 24))},
		{"bearer", []byte("BEARER " + strings.Repeat("e", 24) + "==")},
		{"overlapping patterns", []byte("bearer sk-" + strings.Repeat("a", 30) + " ghp_" + strings.Repeat("b", 36))},
		{"repeated secrets", []byte(strings.Repeat("sk-"+strings.Repeat("a", 30)+"\n", 400))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := bytes.Clone(tc.content)
			want := tc.content
			for _, pattern := range secretPatterns {
				want = pattern.ReplaceAll(want, []byte("[REDACTED]"))
			}
			got := Redact(tc.content)
			if !bytes.Equal(got, want) || (got == nil) != (want == nil) {
				t.Fatal("redaction changed legacy output")
			}
			if !bytes.Equal(tc.content, original) {
				t.Fatal("redaction mutated input")
			}
			if len(got) > 0 {
				got[0] ^= 0xff
				if !bytes.Equal(tc.content, original) {
					t.Fatal("redacted output aliases input")
				}
			}
		})
	}
}

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
