package ipc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	browserCSRFDomain      = "buckley.gosx.csrf.v1"
	browserCSRFEncodedSize = 43
)

var (
	errBrowserMutationForbidden    = errors.New("browser mutation forbidden")
	errBrowserSessionCookieInvalid = errors.New("browser session cookie invalid")
)

func browserCSRFToken(sessionValue string) (string, error) {
	if !validBrowserSessionValue(sessionValue) {
		return "", errBrowserMutationForbidden
	}
	mac := hmac.New(sha256.New, []byte(sessionValue))
	_, _ = mac.Write([]byte(browserCSRFDomain))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (b gosxBackend) ValidateMutation(ctx context.Context, r *http.Request, submittedToken string) error {
	if err := ctx.Err(); err != nil {
		return errBrowserMutationForbidden
	}
	if b.server == nil || b.server.store == nil || r == nil {
		return errBrowserMutationForbidden
	}
	principal := principalFromContext(r.Context())
	sessionValue, ok := requestBrowserSessionValue(r)
	if principal == nil || !ok || !b.server.activeBrowserSession(sessionValue, principal) {
		return errBrowserMutationForbidden
	}

	if len(submittedToken) != browserCSRFEncodedSize {
		return errBrowserMutationForbidden
	}
	decoded, err := base64.RawURLEncoding.DecodeString(submittedToken)
	if err != nil || len(decoded) != sha256.Size || base64.RawURLEncoding.EncodeToString(decoded) != submittedToken {
		return errBrowserMutationForbidden
	}
	mac := hmac.New(sha256.New, []byte(sessionValue))
	_, _ = mac.Write([]byte(browserCSRFDomain))
	if subtle.ConstantTimeCompare(decoded, mac.Sum(nil)) != 1 {
		return errBrowserMutationForbidden
	}
	return nil
}

func (s *Server) browserCSRFTokenForRequest(r *http.Request) string {
	if s == nil || r == nil {
		return ""
	}
	principal := principalFromContext(r.Context())
	sessionValue, ok := requestBrowserSessionValue(r)
	if principal == nil || !ok || !s.activeBrowserSession(sessionValue, principal) {
		return ""
	}
	token, err := browserCSRFToken(sessionValue)
	if err != nil {
		return ""
	}
	return token
}

func (s *Server) activeBrowserSession(sessionValue string, principal *requestPrincipal) bool {
	if s == nil || s.store == nil || principal == nil || !validBrowserSessionValue(sessionValue) {
		return false
	}
	session, err := s.store.GetAuthSession(sessionValue)
	if err != nil || session == nil || !session.ExpiresAt.After(time.Now()) {
		return false
	}
	return session.ID == sessionValue &&
		session.Principal == principal.Name &&
		session.Scope == principal.Scope &&
		session.TokenID == principal.TokenID
}

func requestBrowserSessionValue(r *http.Request) (string, bool) {
	value, present, err := exactRequestBrowserSessionValue(r)
	return value, present && err == nil
}

func exactRequestBrowserSessionValue(r *http.Request) (string, bool, error) {
	if r == nil {
		return "", false, nil
	}
	var value string
	count := 0
	for _, cookie := range r.Cookies() {
		if cookie.Name != sessionCookieName {
			continue
		}
		value = cookie.Value
		count++
	}
	if count == 0 {
		return "", false, nil
	}
	if count != 1 || !validBrowserSessionValue(value) {
		return "", true, errBrowserSessionCookieInvalid
	}
	return value, true, nil
}

func responseBrowserSessionValue(w http.ResponseWriter) (string, bool) {
	if w == nil {
		return "", false
	}
	response := &http.Response{Header: w.Header()}
	var value string
	count := 0
	for _, cookie := range response.Cookies() {
		if cookie.Name != sessionCookieName || cookie.MaxAge < 0 {
			continue
		}
		value = cookie.Value
		count++
	}
	return value, count == 1 && validBrowserSessionValue(value)
}

func validBrowserSessionValue(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value
}

func browserQueryHasToken(r *http.Request) bool {
	if r == nil || r.URL == nil {
		return false
	}
	for _, field := range strings.Split(r.URL.RawQuery, "&") {
		rawKey, _, _ := strings.Cut(field, "=")
		key, err := url.QueryUnescape(rawKey)
		if err == nil && key == "token" {
			return true
		}
	}
	return false
}

func browserLocalRedirect(r *http.Request) string {
	if r == nil || r.URL == nil {
		return "/"
	}
	requestPath := strings.ReplaceAll(r.URL.Path, "\\", "/")
	requestPath = path.Clean("/" + strings.TrimLeft(requestPath, "/"))
	if requestPath == "." || requestPath == "" {
		requestPath = "/"
	}
	query := r.URL.Query()
	query.Del("token")
	return (&url.URL{Path: requestPath, RawQuery: query.Encode()}).String()
}
