package security

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TestAuthenticationAuthorizationIntegration tests the complete flow of:
// 1. Generating a token for an agent
// 2. Validating the token via gRPC interceptor
// 3. Checking tool access permissions
// 4. Auditing the access attempt
func TestAuthenticationAuthorizationIntegration(t *testing.T) {
	// Setup: Create token manager and tool approver
	tokenManager := NewTokenManager("integration-test-secret")
	policy := DefaultToolPolicy()
	approver := NewToolApprover(policy)

	// Agent with code_analysis capability
	agentID := "integration-test-agent"
	capabilities := []string{"code_analysis"}

	// Step 1: Generate authentication token
	token, err := tokenManager.GenerateToken(agentID, capabilities, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Step 2: Simulate gRPC request with token
	md := metadata.Pairs("authorization", "Bearer "+token)
	ctx := metadata.NewIncomingContext(context.Background(), md)

	// Step 3: Authenticate via interceptor
	interceptor := NewAuthInterceptor(tokenManager)
	authCtx, err := interceptor.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}

	// Verify claims are in context
	claims, ok := ClaimsFromContext(authCtx)
	if !ok {
		t.Fatal("Claims not found in authenticated context")
	}
	if claims.AgentID != agentID {
		t.Errorf("Expected agent ID %s, got %s", agentID, claims.AgentID)
	}

	// Step 4: Check tool access permissions
	// Should be allowed: search (part of code_analysis)
	err = approver.CheckToolAccess(authCtx, "search")
	if err != nil {
		t.Errorf("search should be allowed for code_analysis: %v", err)
	}

	// Should be denied: shell (not in code_analysis capability)
	err = approver.CheckToolAccess(authCtx, "shell")
	if err == nil {
		t.Error("shell should NOT be allowed for code_analysis")
	}

	// Step 5: Verify audit log
	auditEntries := approver.GetAuditLog(agentID, 10)
	if len(auditEntries) != 2 {
		t.Errorf("Expected 2 audit entries, got %d", len(auditEntries))
	}

	// First entry: search allowed
	if !auditEntries[0].Allowed {
		t.Error("First audit entry should be allowed")
	}
	if auditEntries[0].ToolName != "search" {
		t.Errorf("Expected search, got %s", auditEntries[0].ToolName)
	}

	// Second entry: shell denied
	if auditEntries[1].Allowed {
		t.Error("Second audit entry should be denied")
	}
	if auditEntries[1].ToolName != "shell" {
		t.Errorf("Expected shell, got %s", auditEntries[1].ToolName)
	}
}

// TestTokenRevocationIntegration tests:
// 1. Token generation
// 2. Token usage for authentication
// 3. Token revocation
// 4. Rejection of revoked token
func TestTokenRevocationIntegration(t *testing.T) {
	tokenManager := NewTokenManager("revocation-test-secret")
	policy := DefaultToolPolicy()
	approver := NewToolApprover(policy)

	// Generate token
	token, err := tokenManager.GenerateToken("revocation-agent", []string{"file_access"}, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Use token successfully
	md := metadata.Pairs("authorization", "Bearer "+token)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	interceptor := NewAuthInterceptor(tokenManager)
	authCtx, err := interceptor.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Initial authentication failed: %v", err)
	}

	// Verify tool access works
	err = approver.CheckToolAccess(authCtx, "file")
	if err != nil {
		t.Errorf("file access should work before revocation: %v", err)
	}

	// Revoke the token
	err = tokenManager.RevokeToken(token)
	if err != nil {
		t.Fatalf("Failed to revoke token: %v", err)
	}

	// Try to use revoked token
	ctx2 := metadata.NewIncomingContext(context.Background(), md)
	_, err = interceptor.Authenticate(ctx2)
	if err == nil {
		t.Error("Authentication should fail for revoked token")
	}
}

