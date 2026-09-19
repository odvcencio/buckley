package secretsafety

import (
	"os"
	"strings"
	"testing"
)

func TestUnsafePath_RejectsInvalidAndFormattingCharacters(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "ordinary", path: "pkg/main.go"},
		{name: "control", path: "pkg\nmain.go", want: true},
		{name: "formatting", path: "pkg/\u202emain.go", want: true},
		{name: "invalid utf8", path: string([]byte{'p', '/', 0xff, '.', 't', 'x', 't'}), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UnsafePath(tt.path); got != tt.want {
				t.Fatalf("UnsafePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestSecretContent_RecognizesCredentialMarkersWithoutCaseSensitivity(t *testing.T) {
	armored := func(kind string) string {
		return "-----BEGIN " + kind + "PRIVATE" + " KEY-----\n" + strings.Repeat("Ab3dEf7h", 8) + "\n"
	}
	markers := []string{
		armored(""),
		armored("OPENSSH "),
		armored("RSA "),
		armored("EC "),
		armored("DSA "),
		armored("ENCRYPTED "),
		"-----BEGIN PGP PRIVATE" + " KEY BLOCK-----\n" + strings.Repeat("Az7+Lm3/", 8) + "\n",
		"AWS_SECRET_ACCESS_KEY=" + strings.Repeat("Ab3dEf7h", 5),
		"aws_secret_access_key: " + strings.Repeat("aB9/cD2+eF7=", 4),
		"github_pat_" + strings.Repeat("Ab3dEf7h", 8),
		"ghp_" + strings.Repeat("Ab3dEf7hJ", 4),
		"xoxb-" + strings.Repeat("12Ab-cDe", 4),
		"xoxp-" + strings.Repeat("34Ef-gHi", 4),
		"Authorization: Bearer " + strings.Repeat("Ab3dEf7h", 4),
		"X-Api-Key: " + strings.Repeat("Cd5jKl9m", 4),
		"api_key=" + strings.Repeat("Ef7nOp2q", 4),
		"api-key=" + strings.Repeat("Gh9rSt4u", 4),
		"access_token=" + strings.Repeat("Ij2vWx6y", 4),
		"client_secret=" + strings.Repeat("Kl4zAb8c", 4),
	}
	for _, content := range markers {
		t.Run(strings.ReplaceAll(strings.ToLower(content), "=", "_"), func(t *testing.T) {
			if !SecretContent([]byte(content)) {
				t.Fatalf("SecretContent(%q) = false, want true", content)
			}
		})
	}
	if SecretContent([]byte("package example\nfunc main() {}\n")) {
		t.Fatal("ordinary source content was classified as secret")
	}
}

func TestSecretContent_AllowsMarkerSourceAndShortPlaceholders(t *testing.T) {
	safe := []string{
		`const header = "-----BEGIN RSA PRIVATE KEY-----"`,
		`regexp.MustCompile("ghp_[A-Za-z0-9]{36}")`,
		`client_secret=your-secret-here`,
		`Authorization: Bearer <token>`,
		`AWS_SECRET_ACCESS_KEY=xxx`,
	}
	for _, content := range safe {
		if SecretContent([]byte(content)) {
			t.Fatalf("safe marker source was classified as secret: %q", content)
		}
	}
	for _, name := range []string{"classifier.go", "classifier_test.go"} {
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if SecretContent(content) {
			t.Fatalf("%s classified its own source as secret", name)
		}
	}
}

func TestAutomaticDisclosureSecretContent_PreservesConservativeMarkerBoundary(t *testing.T) {
	for _, content := range []string{
		"AWS_SECRET_ACCESS_" + "KEY=short",
		"client_" + "secret=placeholder",
		"ghp_" + "short",
		"-----BEGIN RSA " + "PRIVATE KEY-----",
	} {
		if !AutomaticDisclosureSecretContent([]byte(content)) {
			t.Fatalf("automatic disclosure admitted conservative marker %q", content)
		}
	}
}

func TestBinaryContent_RejectsNonTextBytes(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    bool
	}{
		{name: "text", content: []byte("line one\nline two\r\n\tindented")},
		{name: "nul", content: []byte{'a', 0, 'b'}, want: true},
		{name: "delete", content: []byte{'a', 0x7f, 'b'}, want: true},
		{name: "invalid utf8", content: []byte{0xff, 0xfe}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BinaryContent(tt.content); got != tt.want {
				t.Fatalf("BinaryContent(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestSensitivePath_ConservativeBoundaries(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "main.go"},
		{path: "docs/example.env"},
		{path: "sample.env"},
		{path: ".env.example"},
		{path: ".env", want: true},
		{path: "config/.env.production", want: true},
		{path: "credentials.json", want: true},
		{path: "service-account.yaml", want: true},
		{path: "private-key.txt", want: true},
		{path: "keys/id_ed25519", want: true},
		{path: "secret/config.txt", want: true},
		{path: ".aws/credentials", want: true},
		{path: ".kube/config", want: true},
		{path: ".docker/config.json", want: true},
		{path: "image.PNG", want: true},
		{path: "api-key.json", want: true},
		{path: "oauth.toml", want: true},
		{path: "bad\tname.txt", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := SensitivePath(tt.path); got != tt.want {
				t.Fatalf("SensitivePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestCredentialPath_DoesNotTreatOrdinaryBinaryArtifactsAsCredentials(t *testing.T) {
	for _, path := range []string{"dist/app.wasm", "assets/font.woff2", "bin/tool.exe"} {
		if CredentialPath(path) {
			t.Errorf("CredentialPath(%q) = true, want false", path)
		}
		if !SensitivePath(path) {
			t.Errorf("SensitivePath(%q) = false, want disclosure exclusion", path)
		}
	}
	for _, path := range []string{".env", "keys/private.key", "config/access-token.json"} {
		if !CredentialPath(path) {
			t.Errorf("CredentialPath(%q) = false, want true", path)
		}
	}
}

func TestBinaryPath_RecognizesNonSourceExtensions(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "archive.tar.gz", want: true},
		{path: "photo.JpEg", want: true},
		{path: "program.wasm", want: true},
		{path: "report.pdf", want: true},
		{path: "main.go"},
		{path: "README.md"},
	}
	for _, tt := range tests {
		if got := BinaryPath(tt.path); got != tt.want {
			t.Errorf("BinaryPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
