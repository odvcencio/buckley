package security

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"
)

// TestTokenGenerationLoad tests token generation under concurrent load
func TestTokenGenerationLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	tokenManager := NewTokenManager("load-test-secret")

	numGoroutines := 100
	tokensPerGoroutine := 100
	totalTokens := numGoroutines * tokensPerGoroutine

	var wg sync.WaitGroup
	var successCount atomic.Int32
	var errorCount atomic.Int32
	tokens := make([]string, totalTokens)

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < tokensPerGoroutine; j++ {
				agentID := fmt.Sprintf("agent-%d-%d", goroutineID, j)
				token, err := tokenManager.GenerateToken(
					agentID,
					[]string{"testing"},
					1*time.Hour,
				)

				if err != nil {
					errorCount.Add(1)
					t.Errorf("Failed to generate token: %v", err)
				} else {
					successCount.Add(1)
					tokens[goroutineID*tokensPerGoroutine+j] = token
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	// Report results
	t.Logf("Token Generation Load Test:")
	t.Logf("  Total tokens: %d", totalTokens)
	t.Logf("  Successful: %d", successCount.Load())
	t.Logf("  Errors: %d", errorCount.Load())
	t.Logf("  Duration: %v", duration)
	t.Logf("  Tokens/sec: %.2f", float64(totalTokens)/duration.Seconds())

	if errorCount.Load() > 0 {
		t.Errorf("Expected 0 errors, got %d", errorCount.Load())
	}

	if successCount.Load() != int32(totalTokens) {
		t.Errorf("Expected %d successful tokens, got %d", totalTokens, successCount.Load())
	}
}

// TestTokenValidationLoad tests token validation under concurrent load
func TestTokenValidationLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	tokenManager := NewTokenManager("validation-load-test-secret")

	// Pre-generate tokens
	numTokens := 1000
	tokens := make([]string, numTokens)
	for i := 0; i < numTokens; i++ {
		token, err := tokenManager.GenerateToken(
			fmt.Sprintf("agent-%d", i),
			[]string{"testing"},
			1*time.Hour,
		)
		if err != nil {
			t.Fatalf("Failed to generate token %d: %v", i, err)
		}
		tokens[i] = token
	}

	// Concurrent validation
	numGoroutines := 50
	validationsPerGoroutine := 1000
	totalValidations := numGoroutines * validationsPerGoroutine

	var wg sync.WaitGroup
	var successCount atomic.Int32
	var errorCount atomic.Int32

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for j := 0; j < validationsPerGoroutine; j++ {
				// Randomly select a token to validate
				tokenIndex := j % numTokens
				_, err := tokenManager.ValidateToken(tokens[tokenIndex])

				if err != nil {
					errorCount.Add(1)
				} else {
					successCount.Add(1)
				}
			}
		}()
	}

	wg.Wait()
	duration := time.Since(start)

	// Report results
	t.Logf("Token Validation Load Test:")
	t.Logf("  Total validations: %d", totalValidations)
	t.Logf("  Successful: %d", successCount.Load())
	t.Logf("  Errors: %d", errorCount.Load())
	t.Logf("  Duration: %v", duration)
	t.Logf("  Validations/sec: %.2f", float64(totalValidations)/duration.Seconds())

	if errorCount.Load() > 0 {
		t.Errorf("Expected 0 errors, got %d", errorCount.Load())
	}
}

