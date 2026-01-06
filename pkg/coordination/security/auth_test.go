package security

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
)

func TestTokenManager_GenerateToken(t *testing.T) {
	tm := NewTokenManager("test-secret-key")

	token, err := tm.GenerateToken("agent-1", []string{"code_execution", "file_access"}, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	if token == "" {
		t.Error("Generated token is empty")
	}
}

func TestTokenManager_ValidateToken(t *testing.T) {
	tm := NewTokenManager("test-secret-key")

	// Generate a valid token
	token, err := tm.GenerateToken("agent-1", []string{"code_execution"}, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Validate the token
	claims, err := tm.ValidateToken(token)
	if err != nil {
		t.Fatalf("Failed to validate token: %v", err)
	}

	if claims.AgentID != "agent-1" {
		t.Errorf("Expected agent ID 'agent-1', got %s", claims.AgentID)
	}

	if len(claims.Capabilities) != 1 || claims.Capabilities[0] != "code_execution" {
		t.Errorf("Expected capabilities [code_execution], got %v", claims.Capabilities)
	}
}

func TestTokenManager_ValidateExpiredToken(t *testing.T) {
	tm := NewTokenManager("test-secret-key")

	// Generate a token that expires immediately
	token, err := tm.GenerateToken("agent-1", []string{"code_execution"}, -1*time.Second)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Validation should fail due to expiration
	_, err = tm.ValidateToken(token)
	if err == nil {
		t.Error("Expected validation to fail for expired token")
	}
}

func TestTokenManager_ValidateInvalidToken(t *testing.T) {
	tm := NewTokenManager("test-secret-key")

	// Try to validate an invalid token
	_, err := tm.ValidateToken("invalid-token")
	if err == nil {
		t.Error("Expected validation to fail for invalid token")
	}
}

func TestTokenManager_ValidateTokenWithWrongSecret(t *testing.T) {
	tm1 := NewTokenManager("secret-1")
	tm2 := NewTokenManager("secret-2")

	// Generate token with tm1
	token, err := tm1.GenerateToken("agent-1", []string{"code_execution"}, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Try to validate with tm2 (different secret)
	_, err = tm2.ValidateToken(token)
	if err == nil {
		t.Error("Expected validation to fail with different secret")
	}
}

func TestTokenManager_RevokeToken(t *testing.T) {
	tm := NewTokenManager("test-secret-key")

	// Generate a token
	token, err := tm.GenerateToken("agent-1", []string{"code_execution"}, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Validate it works
	_, err = tm.ValidateToken(token)
	if err != nil {
		t.Fatalf("Token should be valid: %v", err)
	}

	// Revoke the token
	err = tm.RevokeToken(token)
	if err != nil {
		t.Fatalf("Failed to revoke token: %v", err)
	}

	// Validation should now fail
	_, err = tm.ValidateToken(token)
	if err == nil {
		t.Error("Expected validation to fail for revoked token")
	}
}

func TestTokenManager_RefreshToken(t *testing.T) {
	tm := NewTokenManager("test-secret-key")

	// Generate initial token
	oldToken, err := tm.GenerateToken("agent-1", []string{"code_execution"}, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Refresh the token
	newToken, err := tm.RefreshToken(oldToken, 2*time.Hour)
	if err != nil {
		t.Fatalf("Failed to refresh token: %v", err)
	}

	if newToken == oldToken {
		t.Error("Refreshed token should be different from original")
	}

	// New token should be valid
	claims, err := tm.ValidateToken(newToken)
	if err != nil {
		t.Fatalf("Refreshed token should be valid: %v", err)
	}

	if claims.AgentID != "agent-1" {
		t.Errorf("Expected agent ID 'agent-1', got %s", claims.AgentID)
	}

	// Old token should be revoked
	_, err = tm.ValidateToken(oldToken)
	if err == nil {
		t.Error("Old token should be revoked after refresh")
	}
}

func TestAuthInterceptor_WithValidToken(t *testing.T) {
	tm := NewTokenManager("test-secret-key")
	interceptor := NewAuthInterceptor(tm)

	// Generate a valid token
	token, err := tm.GenerateToken("agent-1", []string{"code_execution"}, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Create context with token in metadata
	md := metadata.Pairs("authorization", "Bearer "+token)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	// Call the interceptor
	newCtx, err := interceptor.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authentication should succeed: %v", err)
	}

	// Check that claims are in context
	claims, ok := ClaimsFromContext(newCtx)
	if !ok {
		t.Fatal("Claims should be in context")
	}

	if claims.AgentID != "agent-1" {
		t.Errorf("Expected agent ID 'agent-1', got %s", claims.AgentID)
	}
}

func TestAuthInterceptor_WithoutToken(t *testing.T) {
	tm := NewTokenManager("test-secret-key")
	interceptor := NewAuthInterceptor(tm)

	// Create context without token
	ctx := context.Background()

	// Authentication should fail
	_, err := interceptor.Authenticate(ctx)
	if err == nil {
		t.Error("Authentication should fail without token")
	}
}

func TestAuthInterceptor_WithInvalidToken(t *testing.T) {
	tm := NewTokenManager("test-secret-key")
	interceptor := NewAuthInterceptor(tm)

	// Create context with invalid token
	md := metadata.Pairs("authorization", "Bearer invalid-token")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	// Authentication should fail
	_, err := interceptor.Authenticate(ctx)
	if err == nil {
		t.Error("Authentication should fail with invalid token")
	}
}

func TestAuthInterceptor_WithMalformedAuthHeader(t *testing.T) {
	tm := NewTokenManager("test-secret-key")
	interceptor := NewAuthInterceptor(tm)

	testCases := []string{
		"invalid-format", // No "Bearer" prefix
		"Bearer",         // Missing token
		"bearer token",   // Wrong case
		"",               // Empty
	}

	for _, tc := range testCases {
		md := metadata.Pairs("authorization", tc)
		ctx := metadata.NewIncomingContext(context.Background(), md)

		_, err := interceptor.Authenticate(ctx)
		if err == nil {
			t.Errorf("Authentication should fail for malformed header: %q", tc)
		}
	}
}

func TestRequireCapability(t *testing.T) {
	tm := NewTokenManager("test-secret-key")

	// Generate token with specific capabilities
	token, _ := tm.GenerateToken("agent-1", []string{"code_execution", "file_access"}, 1*time.Hour)
	claims, _ := tm.ValidateToken(token)

	// Create context with claims
	ctx := context.WithValue(context.Background(), claimsContextKey{}, claims)

	// Test requiring existing capability
	err := RequireCapability(ctx, "code_execution")
	if err != nil {
		t.Errorf("Should have code_execution capability: %v", err)
	}

	// Test requiring non-existent capability
	err = RequireCapability(ctx, "network_access")
	if err == nil {
		t.Error("Should not have network_access capability")
	}
}

func TestRequireCapability_WithoutClaims(t *testing.T) {
	ctx := context.Background()

	err := RequireCapability(ctx, "code_execution")
	if err == nil {
		t.Error("Should fail when no claims in context")
	}
}

func TestTokenCleanup(t *testing.T) {
	tm := NewTokenManager("test-secret-key")

	// Generate and revoke multiple tokens, simulating old revocations
	for i := 0; i < 50; i++ {
		token, _ := tm.GenerateToken("agent-1", []string{"code_execution"}, 1*time.Hour)
		tm.RevokeToken(token)
		// Manually set revocation time to 25 hours ago (older than cleanup threshold)
		claims, _ := tm.ValidateTokenUnsafe(token)
		tm.mu.Lock()
		tm.revokedTokens[claims.ID] = time.Now().Add(-25 * time.Hour)
		tm.mu.Unlock()
	}

	// Also add some recent revocations (should not be cleaned up)
	for i := 0; i < 50; i++ {
		token, _ := tm.GenerateToken("agent-2", []string{"code_execution"}, 1*time.Hour)
		tm.RevokeToken(token)
	}

	// Cleanup should remove old revoked tokens
	initialCount := tm.RevokedTokenCount()
	if initialCount != 100 {
		t.Fatalf("Expected 100 revoked tokens, got %d", initialCount)
	}

	tm.CleanupRevokedTokens()
	finalCount := tm.RevokedTokenCount()

	// Should have removed ~50 old tokens, kept ~50 recent ones
	if finalCount >= 60 || finalCount <= 40 {
		t.Errorf("Cleanup should remove ~50 old tokens (before: %d, after: %d)", initialCount, finalCount)
	}
}
