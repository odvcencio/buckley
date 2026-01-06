package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCLITicketsLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ticketID := "ticket-123"
	secret := "my-secret-code"
	label := "Test CLI Login"
	expires := time.Now().Add(5 * time.Minute)

	// Create ticket
	if err := store.CreateCLITicket(ticketID, secret, label, expires); err != nil {
		t.Fatalf("failed to create CLI ticket: %v", err)
	}

	// Get ticket
	ticket, err := store.GetCLITicket(ticketID)
	if err != nil {
		t.Fatalf("failed to get CLI ticket: %v", err)
	}
	if ticket == nil {
		t.Fatalf("expected ticket to exist")
	}
	if ticket.ID != ticketID {
		t.Errorf("expected ID=%q, got %q", ticketID, ticket.ID)
	}
	if ticket.Label != label {
		t.Errorf("expected Label=%q, got %q", label, ticket.Label)
	}
	if ticket.Approved {
		t.Errorf("expected ticket to not be approved")
	}
	if ticket.Consumed {
		t.Errorf("expected ticket to not be consumed")
	}

	// Test MatchesSecret
	if !ticket.MatchesSecret(secret) {
		t.Errorf("expected secret to match")
	}
	if ticket.MatchesSecret("wrong-secret") {
		t.Errorf("expected wrong secret to not match")
	}

	// Count pending tickets
	count, err := store.CountPendingCLITickets(time.Now())
	if err != nil {
		t.Fatalf("failed to count pending tickets: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 pending ticket, got %d", count)
	}

	// Approve ticket
	principal := "user@example.com"
	scope := "read:write"
	tokenID := "token-456"
	sessionToken := "session-789"
	if err := store.ApproveCLITicket(ticketID, principal, scope, tokenID, sessionToken); err != nil {
		t.Fatalf("failed to approve CLI ticket: %v", err)
	}

	// Get approved ticket
	ticket, err = store.GetCLITicket(ticketID)
	if err != nil {
		t.Fatalf("failed to get approved ticket: %v", err)
	}
	if !ticket.Approved {
		t.Errorf("expected ticket to be approved")
	}
	if ticket.Principal != principal {
		t.Errorf("expected Principal=%q, got %q", principal, ticket.Principal)
	}
	if ticket.Scope != scope {
		t.Errorf("expected Scope=%q, got %q", scope, ticket.Scope)
	}
	if ticket.TokenID != tokenID {
		t.Errorf("expected TokenID=%q, got %q", tokenID, ticket.TokenID)
	}
	if ticket.SessionToken != sessionToken {
		t.Errorf("expected SessionToken=%q, got %q", sessionToken, ticket.SessionToken)
	}

	// Approved tickets should not count as pending
	count, err = store.CountPendingCLITickets(time.Now())
	if err != nil {
		t.Fatalf("failed to count pending tickets: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 pending tickets, got %d", count)
	}

	// Consume ticket
	if err := store.ConsumeCLITicket(ticketID); err != nil {
		t.Fatalf("failed to consume CLI ticket: %v", err)
	}

	// Get consumed ticket
	ticket, err = store.GetCLITicket(ticketID)
	if err != nil {
		t.Fatalf("failed to get consumed ticket: %v", err)
	}
	if !ticket.Consumed {
		t.Errorf("expected ticket to be consumed")
	}
	if ticket.SessionToken != "" {
		t.Errorf("expected session token to be cleared after consumption, got %q", ticket.SessionToken)
	}
}

func TestGetCLITicketNonexistent(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ticket, err := store.GetCLITicket("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error for nonexistent ticket: %v", err)
	}
	if ticket != nil {
		t.Errorf("expected nil ticket for nonexistent ID, got %+v", ticket)
	}
}

func TestCleanupExpiredCLITickets(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()

	// Create expired ticket
	expiredID := "expired-ticket"
	if err := store.CreateCLITicket(expiredID, "secret1", "Expired", now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("failed to create expired ticket: %v", err)
	}

	// Create active ticket
	activeID := "active-ticket"
	if err := store.CreateCLITicket(activeID, "secret2", "Active", now.Add(1*time.Hour)); err != nil {
		t.Fatalf("failed to create active ticket: %v", err)
	}

	// Cleanup expired tickets
	deleted, err := store.CleanupExpiredCLITickets(now)
	if err != nil {
		t.Fatalf("failed to cleanup expired tickets: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted ticket, got %d", deleted)
	}

	// Verify expired ticket is gone
	ticket, err := store.GetCLITicket(expiredID)
	if err != nil {
		t.Fatalf("failed to get expired ticket: %v", err)
	}
	if ticket != nil {
		t.Errorf("expected expired ticket to be deleted, got %+v", ticket)
	}

	// Verify active ticket still exists
	ticket, err = store.GetCLITicket(activeID)
	if err != nil {
		t.Fatalf("failed to get active ticket: %v", err)
	}
	if ticket == nil {
		t.Errorf("expected active ticket to still exist")
	}
}