// TestToolApprovalLoad tests tool approval checking under concurrent load
func TestToolApprovalLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	tokenManager := NewTokenManager("approval-load-test-secret")
	policy := DefaultToolPolicy()
	approver := NewToolApprover(policy)

	// Pre-generate tokens with various capabilities
	numAgents := 100
	agentTokens := make([]string, numAgents)
	capabilities := [][]string{
		{"code_analysis"},
		{"shell_execution"},
		{"file_access"},
		{"git_access"},
		{"admin"},
	}

	for i := 0; i < numAgents; i++ {
		cap := capabilities[i%len(capabilities)]
		token, err := tokenManager.GenerateToken(
			fmt.Sprintf("agent-%d", i),
			cap,
			1*time.Hour,
		)
		if err != nil {
			t.Fatalf("Failed to generate token: %v", err)
		}
		agentTokens[i] = token
	}

	// Concurrent tool approval checks
	numGoroutines := 100
	checksPerGoroutine := 1000
	totalChecks := numGoroutines * checksPerGoroutine

	tools := []string{"search", "shell", "file", "git", "testing", "documentation"}

	var wg sync.WaitGroup
	var allowedCount atomic.Int32
	var deniedCount atomic.Int32

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			// Create authenticated context
			agentIndex := goroutineID % numAgents
			token := agentTokens[agentIndex]
			md := metadata.Pairs("authorization", "Bearer "+token)
			ctx := metadata.NewIncomingContext(context.Background(), md)
			interceptor := NewAuthInterceptor(tokenManager)
			authCtx, err := interceptor.Authenticate(ctx)
			if err != nil {
				t.Errorf("Authentication failed: %v", err)
				return
			}

			for j := 0; j < checksPerGoroutine; j++ {
				tool := tools[j%len(tools)]
				err := approver.CheckToolAccess(authCtx, tool)
				if err != nil {
					deniedCount.Add(1)
				} else {
					allowedCount.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	// Report results
	t.Logf("Tool Approval Load Test:")
	t.Logf("  Total checks: %d", totalChecks)
	t.Logf("  Allowed: %d", allowedCount.Load())
	t.Logf("  Denied: %d", deniedCount.Load())
	t.Logf("  Duration: %v", duration)
	t.Logf("  Checks/sec: %.2f", float64(totalChecks)/duration.Seconds())

	totalProcessed := allowedCount.Load() + deniedCount.Load()
	if totalProcessed != int32(totalChecks) {
		t.Errorf("Expected %d total checks, got %d", totalChecks, totalProcessed)
	}
}

// TestAuditLogLoad tests audit logging under concurrent load
func TestAuditLogLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	tokenManager := NewTokenManager("audit-load-test-secret")
	policy := DefaultToolPolicy()
	approver := NewToolApprover(policy)

	// Generate tokens for multiple agents
	numAgents := 50
	agentTokens := make([]string, numAgents)
	agentIDs := make([]string, numAgents)

	for i := 0; i < numAgents; i++ {
		agentID := fmt.Sprintf("agent-%d", i)
		agentIDs[i] = agentID
		token, err := tokenManager.GenerateToken(
			agentID,
			[]string{"code_analysis"},
			1*time.Hour,
		)
		if err != nil {
			t.Fatalf("Failed to generate token: %v", err)
		}
		agentTokens[i] = token
	}

	// Concurrent tool access attempts (to generate audit logs)
	numGoroutines := 50
	attemptsPerGoroutine := 100
	totalAttempts := numGoroutines * attemptsPerGoroutine

	var wg sync.WaitGroup
	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			agentIndex := goroutineID % numAgents
			token := agentTokens[agentIndex]
			md := metadata.Pairs("authorization", "Bearer "+token)
			ctx := metadata.NewIncomingContext(context.Background(), md)
			interceptor := NewAuthInterceptor(tokenManager)
			authCtx, err := interceptor.Authenticate(ctx)
			if err != nil {
				t.Errorf("Authentication failed: %v", err)
				return
			}

			for j := 0; j < attemptsPerGoroutine; j++ {
				// Alternate between allowed and denied tools
				if j%2 == 0 {
					approver.CheckToolAccess(authCtx, "search") // allowed
				} else {
					approver.CheckToolAccess(authCtx, "shell") // denied
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	// Verify audit logs were created
	totalAuditEntries := 0
	for _, agentID := range agentIDs {
		entries := approver.GetAuditLog(agentID, 1000)
		totalAuditEntries += len(entries)
	}

	t.Logf("Audit Log Load Test:")
	t.Logf("  Total attempts: %d", totalAttempts)
	t.Logf("  Total audit entries: %d", totalAuditEntries)
	t.Logf("  Duration: %v", duration)
	t.Logf("  Attempts/sec: %.2f", float64(totalAttempts)/duration.Seconds())

	// Note: totalAuditEntries might be less than totalAttempts due to 10k limit
	if totalAuditEntries == 0 {
		t.Error("Expected audit entries, got 0")
	}
}

// TestTokenRevocationLoad tests token revocation under concurrent load
func TestTokenRevocationLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	tokenManager := NewTokenManager("revocation-load-test-secret")

	// Generate tokens
	numTokens := 1000
	tokens := make([]string, numTokens)
	for i := 0; i < numTokens; i++ {
		token, err := tokenManager.GenerateToken(
			fmt.Sprintf("agent-%d", i),
			[]string{"testing"},
			1*time.Hour,
		)
		if err != nil {
			t.Fatalf("Failed to generate token: %v", err)
		}
		tokens[i] = token
	}

	// Concurrent revocations
	numGoroutines := 50
	var wg sync.WaitGroup
	var successCount atomic.Int32
	var errorCount atomic.Int32

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := goroutineID; j < numTokens; j += numGoroutines {
				err := tokenManager.RevokeToken(tokens[j])
				if err != nil {
					errorCount.Add(1)
				} else {
					successCount.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	t.Logf("Token Revocation Load Test:")
	t.Logf("  Total tokens: %d", numTokens)
	t.Logf("  Successful revocations: %d", successCount.Load())
	t.Logf("  Errors: %d", errorCount.Load())
	t.Logf("  Duration: %v", duration)
	t.Logf("  Revocations/sec: %.2f", float64(numTokens)/duration.Seconds())

	if errorCount.Load() > 0 {
		t.Errorf("Expected 0 errors, got %d", errorCount.Load())
	}

	// Verify revocations
	revokedCount := tokenManager.RevokedTokenCount()
	if revokedCount != numTokens {
		t.Errorf("Expected %d revoked tokens, got %d", numTokens, revokedCount)
	}
}

// TestMixedWorkload tests a realistic mixed workload of all operations
func TestMixedWorkload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping load test in short mode")
	}

	tokenManager := NewTokenManager("mixed-load-test-secret")
	policy := DefaultToolPolicy()
	approver := NewToolApprover(policy)
	interceptor := NewAuthInterceptor(tokenManager)

	numGoroutines := 100
	operationsPerGoroutine := 100
	totalOperations := numGoroutines * operationsPerGoroutine

	var wg sync.WaitGroup
	var tokenGenCount atomic.Int32
	var tokenValCount atomic.Int32
	var toolCheckCount atomic.Int32
	var tokenRevCount atomic.Int32

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < operationsPerGoroutine; j++ {
				op := (goroutineID*operationsPerGoroutine + j) % 4

				switch op {
				case 0: // Generate token
					_, err := tokenManager.GenerateToken(
						fmt.Sprintf("agent-%d-%d", goroutineID, j),
						[]string{"testing"},
						1*time.Hour,
					)
					if err == nil {
						tokenGenCount.Add(1)
					}

				case 1: // Validate token
					token, _ := tokenManager.GenerateToken(
						fmt.Sprintf("agent-%d-%d", goroutineID, j),
						[]string{"testing"},
						1*time.Hour,
					)
					_, err := tokenManager.ValidateToken(token)
					if err == nil {
						tokenValCount.Add(1)
					}

				case 2: // Check tool access
					token, _ := tokenManager.GenerateToken(
						fmt.Sprintf("agent-%d-%d", goroutineID, j),
						[]string{"code_analysis"},
						1*time.Hour,
					)
					md := metadata.Pairs("authorization", "Bearer "+token)
					ctx := metadata.NewIncomingContext(context.Background(), md)
					authCtx, err := interceptor.Authenticate(ctx)
					if err == nil {
						approver.CheckToolAccess(authCtx, "search")
						toolCheckCount.Add(1)
					}

				case 3: // Revoke token
					token, _ := tokenManager.GenerateToken(
						fmt.Sprintf("agent-%d-%d", goroutineID, j),
						[]string{"testing"},
						1*time.Hour,
					)
					err := tokenManager.RevokeToken(token)
					if err == nil {
						tokenRevCount.Add(1)
					}
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	t.Logf("Mixed Workload Test:")
	t.Logf("  Total operations: %d", totalOperations)
	t.Logf("  Token generations: %d", tokenGenCount.Load())
	t.Logf("  Token validations: %d", tokenValCount.Load())
	t.Logf("  Tool checks: %d", toolCheckCount.Load())
	t.Logf("  Token revocations: %d", tokenRevCount.Load())
	t.Logf("  Duration: %v", duration)
	t.Logf("  Operations/sec: %.2f", float64(totalOperations)/duration.Seconds())

	totalCompleted := tokenGenCount.Load() + tokenValCount.Load() + toolCheckCount.Load() + tokenRevCount.Load()
	if totalCompleted != int32(totalOperations) {
		t.Errorf("Expected %d operations, completed %d", totalOperations, totalCompleted)
	}
}

// BenchmarkTokenGeneration benchmarks token generation performance
func BenchmarkTokenGeneration(b *testing.B) {
	tokenManager := NewTokenManager("benchmark-secret")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := tokenManager.GenerateToken(
			fmt.Sprintf("agent-%d", i),
			[]string{"testing"},
			1*time.Hour,
		)
		if err != nil {
			b.Fatalf("Failed to generate token: %v", err)
		}
	}
}

// BenchmarkTokenValidation benchmarks token validation performance
func BenchmarkTokenValidation(b *testing.B) {
	tokenManager := NewTokenManager("benchmark-secret")
	token, _ := tokenManager.GenerateToken("test-agent", []string{"testing"}, 1*time.Hour)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := tokenManager.ValidateToken(token)
		if err != nil {
			b.Fatalf("Failed to validate token: %v", err)
		}
	}
}

// BenchmarkToolApproval benchmarks tool approval checking performance
func BenchmarkToolApproval(b *testing.B) {
	tokenManager := NewTokenManager("benchmark-secret")
	policy := DefaultToolPolicy()
	approver := NewToolApprover(policy)
	interceptor := NewAuthInterceptor(tokenManager)

	token, _ := tokenManager.GenerateToken("test-agent", []string{"code_analysis"}, 1*time.Hour)
	md := metadata.Pairs("authorization", "Bearer "+token)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	authCtx, _ := interceptor.Authenticate(ctx)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		approver.CheckToolAccess(authCtx, "search")
	}
}
