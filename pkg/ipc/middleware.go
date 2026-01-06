package ipc

import (
	"context"
	stdliberrors "errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/odvcencio/buckley/pkg/storage"
)

// corsMiddleware adds CORS headers based on allowed origins configuration.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if allowed, wildcard := s.isOriginAllowed(origin); allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				if !wildcard {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
			}
		}
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Buckley-Session-Token, Connect-Protocol-Version, Connect-Accept-Encoding")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// securityHeadersMiddleware adds standard security headers to responses.
func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := w.Header()
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("X-Frame-Options", "DENY")
		headers.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		headers.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=(), payment=(), usb=()")
		if s.cfg.EnableBrowser {
			headers.Set("Content-Security-Policy", browserCSP(r, false))
		}
		next.ServeHTTP(w, r)
	})
}

// browserCSP builds a Content-Security-Policy header for browser requests.
func browserCSP(r *http.Request, allowInlineScript bool) string {
	scriptPolicy := "script-src 'self'"
	if allowInlineScript {
		scriptPolicy = "script-src 'self' 'unsafe-inline'"
	}

	connectPolicy := "connect-src 'self' ws: wss:"
	if wsSources := websocketSourcesForHost(r); wsSources != "" {
		connectPolicy = "connect-src 'self' " + wsSources
	}

	return strings.Join([]string{
		"default-src 'self'",
		"base-uri 'self'",
		"object-src 'none'",
		"frame-ancestors 'none'",
		"img-src 'self' data:",
		"font-src 'self' data:",
		"style-src 'self' 'unsafe-inline'",
		scriptPolicy,
		connectPolicy,
		"manifest-src 'self'",
		"worker-src 'self'",
	}, "; ") + ";"
}

// websocketSourcesForHost returns CSP-safe WebSocket sources for the request host.
func websocketSourcesForHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	// Check for injection characters BEFORE trimming to prevent bypass
	if strings.ContainsAny(r.Host, " \t\r\n\"'") {
		return ""
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	return fmt.Sprintf("ws://%s wss://%s", host, host)
}

