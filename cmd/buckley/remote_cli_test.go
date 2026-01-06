package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunRemoteSessionsAndTokensAndLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var createdTokenName string
	var revokedTokenID string
	loginURL := "http://example.com/approve"
	var polledTicketSecret string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/sessions" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessions": []map[string]any{
					{
						"id":         "s1",
						"status":     "active",
						"lastActive": time.Now().UTC().Format(time.RFC3339Nano),
						"gitBranch":  "main",
					},
				},
			})

		case r.URL.Path == "/api/config/api-tokens" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tokens": []map[string]any{
					{
						"id":       "tok1",
						"name":     "one",
						"scope":    "member",
						"prefix":   "buckley_",
						"lastUsed": "",
						"revoked":  false,
					},
				},
			})

		case r.URL.Path == "/api/config/api-tokens" && r.Method == http.MethodPost:
			var body struct {
				Name  string `json:"name"`
				Owner string `json:"owner"`
				Scope string `json:"scope"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			createdTokenName = body.Name
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "secret-token",
				"record": map[string]any{
					"id":     "tok2",
					"name":   body.Name,
					"scope":  body.Scope,
					"prefix": "buckley_",
				},
			})

		case strings.HasPrefix(r.URL.Path, "/api/config/api-tokens/") && r.Method == http.MethodDelete:
			revokedTokenID = strings.TrimPrefix(r.URL.Path, "/api/config/api-tokens/")
			w.WriteHeader(http.StatusOK)

		case r.URL.Path == "/api/cli/tickets" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ticket":    "t1",
				"secret":    "s1",
				"loginUrl":  loginURL,
				"expiresAt": time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
			})

		case r.URL.Path == "/api/cli/tickets/t1" && r.Method == http.MethodGet:
			polledTicketSecret = strings.TrimSpace(r.Header.Get("X-Buckley-CLI-Ticket-Secret"))
			if polledTicketSecret == "" {
				t.Fatalf("expected cli ticket secret header")
			}
			if q := r.URL.Query().Get("secret"); q != "" {
				t.Fatalf("expected no cli ticket secret in query, got %q", q)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":    "approved",
				"label":     "test",
				"expiresAt": time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
				"principal": map[string]any{"user": "test"},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	sessionsOut := captureStdout(t, func() {
		if err := runRemoteSessions([]string{"--url", srv.URL}); err != nil {
			t.Fatalf("runRemoteSessions: %v", err)
		}
	})
	if !strings.Contains(sessionsOut, "SESSION") || !strings.Contains(sessionsOut, "s1") {
		t.Fatalf("unexpected sessions output: %q", sessionsOut)
	}

	tokensListOut := captureStdout(t, func() {
		if err := runRemoteTokens([]string{"list", "--url", srv.URL}); err != nil {
			t.Fatalf("runRemoteTokens list: %v", err)
		}
	})
	if !strings.Contains(tokensListOut, "tok1") {
		t.Fatalf("unexpected tokens list output: %q", tokensListOut)
	}

	tokensCreateOut := captureStdout(t, func() {
		if err := runRemoteTokens([]string{"create", "--url", srv.URL, "--name", "two", "--scope", "operator"}); err != nil {
			t.Fatalf("runRemoteTokens create: %v", err)
		}
	})
	if createdTokenName != "two" || !strings.Contains(tokensCreateOut, "secret-token") {
		t.Fatalf("unexpected create output: %q name=%q", tokensCreateOut, createdTokenName)
	}

	tokensRevokeOut := captureStdout(t, func() {
		if err := runRemoteTokens([]string{"revoke", "--url", srv.URL, "--id", "tok1"}); err != nil {
			t.Fatalf("runRemoteTokens revoke: %v", err)
		}
	})
	if revokedTokenID != "tok1" || !strings.Contains(tokensRevokeOut, "Token revoked") {
		t.Fatalf("unexpected revoke output: %q id=%q", tokensRevokeOut, revokedTokenID)
	}

	loginOut := captureStdout(t, func() {
		if err := runRemoteLogin([]string{"--url", srv.URL, "--no-browser", "--label", "test"}); err != nil {
			t.Fatalf("runRemoteLogin: %v", err)
		}
	})
	if !strings.Contains(loginOut, "Ticket t1 created") || !strings.Contains(loginOut, "authenticated") {
		t.Fatalf("unexpected login output: %q", loginOut)
	}
	if polledTicketSecret != "s1" {
		t.Fatalf("unexpected polled ticket secret %q", polledTicketSecret)
	}

	// Ensure no auth store panic on Save with empty cookies.
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("expected HOME dir to exist: %v", err)
	}
}
