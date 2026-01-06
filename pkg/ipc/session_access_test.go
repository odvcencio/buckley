package ipc

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/storage"
)

func TestIsOperatorPrincipal(t *testing.T) {
	tests := []struct {
		name      string
		principal *requestPrincipal
		want      bool
	}{
		{
			name:      "nil principal",
			principal: nil,
			want:      false,
		},
		{
			name:      "operator scope",
			principal: &requestPrincipal{Name: "admin", Scope: storage.TokenScopeOperator},
			want:      true,
		},
		{
			name:      "member scope",
			principal: &requestPrincipal{Name: "user", Scope: storage.TokenScopeMember},
			want:      false,
		},
		{
			name:      "viewer scope",
			principal: &requestPrincipal{Name: "guest", Scope: storage.TokenScopeViewer},
			want:      false,
		},
		{
			name:      "operator scope uppercase",
			principal: &requestPrincipal{Name: "admin", Scope: "OPERATOR"},
			want:      true,
		},
		{
			name:      "empty scope",
			principal: &requestPrincipal{Name: "empty", Scope: ""},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isOperatorPrincipal(tt.principal)
			if got != tt.want {
				t.Errorf("isOperatorPrincipal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPrincipalCanAccessSession(t *testing.T) {
	tests := []struct {
		name      string
		principal *requestPrincipal
		session   *storage.Session
		want      bool
	}{
		{
			name:      "nil principal",
			principal: nil,
			session:   &storage.Session{ID: "s1", Principal: "alice"},
			want:      false,
		},
		{
			name:      "nil session",
			principal: &requestPrincipal{Name: "alice", Scope: storage.TokenScopeMember},
			session:   nil,
			want:      false,
		},
		{
			name:      "both nil",
			principal: nil,
			session:   nil,
			want:      false,
		},
		{
			name:      "operator can access any session",
			principal: &requestPrincipal{Name: "admin", Scope: storage.TokenScopeOperator},
			session:   &storage.Session{ID: "s1", Principal: "bob"},
			want:      true,
		},
		{
			name:      "member can access own session",
			principal: &requestPrincipal{Name: "alice", Scope: storage.TokenScopeMember},
			session:   &storage.Session{ID: "s1", Principal: "alice"},
			want:      true,
		},
		{
			name:      "member cannot access other's session",
			principal: &requestPrincipal{Name: "alice", Scope: storage.TokenScopeMember},
			session:   &storage.Session{ID: "s1", Principal: "bob"},
			want:      false,
		},
		{
			name:      "viewer can access own session",
			principal: &requestPrincipal{Name: "alice", Scope: storage.TokenScopeViewer},
			session:   &storage.Session{ID: "s1", Principal: "alice"},
			want:      true,
		},
		{
			name:      "viewer cannot access other's session",
			principal: &requestPrincipal{Name: "alice", Scope: storage.TokenScopeViewer},
			session:   &storage.Session{ID: "s1", Principal: "bob"},
			want:      false,
		},
		{
			name:      "session with empty principal",
			principal: &requestPrincipal{Name: "alice", Scope: storage.TokenScopeMember},
			session:   &storage.Session{ID: "s1", Principal: ""},
			want:      false,
		},
		{
			name:      "case insensitive principal match",
			principal: &requestPrincipal{Name: "Alice", Scope: storage.TokenScopeMember},
			session:   &storage.Session{ID: "s1", Principal: "alice"},
			want:      true,
		},
		{
			name:      "principal with whitespace",
			principal: &requestPrincipal{Name: " alice ", Scope: storage.TokenScopeMember},
			session:   &storage.Session{ID: "s1", Principal: "alice"},
			want:      true,
		},
		{
			name:      "session principal with whitespace",
			principal: &requestPrincipal{Name: "alice", Scope: storage.TokenScopeMember},
			session:   &storage.Session{ID: "s1", Principal: " alice "},
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := principalCanAccessSession(tt.principal, tt.session)
			if got != tt.want {
				t.Errorf("principalCanAccessSession() = %v, want %v", got, tt.want)
			}
		})
	}
}
