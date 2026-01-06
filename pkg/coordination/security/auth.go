package security

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var (
	ErrNoToken          = errors.New("no authentication token provided")
	ErrInvalidToken     = errors.New("invalid authentication token")
	ErrExpiredToken     = errors.New("token has expired")
	ErrRevokedToken     = errors.New("token has been revoked")
	ErrInsufficientAuth = errors.New("insufficient authentication")
	ErrNoCapability     = errors.New("missing required capability")
)

// Claims represents the JWT claims for an agent
type Claims struct {
	AgentID      string   `json:"agent_id"`
	Capabilities []string `json:"capabilities"`
	jwt.RegisteredClaims
}

// TokenManager manages JWT tokens for agent authentication
type TokenManager struct {
	secretKey     []byte
	mu            sync.RWMutex
	revokedTokens map[string]time.Time // token ID -> revocation time
}

// NewTokenManager creates a new token manager with the given secret key
func NewTokenManager(secretKey string) *TokenManager {
	return &TokenManager{
		secretKey:     []byte(secretKey),
		revokedTokens: make(map[string]time.Time),
	}
}

// GenerateToken generates a new JWT token for an agent
func (tm *TokenManager) GenerateToken(agentID string, capabilities []string, duration time.Duration) (string, error) {
	// Generate unique token ID
	tokenID, err := generateTokenID()
	if err != nil {
		return "", fmt.Errorf("failed to generate token ID: %w", err)
	}

	now := time.Now()
	claims := &Claims{
		AgentID:      agentID,
		Capabilities: capabilities,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        tokenID,
			Subject:   agentID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(duration)),
			NotBefore: jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signedToken, err := token.SignedString(tm.secretKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign token: %w", err)
	}

	return signedToken, nil
}

// ValidateToken validates a JWT token and returns the claims
func (tm *TokenManager) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Verify signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return tm.secretKey, nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	// Check if token is revoked
	tm.mu.RLock()
	_, revoked := tm.revokedTokens[claims.ID]
	tm.mu.RUnlock()

	if revoked {
		return nil, ErrRevokedToken
	}

	return claims, nil
}

// RevokeToken revokes a token by adding it to the revocation list
func (tm *TokenManager) RevokeToken(tokenString string) error {
	// Parse token to get ID (don't validate, just extract claims)
	token, _, err := jwt.NewParser().ParseUnverified(tokenString, &Claims{})
	if err != nil {
		return fmt.Errorf("failed to parse token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok {
		return ErrInvalidToken
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	tm.revokedTokens[claims.ID] = time.Now()
	return nil
}

// RefreshToken generates a new token based on an existing valid token
func (tm *TokenManager) RefreshToken(oldTokenString string, duration time.Duration) (string, error) {
	// Validate old token first
	claims, err := tm.ValidateToken(oldTokenString)
	if err != nil {
		return "", fmt.Errorf("cannot refresh invalid token: %w", err)
	}

	// Generate new token with same capabilities
	newToken, err := tm.GenerateToken(claims.AgentID, claims.Capabilities, duration)
	if err != nil {
		return "", err
	}

	// Revoke old token
	if err := tm.RevokeToken(oldTokenString); err != nil {
		return "", fmt.Errorf("failed to revoke old token: %w", err)
	}

	return newToken, nil
}

// CleanupRevokedTokens removes expired tokens from the revocation list
func (tm *TokenManager) CleanupRevokedTokens() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	// Remove tokens revoked more than 24 hours ago
	cutoff := time.Now().Add(-24 * time.Hour)
	for tokenID, revokedAt := range tm.revokedTokens {
		if revokedAt.Before(cutoff) {
			delete(tm.revokedTokens, tokenID)
		}
	}
}

// RevokedTokenCount returns the number of revoked tokens (for testing)
func (tm *TokenManager) RevokedTokenCount() int {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	return len(tm.revokedTokens)
}

// ValidateTokenUnsafe parses a token without validating it (for testing)
func (tm *TokenManager) ValidateTokenUnsafe(tokenString string) (*Claims, error) {
	token, _, err := jwt.NewParser().ParseUnverified(tokenString, &Claims{})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

// AuthInterceptor is a gRPC interceptor for authentication
type AuthInterceptor struct {
	tokenManager *TokenManager
}

// NewAuthInterceptor creates a new authentication interceptor
func NewAuthInterceptor(tokenManager *TokenManager) *AuthInterceptor {
	return &AuthInterceptor{
		tokenManager: tokenManager,
	}
}

// Authenticate validates the authentication token from context
func (ai *AuthInterceptor) Authenticate(ctx context.Context) (context.Context, error) {
	// Extract token from metadata
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}

	authHeaders := md.Get("authorization")
	if len(authHeaders) == 0 {
		return nil, status.Error(codes.Unauthenticated, "missing authorization header")
	}

	// Parse "Bearer <token>" format
	authHeader := authHeaders[0]
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return nil, status.Error(codes.Unauthenticated, "invalid authorization header format")
	}

	token := parts[1]
	if token == "" {
		return nil, status.Error(codes.Unauthenticated, "missing token")
	}

	// Validate token
	claims, err := ai.tokenManager.ValidateToken(token)
	if err != nil {
		if errors.Is(err, ErrExpiredToken) {
			return nil, status.Error(codes.Unauthenticated, "token expired")
		}
		if errors.Is(err, ErrRevokedToken) {
			return nil, status.Error(codes.Unauthenticated, "token revoked")
		}
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}

	// Add claims to context
	return ContextWithClaims(ctx, claims), nil
}

