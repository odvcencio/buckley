package ipc

import (
	"strings"

	"github.com/odvcencio/buckley/pkg/storage"
)

func isOperatorPrincipal(principal *requestPrincipal) bool {
	if principal == nil {
		return false
	}
	return scopeRank[strings.ToLower(principal.Scope)] >= scopeRank[strings.ToLower(storage.TokenScopeOperator)]
}

func principalCanAccessSession(principal *requestPrincipal, session *storage.Session) bool {
	if principal == nil || session == nil {
		return false
	}
	if isOperatorPrincipal(principal) {
		return true
	}
	sessionPrincipal := strings.TrimSpace(session.Principal)
	if sessionPrincipal == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(principal.Name), sessionPrincipal)
}