// TestTokenRefreshIntegration tests:
// 1. Generate initial token
// 2. Use token for authentication
// 3. Refresh token
// 4. Verify old token is revoked
// 5. Verify new token works
func TestTokenRefreshIntegration(t *testing.T) {
	tokenManager := NewTokenManager("refresh-test-secret")
	interceptor := NewAuthInterceptor(tokenManager)

	// Generate initial token
	oldToken, err := tokenManager.GenerateToken("refresh-agent", []string{"testing"}, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate initial token: %v", err)
	}

	// Verify old token works
	md := metadata.Pairs("authorization", "Bearer "+oldToken)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err = interceptor.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Old token should work: %v", err)
	}

	// Refresh token
	newToken, err := tokenManager.RefreshToken(oldToken, 2*time.Hour)
	if err != nil {
		t.Fatalf("Failed to refresh token: %v", err)
	}

	// Verify old token is revoked
	ctx2 := metadata.NewIncomingContext(context.Background(), md)
	_, err = interceptor.Authenticate(ctx2)
	if err == nil {
		t.Error("Old token should be revoked after refresh")
	}

	// Verify new token works
	newMd := metadata.Pairs("authorization", "Bearer "+newToken)
	ctx3 := metadata.NewIncomingContext(context.Background(), newMd)
	authCtx, err := interceptor.Authenticate(ctx3)
	if err != nil {
		t.Fatalf("New token should work: %v", err)
	}

	// Verify claims are preserved
	claims, ok := ClaimsFromContext(authCtx)
	if !ok {
		t.Fatal("Claims not found in context")
	}
	if claims.AgentID != "refresh-agent" {
		t.Errorf("Expected refresh-agent, got %s", claims.AgentID)
	}
	if len(claims.Capabilities) != 1 || claims.Capabilities[0] != "testing" {
		t.Errorf("Capabilities not preserved: %v", claims.Capabilities)
	}
}

// TestAdminBypassIntegration tests that admin capability bypasses all restrictions
func TestAdminBypassIntegration(t *testing.T) {
	tokenManager := NewTokenManager("admin-test-secret")
	policy := DefaultToolPolicy()
	approver := NewToolApprover(policy)
	interceptor := NewAuthInterceptor(tokenManager)

	// Generate admin token
	token, err := tokenManager.GenerateToken("admin-agent", []string{"admin"}, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate admin token: %v", err)
	}

	// Authenticate
	md := metadata.Pairs("authorization", "Bearer "+token)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	authCtx, err := interceptor.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Admin authentication failed: %v", err)
	}

	// Admin should have access to ALL tools
	dangerousTools := []string{"shell", "file", "git", "refactoring"}
	for _, tool := range dangerousTools {
		err := approver.CheckToolAccess(authCtx, tool)
		if err != nil {
			t.Errorf("Admin should have access to %s: %v", tool, err)
		}
	}

	// Admin should even have access to non-existent tools
	err = approver.CheckToolAccess(authCtx, "nonexistent_tool")
	if err != nil {
		t.Errorf("Admin should bypass all restrictions: %v", err)
	}
}

// TestMultipleCapabilitiesIntegration tests an agent with multiple capabilities
func TestMultipleCapabilitiesIntegration(t *testing.T) {
	tokenManager := NewTokenManager("multi-cap-test-secret")
	policy := DefaultToolPolicy()
	approver := NewToolApprover(policy)
	interceptor := NewAuthInterceptor(tokenManager)

	// Agent with multiple capabilities
	capabilities := []string{"code_analysis", "testing", "documentation"}
	token, err := tokenManager.GenerateToken("multi-cap-agent", capabilities, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Authenticate
	md := metadata.Pairs("authorization", "Bearer "+token)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	authCtx, err := interceptor.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}

	// Should have access to tools from all capabilities
	allowedTools := []string{
		"search",        // code_analysis
		"testing",       // testing
		"documentation", // documentation
	}

	for _, tool := range allowedTools {
		err := approver.CheckToolAccess(authCtx, tool)
		if err != nil {
			t.Errorf("Should have access to %s: %v", tool, err)
		}
	}

	// Should NOT have access to tools outside capabilities
	deniedTools := []string{"shell", "file", "git"}
	for _, tool := range deniedTools {
		err := approver.CheckToolAccess(authCtx, tool)
		if err == nil {
			t.Errorf("Should NOT have access to %s", tool)
		}
	}

	// Verify GetAllowedToolsForAgent returns all tools
	allowedToolsList := approver.GetAllowedToolsForAgent(authCtx)
	if len(allowedToolsList) == 0 {
		t.Error("Should return non-empty list of allowed tools")
	}

	// Verify the list contains tools from all capabilities
	hasSearch := false
	hasTesting := false
	hasDocumentation := false
	for _, tool := range allowedToolsList {
		if tool == "search" {
			hasSearch = true
		}
		if tool == "testing" {
			hasTesting = true
		}
		if tool == "documentation" {
			hasDocumentation = true
		}
	}

	if !hasSearch || !hasTesting || !hasDocumentation {
		t.Error("Allowed tools list should contain tools from all capabilities")
	}
}