// UnaryInterceptor returns a gRPC unary server interceptor for authentication
func (ai *AuthInterceptor) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Skip authentication for certain methods (e.g., health checks)
		if shouldSkipAuth(info.FullMethod) {
			return handler(ctx, req)
		}

		// Authenticate
		authCtx, err := ai.Authenticate(ctx)
		if err != nil {
			return nil, err
		}

		// Call handler with authenticated context
		return handler(authCtx, req)
	}
}

// StreamInterceptor returns a gRPC stream server interceptor for authentication
func (ai *AuthInterceptor) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		// Skip authentication for certain methods
		if shouldSkipAuth(info.FullMethod) {
			return handler(srv, ss)
		}

		// Authenticate
		authCtx, err := ai.Authenticate(ss.Context())
		if err != nil {
			return err
		}

		// Create wrapped stream with authenticated context
		wrappedStream := &authenticatedStream{
			ServerStream: ss,
			ctx:          authCtx,
		}

		// Call handler with authenticated stream
		return handler(srv, wrappedStream)
	}
}

// authenticatedStream wraps grpc.ServerStream with an authenticated context
type authenticatedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authenticatedStream) Context() context.Context {
	return s.ctx
}

// Context keys
type claimsContextKey struct{}

// ContextWithClaims adds claims to context
func ContextWithClaims(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsContextKey{}, claims)
}

// ClaimsFromContext extracts claims from context
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	claims, ok := ctx.Value(claimsContextKey{}).(*Claims)
	return claims, ok
}

// RequireCapability checks if the authenticated agent has a required capability
func RequireCapability(ctx context.Context, capability string) error {
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		return ErrInsufficientAuth
	}

	for _, cap := range claims.Capabilities {
		if cap == capability {
			return nil
		}
	}

	return fmt.Errorf("%w: %s", ErrNoCapability, capability)
}

// shouldSkipAuth determines if authentication should be skipped for a method
func shouldSkipAuth(method string) bool {
	// Skip auth for health checks and similar endpoints
	skipMethods := []string{
		"/grpc.health.v1.Health/Check",
		"/grpc.health.v1.Health/Watch",
	}

	for _, skipMethod := range skipMethods {
		if method == skipMethod {
			return true
		}
	}

	return false
}

// generateTokenID generates a cryptographically random token ID
func generateTokenID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
