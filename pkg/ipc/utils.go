package ipc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	stdliberrors "errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	apperrors "github.com/odvcencio/buckley/pkg/errors"
	"github.com/odvcencio/buckley/pkg/storage"
)

// rateLimiter provides simple per-key rate limiting.
type rateLimiter struct {
	interval time.Duration
	mu       sync.Mutex
	last     map[string]time.Time
}

func newRateLimiter(interval time.Duration) *rateLimiter {
	return &rateLimiter{
		interval: interval,
		last:     make(map[string]time.Time),
	}
}

func (r *rateLimiter) Allow(key string) bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if last, ok := r.last[key]; ok {
		if now.Sub(last) < r.interval {
			return false
		}
	}
	r.last[key] = now
	return true
}

// parseIntDefault parses an integer with a default fallback.
func parseIntDefault(raw string, def int) int {
	if raw == "" {
		return def
	}
	if v, err := strconv.Atoi(raw); err == nil && v > 0 {
		return v
	}
	return def
}

// respondJSON sends a JSON response with appropriate headers.
func respondJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

// respondError sends a structured JSON error response.
func respondError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)

	response := struct {
		Error       string   `json:"error"`
		Status      int      `json:"status"`
		Code        string   `json:"code,omitempty"`
		Message     string   `json:"message"`
		Details     string   `json:"details,omitempty"`
		Remediation []string `json:"remediation,omitempty"`
		Retryable   bool     `json:"retryable,omitempty"`
		Timestamp   string   `json:"timestamp"`
	}{
		Status:    status,
		Message:   http.StatusText(status),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	response.Error = response.Message

	var buckleyErr *apperrors.Error
	if stdliberrors.As(err, &buckleyErr) {
		response.Code = string(buckleyErr.Code)
		if buckleyErr.UserMessage != "" {
			response.Message = buckleyErr.UserMessage
		} else if buckleyErr.Message != "" {
			response.Message = buckleyErr.Message
		}
		if len(buckleyErr.Remediation) > 0 {
			response.Remediation = append([]string{}, buckleyErr.Remediation...)
		}
		response.Retryable = buckleyErr.Retryable
		if response.Details == "" {
			response.Details = buckleyErr.Error()
		}
	} else if err != nil {
		response.Message = err.Error()
	}

	if response.Details == "" && err != nil {
		response.Details = fmt.Sprintf("%v", err)
	}

	if len(response.Remediation) == 0 {
		response.Remediation = defaultRemediation(response.Code, status)
	}

	response.Error = response.Message
	_ = json.NewEncoder(w).Encode(response)
}

// defaultRemediation provides helpful remediation steps for common errors.
func defaultRemediation(code string, status int) []string {
	switch apperrors.ErrorCode(code) {
	case apperrors.ErrCodeModelTimeout, apperrors.ErrCodeModelAPIError:
		return []string{
			"Check your internet connection or OpenRouter status.",
			"Retry the request once connectivity is restored.",
		}
	case apperrors.ErrCodeModelRateLimit:
		return []string{
			"Wait 30-60 seconds for the model rate limit to reset.",
			"Reduce concurrent requests or upgrade your API plan.",
		}
	case apperrors.ErrCodeToolExecution, apperrors.ErrCodeToolTimeout:
		return []string{
			"Inspect the Terminal pane for the tool's stderr/stdout output.",
			"Resolve any command errors locally, then re-run the step.",
		}
	case apperrors.ErrCodeStorageRead, apperrors.ErrCodeStorageWrite:
		return []string{
			"Ensure the Buckley data directory is writable and not full.",
			"Restart Buckley if the SQLite database was locked.",
		}
	case apperrors.ErrCodePlanInvalid, apperrors.ErrCodeTaskFailed:
		return []string{
			"Open /workflow status to see which task failed.",
			"Adjust the plan or rerun the task after fixing the issue.",
		}
	}

	switch status {
	case http.StatusUnauthorized:
		return []string{
			"Verify your IPC token or authentication headers.",
			"Restart Buckley after updating credentials.",
		}
	case http.StatusForbidden:
		return []string{
			"Use a token with sufficient scope (viewer|member|operator).",
			"Re-run the request after updating credentials.",
		}
	case http.StatusNotFound:
		return []string{
			"Verify the resource ID in the request URL.",
			"Refresh Buckley's state and retry the action.",
		}
	case http.StatusTooManyRequests:
		return []string{
			"Slow down requests to Buckley.",
			"Wait a few seconds for the rate limiter to reset.",
		}
	case http.StatusServiceUnavailable:
		return []string{
			"Ensure the Buckley daemon is still running in the background.",
			"Retry after any long-running operations complete.",
		}
	default:
		return []string{
			"Check Buckley's logs (/workflow status or the desktop activity panel).",
			"Retry the action once the underlying issue is resolved.",
		}
	}
}

// principalFromContext extracts the request principal from context.
func principalFromContext(ctx context.Context) *requestPrincipal {
	if ctx == nil {
		return nil
	}
	if p, ok := ctx.Value(principalContextKey).(*requestPrincipal); ok {
		return p
	}
	return nil
}

// scopeRank maps token scopes to their authorization level.
var scopeRank = map[string]int{
	storage.TokenScopeViewer:   0,
	storage.TokenScopeMember:   1,
	storage.TokenScopeOperator: 2,
}

// requireScope checks that the request principal has at least the required scope.
func requireScope(w http.ResponseWriter, r *http.Request, required string) (*requestPrincipal, bool) {
	p := principalFromContext(r.Context())
	if p == nil {
		respondError(w, http.StatusUnauthorized, stdliberrors.New("unauthorized"))
		return nil, false
	}
	if scopeRank[strings.ToLower(p.Scope)] < scopeRank[strings.ToLower(required)] {
		respondError(w, http.StatusForbidden, stdliberrors.New("forbidden"))
		return nil, false
	}
	return p, true
}

// extractBearerToken extracts a bearer token from Authorization header or query param.
func extractBearerToken(r *http.Request) (token string, fromQuery bool) {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[len("Bearer "):]), false
	}
	if tok := r.URL.Query().Get("token"); tok != "" {
		return tok, true
	}
	return "", false
}

// randomHex generates a random hex string of n bytes.
func randomHex(n int) (string, error) {
	if n <= 0 {
		n = 16
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// schemeForRequest returns the scheme (http/https) for the request.
func schemeForRequest(r *http.Request) string {
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		return strings.ToLower(proto)
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// isRequestSecure returns true if the request is over HTTPS.
func isRequestSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
	return proto == "https"
}

// loginURL builds the CLI login URL for the given ticket.
func loginURL(r *http.Request, ticket string) string {
	scheme := schemeForRequest(r)
	host := r.Host
	return fmt.Sprintf("%s://%s/cli/login/%s", scheme, host, ticket)
}
