package storage

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func createPendingApprovalTestSession(t *testing.T, store *Store, sessionID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.CreateSession(&Session{
		ID: sessionID, Status: SessionStatusActive, CreatedAt: now, LastActive: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func createPendingApprovalForDecision(t *testing.T, store *Store, sessionID, approvalID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := store.CreatePendingApproval(&PendingApproval{
		ID: approvalID, SessionID: sessionID, ToolName: "write_file", ToolInput: `{"path":"README.md"}`,
		RiskScore: 50, Status: "pending", ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDecidePendingApproval_IdempotentExactAndConflict(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "approval.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createPendingApprovalTestSession(t, store, "session-approval")
	createPendingApprovalForDecision(t, store, "session-approval", "approval-one")

	decidedAt := time.Now().UTC()
	first, duplicate, err := store.DecidePendingApproval(
		"approval-one", "session-approval", "approved", "operator@example.test", "reviewed", decidedAt,
	)
	if err != nil || duplicate || first.Status != "approved" || first.DecidedBy != "operator@example.test" {
		t.Fatalf("first decision = %#v, duplicate=%v, err=%v", first, duplicate, err)
	}
	replay, duplicate, err := store.DecidePendingApproval(
		"approval-one", "session-approval", "approved", "operator@example.test", "reviewed", decidedAt.Add(time.Minute),
	)
	if err != nil || !duplicate || replay.Status != first.Status || !replay.DecidedAt.Equal(first.DecidedAt) {
		t.Fatalf("replayed decision = %#v, duplicate=%v, err=%v", replay, duplicate, err)
	}
	if _, _, err := store.DecidePendingApproval(
		"approval-one", "session-approval", "rejected", "operator@example.test", "reviewed", decidedAt,
	); !errors.Is(err, ErrApprovalDecisionConflict) {
		t.Fatalf("conflicting decision error = %v", err)
	}
	if _, _, err := store.DecidePendingApproval(
		"approval-one", "other-session", "approved", "operator@example.test", "reviewed", decidedAt,
	); !errors.Is(err, ErrApprovalDecisionConflict) {
		t.Fatalf("cross-session decision error = %v", err)
	}
}

func TestDecidePendingApproval_ConcurrentTwoStoreSingleWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approval-race.db")
	first, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	createPendingApprovalTestSession(t, first, "session-approval-race")
	createPendingApprovalForDecision(t, first, "session-approval-race", "approval-race")

	type result struct {
		status    string
		approval  *PendingApproval
		duplicate bool
		err       error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for index, candidate := range []struct {
		store  *Store
		status string
	}{
		{first, "approved"},
		{second, "rejected"},
	} {
		wg.Add(1)
		go func(index int, candidate struct {
			store  *Store
			status string
		}) {
			defer wg.Done()
			<-start
			approval, duplicate, err := candidate.store.DecidePendingApproval(
				"approval-race", "session-approval-race", candidate.status,
				fmt.Sprintf("operator-%d", index), "race", time.Now().UTC(),
			)
			results <- result{candidate.status, approval, duplicate, err}
		}(index, candidate)
	}
	close(start)
	wg.Wait()
	close(results)
	winner := ""
	conflicts := 0
	for result := range results {
		switch {
		case result.err == nil:
			if result.duplicate || result.approval == nil || result.approval.Status != result.status || winner != "" {
				t.Fatalf("invalid winning result %#v", result)
			}
			winner = result.status
		case errors.Is(result.err, ErrApprovalDecisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected decision error = %v", result.err)
		}
	}
	if winner == "" || conflicts != 1 {
		t.Fatalf("winner=%q conflicts=%d", winner, conflicts)
	}
	stored, err := second.GetPendingApproval("approval-race")
	if err != nil || stored == nil || stored.Status != winner {
		t.Fatalf("stored winner = %#v, %v", stored, err)
	}
}

func TestExpirePendingApproval_ExactCASAndRereadsWinner(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "approval-expire.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessionID := "session-approval-expire"
	createPendingApprovalTestSession(t, store, sessionID)
	now := time.Now().UTC()
	if err := store.CreatePendingApproval(&PendingApproval{
		ID: "approval-expire", SessionID: sessionID, ToolName: "write_file", ToolInput: "{}",
		Status: "pending", ExpiresAt: now.Add(-time.Minute), CreatedAt: now.Add(-2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	expired, changed, err := store.ExpirePendingApproval("approval-expire", sessionID)
	if err != nil || !changed || expired == nil || expired.Status != "expired" || expired.DecisionReason != "timeout" {
		t.Fatalf("first expiry = %#v changed=%v err=%v", expired, changed, err)
	}
	replay, changed, err := store.ExpirePendingApproval("approval-expire", sessionID)
	if err != nil || changed || replay == nil || replay.Status != "expired" || !replay.DecidedAt.Equal(expired.DecidedAt) {
		t.Fatalf("replayed expiry = %#v changed=%v err=%v", replay, changed, err)
	}
	if err := store.CreatePendingApproval(&PendingApproval{
		ID: "approval-future", SessionID: sessionID, ToolName: "write_file", ToolInput: "{}",
		Status: "pending", ExpiresAt: now.Add(5 * time.Minute), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	future, changed, err := store.ExpirePendingApproval("approval-future", sessionID)
	if err != nil || changed || future == nil || future.Status != "pending" {
		t.Fatalf("future expiry = %#v changed=%v err=%v", future, changed, err)
	}
}

func TestExpirePendingApproval_ConcurrentDecisionHasOneAuthoritativeWinner(t *testing.T) {
	for _, decisionStatus := range []string{"approved", "rejected"} {
		t.Run(decisionStatus, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "approval-expire-race.db")
			first, err := New(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = first.Close() })
			second, err := New(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = second.Close() })
			sessionID := "session-approval-expire-race"
			createPendingApprovalTestSession(t, first, sessionID)
			now := time.Now().UTC()
			if err := first.CreatePendingApproval(&PendingApproval{
				ID: "approval-expire-race", SessionID: sessionID, ToolName: "write_file", ToolInput: "{}",
				Status: "pending", ExpiresAt: now.Add(-time.Minute), CreatedAt: now.Add(-2 * time.Minute),
			}); err != nil {
				t.Fatal(err)
			}

			start := make(chan struct{})
			type result struct {
				approval *PendingApproval
				changed  bool
				err      error
			}
			decisions := make(chan result, 1)
			expiries := make(chan result, 1)
			go func() {
				<-start
				approval, _, err := second.DecidePendingApproval(
					"approval-expire-race", sessionID, decisionStatus, "operator", "race", time.Now().UTC(),
				)
				decisions <- result{approval: approval, err: err}
			}()
			go func() {
				<-start
				approval, changed, err := first.ExpirePendingApproval("approval-expire-race", sessionID)
				expiries <- result{approval: approval, changed: changed, err: err}
			}()
			close(start)
			decision := <-decisions
			expiry := <-expiries

			if decision.err != nil && !errors.Is(decision.err, ErrApprovalDecisionConflict) {
				t.Fatalf("decision error = %v", decision.err)
			}
			if expiry.err != nil {
				t.Fatalf("expiry error = %v", expiry.err)
			}
			stored, err := second.GetPendingApproval("approval-expire-race")
			if err != nil || stored == nil {
				t.Fatalf("stored winner = %#v, err=%v", stored, err)
			}
			switch stored.Status {
			case "expired":
				if !expiry.changed || expiry.approval == nil || decision.err == nil {
					t.Fatalf("expiry winner decision=%#v expiry=%#v", decision, expiry)
				}
			case decisionStatus:
				if expiry.changed || decision.err != nil || decision.approval == nil {
					t.Fatalf("decision winner decision=%#v expiry=%#v", decision, expiry)
				}
			default:
				t.Fatalf("unexpected stored winner = %#v", stored)
			}
		})
	}
}