func TestCountPendingCLITickets(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()

	// No tickets initially
	count, err := store.CountPendingCLITickets(now)
	if err != nil {
		t.Fatalf("failed to count pending tickets: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 pending tickets, got %d", count)
	}

	// Create pending tickets
	for i := 0; i < 3; i++ {
		id := "pending-" + string(rune('0'+i))
		if err := store.CreateCLITicket(id, "secret", "label", now.Add(1*time.Hour)); err != nil {
			t.Fatalf("failed to create pending ticket %d: %v", i, err)
		}
	}

	// Create approved ticket (should not count as pending)
	approvedID := "approved-ticket"
	if err := store.CreateCLITicket(approvedID, "secret", "label", now.Add(1*time.Hour)); err != nil {
		t.Fatalf("failed to create approved ticket: %v", err)
	}
	if err := store.ApproveCLITicket(approvedID, "user", "read", "token", "session"); err != nil {
		t.Fatalf("failed to approve ticket: %v", err)
	}

	// Create consumed ticket (should not count as pending)
	consumedID := "consumed-ticket"
	if err := store.CreateCLITicket(consumedID, "secret", "label", now.Add(1*time.Hour)); err != nil {
		t.Fatalf("failed to create consumed ticket: %v", err)
	}
	if err := store.ConsumeCLITicket(consumedID); err != nil {
		t.Fatalf("failed to consume ticket: %v", err)
	}

	// Create expired ticket (should not count as pending)
	expiredID := "expired-ticket"
	if err := store.CreateCLITicket(expiredID, "secret", "label", now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("failed to create expired ticket: %v", err)
	}

	// Count should only include pending, non-consumed, non-expired tickets
	count, err = store.CountPendingCLITickets(now)
	if err != nil {
		t.Fatalf("failed to count pending tickets: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 pending tickets, got %d", count)
	}
}

func TestCLITicketMatchesSecretNil(t *testing.T) {
	var ticket *CLITicket
	if ticket.MatchesSecret("secret") {
		t.Errorf("expected nil ticket to not match any secret")
	}
}

func TestCLITicketsWhitespace(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ticketID := "ticket-123"
	label := "  Test Label  "
	expires := time.Now().Add(1 * time.Hour)

	// Create ticket with whitespace in label
	if err := store.CreateCLITicket(ticketID, "secret", label, expires); err != nil {
		t.Fatalf("failed to create ticket: %v", err)
	}

	ticket, err := store.GetCLITicket(ticketID)
	if err != nil {
		t.Fatalf("failed to get ticket: %v", err)
	}
	if ticket.Label != "Test Label" {
		t.Errorf("expected trimmed label, got %q", ticket.Label)
	}

	// Approve with whitespace
	if err := store.ApproveCLITicket(ticketID, "  user  ", "  scope  ", "  token  ", "session"); err != nil {
		t.Fatalf("failed to approve ticket: %v", err)
	}

	ticket, err = store.GetCLITicket(ticketID)
	if err != nil {
		t.Fatalf("failed to get approved ticket: %v", err)
	}
	if ticket.Principal != "user" {
		t.Errorf("expected trimmed principal, got %q", ticket.Principal)
	}
	if ticket.Scope != "scope" {
		t.Errorf("expected trimmed scope, got %q", ticket.Scope)
	}
	if ticket.TokenID != "token" {
		t.Errorf("expected trimmed tokenID, got %q", ticket.TokenID)
	}
}

func TestCLITicketsClosedStore(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	_ = store.Close()

	expires := time.Now().Add(1 * time.Hour)

	// Test operations on closed store - should return an error
	if err := store.CreateCLITicket("id", "secret", "label", expires); err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	_, err = store.GetCLITicket("id")
	if err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	if err := store.ApproveCLITicket("id", "principal", "scope", "token", "session"); err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	if err := store.ConsumeCLITicket("id"); err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	_, err = store.CleanupExpiredCLITickets(time.Now())
	if err == nil {
		t.Errorf("expected error for closed store, got nil")
	}

	_, err = store.CountPendingCLITickets(time.Now())
	if err == nil {
		t.Errorf("expected error for closed store, got nil")
	}
}