// sessionMiddleware attaches the principal from session cookie if present.
func (s *Server) sessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principalFromContext(r.Context()) != nil {
			next.ServeHTTP(w, r)
			return
		}
		principal, _ := s.principalFromSessionCookie(r)
		if principal != nil {
			ctx := context.WithValue(r.Context(), principalContextKey, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// basicAuthMiddleware handles HTTP Basic Authentication.
func (s *Server) basicAuthMiddleware(next http.Handler) http.Handler {
	if !s.cfg.BasicAuthEnabled {
		return next
	}
	realm := `Basic realm="Buckley"`
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isUnauthenticatedEndpoint(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if principalFromContext(r.Context()) != nil {
			next.ServeHTTP(w, r)
			return
		}
		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		lowerAuth := strings.ToLower(authHeader)
		if strings.HasPrefix(lowerAuth, "bearer ") {
			token := strings.TrimSpace(authHeader[len("Bearer "):])
			if token != "" {
				principal, ok := s.authorize(r)
				if !ok {
					respondError(w, http.StatusUnauthorized, stdliberrors.New("unauthorized"))
					return
				}
				ctx := context.WithValue(r.Context(), principalContextKey, principal)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		username, password, ok := r.BasicAuth()
		if !ok || username != s.cfg.BasicAuthUsername || password != s.cfg.BasicAuthPassword {
			w.Header().Set("WWW-Authenticate", realm)
			respondError(w, http.StatusUnauthorized, stdliberrors.New("unauthorized"))
			return
		}
		principal := &requestPrincipal{Name: username, Scope: storage.TokenScopeOperator}
		token, err := s.issueAuthSession(principal)
		if err != nil {
			respondError(w, http.StatusInternalServerError, err)
			return
		}
		s.setSessionCookie(w, r, token)
		ctx := context.WithValue(r.Context(), principalContextKey, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isOriginAllowed checks if the provided origin is in the allowed origins list.
func (s *Server) isOriginAllowed(origin string) (allowed bool, wildcard bool) {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return false, false
	}

	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false, false
	}

	scheme := strings.ToLower(parsed.Scheme)
	host := parsed.Host
	normalized := scheme + "://" + host

	wildcardPresent := false
	for _, allowedOrigin := range s.cfg.AllowedOrigins {
		allowedOrigin = strings.TrimSpace(allowedOrigin)
		if allowedOrigin == "" {
			continue
		}
		if allowedOrigin == "*" {
			wildcardPresent = true
			continue
		}
		if strings.EqualFold(allowedOrigin, origin) || strings.EqualFold(allowedOrigin, normalized) {
			return true, false
		}
		allowedURL, err := url.Parse(allowedOrigin)
		if err != nil || allowedURL.Scheme == "" || allowedURL.Host == "" {
			continue
		}
		if !strings.EqualFold(allowedURL.Scheme, scheme) {
			continue
		}
		if originHostsMatch(allowedURL.Host, host, scheme) {
			return true, false
		}
	}

	if wildcardPresent {
		return true, true
	}
	return false, false
}

// originHostsMatch compares host:port combinations for origin matching.
func originHostsMatch(allowedHost, originHost, scheme string) bool {
	allowedName, allowedPort, allowedHasPort := splitHostPortLoose(allowedHost)
	originName, originPort, originHasPort := splitHostPortLoose(originHost)
	if allowedName == "" || originName == "" {
		return false
	}
	if !strings.EqualFold(allowedName, originName) {
		return false
	}

	originEffectivePort := originPort
	if !originHasPort {
		originEffectivePort = defaultPortForScheme(scheme)
	}

	if allowedHasPort {
		allowedEffectivePort := allowedPort
		if allowedEffectivePort == "" {
			allowedEffectivePort = defaultPortForScheme(scheme)
		}
		return allowedEffectivePort == originEffectivePort
	}

	if strings.EqualFold(allowedName, "localhost") {
		return true
	}
	if ip := net.ParseIP(allowedName); ip != nil && ip.IsLoopback() {
		return true
	}

	return originEffectivePort == defaultPortForScheme(scheme)
}

// splitHostPortLoose parses host:port without strict validation.
func splitHostPortLoose(hostport string) (host, port string, hasPort bool) {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return "", "", false
	}
	host, port, err := net.SplitHostPort(hostport)
	if err == nil {
		return host, port, true
	}
	if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]"), "", false
	}
	return hostport, "", false
}

// defaultPortForScheme returns the default port for http/https.
func defaultPortForScheme(scheme string) string {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "https":
		return "443"
	default:
		return "80"
	}
}

// isWebSocketOriginAllowed checks if a WebSocket upgrade request has an allowed origin.
func (s *Server) isWebSocketOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err == nil && parsed.Host != "" && strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	allowed, _ := s.isOriginAllowed(origin)
	return allowed
}

// authMiddleware requires authentication and short-circuits if unauthorized.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := s.authorize(r)
		if !ok {
			respondError(w, http.StatusUnauthorized, stdliberrors.New("unauthorized"))
			return
		}
		ctx := context.WithValue(r.Context(), principalContextKey, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authContextMiddleware attaches the current request principal if authorized.
// Unlike authMiddleware, it does not short-circuit unauthenticated requests. This is
// used for Connect/gRPC endpoints so Connect can return protocol-native errors.
func (s *Server) authContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := s.authorize(r)
		if ok {
			ctx := context.WithValue(r.Context(), principalContextKey, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authorize validates the request and returns the associated principal.
func (s *Server) authorize(r *http.Request) (*requestPrincipal, bool) {
	if principal := principalFromContext(r.Context()); principal != nil {
		return principal, true
	}
	token, fromQuery := extractBearerToken(r)
	if fromQuery && !isLoopbackBindAddress(s.cfg.BindAddress) {
		token = ""
	}
	if token != "" {
		if s.cfg.AuthToken != "" && token == s.cfg.AuthToken {
			return &requestPrincipal{Name: "builtin", Scope: storage.TokenScopeOperator}, true
		}
		if p := s.validateBearerToken(token); p != nil {
			return p, true
		}
		return nil, false
	}
	if s.cfg.RequireToken {
		if !s.isUnauthenticatedEndpoint(r.URL.Path) {
			return nil, false
		}
	}
	return &requestPrincipal{Name: "anonymous", Scope: storage.TokenScopeViewer}, true
}

// isUnauthenticatedEndpoint returns true for endpoints that don't require auth.
func (s *Server) isUnauthenticatedEndpoint(path string) bool {
	switch strings.TrimSpace(path) {
	case "/healthz":
		return true
	case "/metrics":
		return s.cfg.PublicMetrics
	default:
		return false
	}
}
