package storage

import (
	"path/filepath"
	"testing"
)

func TestGenerateAPITokenValue(t *testing.T) {
	token1, err := GenerateAPITokenValue()
	if err != nil {
		t.Fatalf("GenerateAPITokenValue() error = %v", err)
	}

	// Token should be 64 hex characters (32 bytes)
	if len(token1) != 64 {
		t.Errorf("expected token length 64, got %d", len(token1))
	}

	// Generate another token - should be different
	token2, err := GenerateAPITokenValue()
	if err != nil {
		t.Fatalf("GenerateAPITokenValue() error = %v", err)
	}

	if token1 == token2 {
		t.Error("expected unique tokens, got duplicates")
	}
}

func TestNormalizeScope(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"operator", TokenScopeOperator},
		{"OPERATOR", TokenScopeOperator},
		{"  operator  ", TokenScopeOperator},
		{"viewer", TokenScopeViewer},
		{"VIEWER", TokenScopeViewer},
		{"member", TokenScopeMember},
		{"", TokenScopeMember},
		{"unknown", TokenScopeMember},
		{"admin", TokenScopeMember}, // defaults to member
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeScope(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeScope(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestTokenPrefix(t *testing.T) {
	tests := []struct {
		secret   string
		expected string
	}{
		{"abcdefghij", "abcdefgh"},
		{"short", "short"},
		{"abc", "abc"},
		{"", ""},
		{"12345678901234567890", "12345678"},
	}

	for _, tt := range tests {
		t.Run(tt.secret, func(t *testing.T) {
			got := tokenPrefix(tt.secret)
			if got != tt.expected {
				t.Errorf("tokenPrefix(%q) = %q, want %q", tt.secret, got, tt.expected)
			}
		})
	}
}

func TestAPITokenCRUD(t *testing.T) {
	store := setupAPITokenTestStore(t)
	defer store.Close()

	// Generate a token value
	secret, err := GenerateAPITokenValue()
	if err != nil {
		t.Fatalf("GenerateAPITokenValue() error = %v", err)
	}

	// Create token
	token, err := store.CreateAPIToken("test-token", "test-user", "operator", secret)
	if err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}

	if token.Name != "test-token" {
		t.Errorf("expected name 'test-token', got %q", token.Name)
	}
	if token.Scope != TokenScopeOperator {
		t.Errorf("expected scope 'operator', got %q", token.Scope)
	}
	if token.Revoked {
		t.Error("expected token to not be revoked")
	}

	// Validate token
	validated, err := store.ValidateAPIToken(secret)
	if err != nil {
		t.Fatalf("ValidateAPIToken() error = %v", err)
	}
	if validated == nil {
		t.Fatal("expected validated token, got nil")
	}
	if validated.ID != token.ID {
		t.Errorf("expected token ID %q, got %q", token.ID, validated.ID)
	}

	// List tokens
	tokens, err := store.ListAPITokens()
	if err != nil {
		t.Fatalf("ListAPITokens() error = %v", err)
	}
	if len(tokens) != 1 {
		t.Errorf("expected 1 token, got %d", len(tokens))
	}

	// Revoke token
	err = store.RevokeAPIToken(token.ID)
	if err != nil {
		t.Fatalf("RevokeAPIToken() error = %v", err)
	}

	// Validate revoked token should fail
	validated, err = store.ValidateAPIToken(secret)
	if err == nil && validated != nil {
		t.Error("expected validation to fail for revoked token")
	}
}

func TestCreateAPIToken_DefaultName(t *testing.T) {
	store := setupAPITokenTestStore(t)
	defer store.Close()

	secret, _ := GenerateAPITokenValue()
	token, err := store.CreateAPIToken("", "owner", "member", secret)
	if err != nil {
		t.Fatalf("CreateAPIToken() error = %v", err)
	}

	if token.Name == "" {
		t.Error("expected default name to be generated")
	}
	if len(token.Name) < 10 {
		t.Errorf("expected generated name to be longer, got %q", token.Name)
	}
}

func TestValidateAPIToken_InvalidToken(t *testing.T) {
	store := setupAPITokenTestStore(t)
	defer store.Close()

	// Validate non-existent token
	validated, err := store.ValidateAPIToken("nonexistent-token-value")
	if err == nil && validated != nil {
		t.Error("expected validation to fail for invalid token")
	}
}

// setupAPITokenTestStore creates a temporary store for testing
func setupAPITokenTestStore(t *testing.T) *Store {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create test store: %v", err)
	}
	return store
}