// TestUnaryInterceptorIntegration tests the gRPC unary interceptor
func TestUnaryInterceptorIntegration(t *testing.T) {
	tokenManager := NewTokenManager("unary-test-secret")
	interceptor := NewAuthInterceptor(tokenManager)

	// Generate token
	token, err := tokenManager.GenerateToken("unary-agent", []string{"code_analysis"}, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Create unary interceptor
	unaryInterceptor := interceptor.UnaryInterceptor()

	// Mock handler that checks for claims in context
	handlerCalled := false
	mockHandler := func(ctx context.Context, req interface{}) (interface{}, error) {
		handlerCalled = true
		claims, ok := ClaimsFromContext(ctx)
		if !ok {
			t.Error("Claims not found in handler context")
		}
		if claims.AgentID != "unary-agent" {
			t.Errorf("Expected unary-agent, got %s", claims.AgentID)
		}
		return "success", nil
	}

	// Simulate authenticated request
	md := metadata.Pairs("authorization", "Bearer "+token)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}

	// Call interceptor
	_, err = unaryInterceptor(ctx, nil, info, mockHandler)
	if err != nil {
		t.Fatalf("Unary interceptor failed: %v", err)
	}

	if !handlerCalled {
		t.Error("Handler should have been called")
	}

	// Test with invalid token
	invalidMd := metadata.Pairs("authorization", "Bearer invalid-token")
	ctx2 := metadata.NewIncomingContext(context.Background(), invalidMd)
	handlerCalled = false

	_, err = unaryInterceptor(ctx2, nil, info, mockHandler)
	if err == nil {
		t.Error("Interceptor should reject invalid token")
	}
	if handlerCalled {
		t.Error("Handler should not be called for invalid token")
	}
}

// TestExpiredTokenIntegration tests that expired tokens are rejected
func TestExpiredTokenIntegration(t *testing.T) {
	tokenManager := NewTokenManager("expiry-test-secret")
	interceptor := NewAuthInterceptor(tokenManager)

	// Generate token with 1 millisecond expiry
	token, err := tokenManager.GenerateToken("expiry-agent", []string{"testing"}, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Wait for token to expire
	time.Sleep(10 * time.Millisecond)

	// Try to use expired token
	md := metadata.Pairs("authorization", "Bearer "+token)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	_, err = interceptor.Authenticate(ctx)
	if err == nil {
		t.Error("Expired token should be rejected")
	}
}

// TestCapabilityRequirementIntegration tests RequireCapability function
func TestCapabilityRequirementIntegration(t *testing.T) {
	tokenManager := NewTokenManager("capability-test-secret")
	interceptor := NewAuthInterceptor(tokenManager)

	// Generate token with specific capabilities
	token, err := tokenManager.GenerateToken("capability-agent", []string{"code_analysis", "testing"}, 1*time.Hour)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Authenticate
	md := metadata.Pairs("authorization", "Bearer "+token)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	authCtx, err := interceptor.Authenticate(ctx)
	if err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}

	// Should have required capabilities
	err = RequireCapability(authCtx, "code_analysis")
	if err != nil {
		t.Errorf("Should have code_analysis capability: %v", err)
	}

	err = RequireCapability(authCtx, "testing")
	if err != nil {
		t.Errorf("Should have testing capability: %v", err)
	}

	// Should NOT have unrequested capability
	err = RequireCapability(authCtx, "shell_execution")
	if err == nil {
		t.Error("Should NOT have shell_execution capability")
	}
}
