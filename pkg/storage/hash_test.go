package storage

import (
	"testing"
)

func TestHashSecret(t *testing.T) {
	tests := []struct {
		name     string
		secret   string
		expected string
	}{
		{
			name:     "basic secret",
			secret:   "my-secret-token",
			expected: "ea5add57437cbf20af59034d7ed17968dcc56767b41965fcc5b376d45db8b4a3",
		},
		{
			name:     "empty string",
			secret:   "",
			expected: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:     "secret with whitespace",
			secret:   "  my-secret-token  ",
			expected: "ea5add57437cbf20af59034d7ed17968dcc56767b41965fcc5b376d45db8b4a3",
		},
		{
			name:     "secret with newline",
			secret:   "my-secret-token\n",
			expected: "ea5add57437cbf20af59034d7ed17968dcc56767b41965fcc5b376d45db8b4a3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hashSecret(tt.secret)
			if result != tt.expected {
				t.Errorf("hashSecret(%q) = %q, expected %q", tt.secret, result, tt.expected)
			}
		})
	}
}

func TestHashSecretConsistency(t *testing.T) {
	secret := "consistent-secret"
	hash1 := hashSecret(secret)
	hash2 := hashSecret(secret)

	if hash1 != hash2 {
		t.Errorf("hashSecret should be deterministic, got different hashes: %q vs %q", hash1, hash2)
	}
}

func TestHashSecretDifferentInputs(t *testing.T) {
	hash1 := hashSecret("secret1")
	hash2 := hashSecret("secret2")

	if hash1 == hash2 {
		t.Errorf("different secrets should produce different hashes")
	}
}
