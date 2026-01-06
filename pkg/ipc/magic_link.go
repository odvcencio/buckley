package ipc

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/odvcencio/buckley/pkg/storage"
)

// MagicLinkRequest contains parameters for generating a magic link.
type MagicLinkRequest struct {
	Label      string `json:"label,omitempty"`
	TTLMinutes int    `json:"ttlMinutes,omitempty"` // Default: 15 minutes
	Scope      string `json:"scope,omitempty"`      // Default: "member"
}

// MagicLinkResponse contains the generated magic link.
type MagicLinkResponse struct {
	URL       string    `json:"url"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	Label     string    `json:"label,omitempty"`
}

// setupMagicLinkRoutes adds magic link auth routes.
func (s *Server) setupMagicLinkRoutes(r chi.Router) {
	r.Route("/auth", func(r chi.Router) {
		r.Post("/magic-link", s.handleCreateMagicLink)
	})
	// Public endpoint for redeeming magic links
	r.Get("/auth/magic/{token}", s.handleRedeemMagicLink)
}

// handleCreateMagicLink generates a single-use magic link for authentication.
func (s *Server) handleCreateMagicLink(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireScope(w, r, storage.TokenScopeMember)
	if !ok {
		return
	}

	var req MagicLinkRequest
	if status, err := decodeJSONBody(w, r, &req, maxBodyBytesTiny, true); err != nil {
		respondError(w, status, err)
		return
	}

	// Set defaults
	ttl := 15 * time.Minute
	if req.TTLMinutes > 0 && req.TTLMinutes <= 60 {
		ttl = time.Duration(req.TTLMinutes) * time.Minute
	}

	scope := req.Scope
	if scope == "" {
		scope = storage.TokenScopeMember
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope != storage.TokenScopeViewer && scope != storage.TokenScopeMember && scope != storage.TokenScopeOperator {
		scope = storage.TokenScopeMember
	}
	if scopeRank[scope] > scopeRank[strings.ToLower(strings.TrimSpace(principal.Scope))] {
		respondError(w, http.StatusForbidden, fmt.Errorf("requested scope exceeds your scope"))
		return
	}

	principalName := principal.Name
	label := req.Label
	if label == "" {
		label = fmt.Sprintf("Magic link from %s", principalName)
	}

	// Generate secure token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Errorf("failed to generate token"))
		return
	}
	token := hex.EncodeToString(tokenBytes)

	// Generate ticket ID
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Errorf("failed to generate ID"))
		return
	}
	ticketID := "ml_" + hex.EncodeToString(idBytes)

	expiresAt := time.Now().Add(ttl)

	// Create the ticket (reusing CLI ticket infrastructure)
	if err := s.store.CreateCLITicket(ticketID, token, label, expiresAt); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	// Pre-approve the ticket since it's created by an authenticated user
	if err := s.store.ApproveCLITicket(ticketID, principalName, scope, "", ""); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	// Build the URL
	baseURL := s.getExternalURL(r)
	magicURL := fmt.Sprintf("%s/auth/magic/%s?id=%s", baseURL, token, ticketID)

	resp := MagicLinkResponse{
		URL:       magicURL,
		Token:     token,
		ExpiresAt: expiresAt,
		Label:     label,
	}

	w.WriteHeader(http.StatusCreated)
	respondJSON(w, resp)
}

// handleRedeemMagicLink exchanges a magic link token for a session.
func (s *Server) handleRedeemMagicLink(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	ticketID := r.URL.Query().Get("id")

	if token == "" || ticketID == "" {
		respondError(w, http.StatusBadRequest, fmt.Errorf("missing token or id"))
		return
	}

	// Look up the ticket
	ticket, err := s.store.GetCLITicket(ticketID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	// Validate ticket
	if ticket == nil {
		respondError(w, http.StatusNotFound, fmt.Errorf("invalid or expired link"))
		return
	}

	if time.Now().After(ticket.ExpiresAt) {
		respondError(w, http.StatusGone, fmt.Errorf("link has expired"))
		return
	}

	if !ticket.MatchesSecret(token) {
		respondError(w, http.StatusUnauthorized, fmt.Errorf("invalid token"))
		return
	}

	if ticket.Consumed {
		respondError(w, http.StatusGone, fmt.Errorf("link has already been used"))
		return
	}

	if !ticket.Approved {
		respondError(w, http.StatusForbidden, fmt.Errorf("link not approved"))
		return
	}

	// Mark as consumed
	if err := s.store.ConsumeCLITicket(ticketID); err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	// Create a web auth session for the user
	sessionToken, err := s.issueAuthSession(&requestPrincipal{
		Name:    ticket.Principal,
		Scope:   ticket.Scope,
		TokenID: ticket.TokenID,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}

	// Set session cookie
	s.setSessionCookie(w, r, sessionToken)

	// Check if this is an API request or browser request
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/json") {
		respondJSON(w, map[string]any{
			"success":   true,
			"principal": ticket.Principal,
			"scope":     ticket.Scope,
			"message":   "Authentication successful",
		})
		return
	}

	// Redirect to the app for browser requests
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// getExternalURL determines the external URL for building links.
func (s *Server) getExternalURL(r *http.Request) string {
	// Check for forwarded headers first
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		host := r.Header.Get("X-Forwarded-Host")
		if host == "" {
			host = r.Host
		}
		return fmt.Sprintf("%s://%s", proto, host)
	}

	// Check config
	if s.cfg.ExternalURL != "" {
		return strings.TrimSuffix(s.cfg.ExternalURL, "/")
	}

	// Fallback to request info
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, r.Host)
}
